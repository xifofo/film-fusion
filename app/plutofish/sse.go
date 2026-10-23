package plutofish

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	maxLineBytes  = 1 << 20
	maxFrameBytes = 4 << 20
)

type sseFrame struct {
	id    string
	event string
	data  string
}

// readSSE holds only one bounded frame and invokes consume synchronously. Slow
// durable writes backpressure the socket rather than filling a memory queue.
func readSSE(r io.Reader, consume func(sseFrame) error, retry func(time.Duration)) error {
	reader := bufio.NewReaderSize(r, 32*1024)
	var frame sseFrame
	var data strings.Builder
	var hasData, skipLF bool
	firstLine := true
	for {
		line, err := readSSELine(reader, &skipLF)
		if err != nil {
			// A frame is committed only after its terminating empty line. A
			// partial final frame is replayed from the durable cursor.
			return err
		}
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		if line == "" {
			if hasData {
				frame.data = strings.TrimSuffix(data.String(), "\n")
				if err := consume(frame); err != nil {
					return err
				}
			}
			frame = sseFrame{}
			data.Reset()
			hasData = false
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "data":
			if data.Len()+len(value)+1 > maxFrameBytes {
				return errors.New("SSE frame exceeds bounded size")
			}
			data.WriteString(value)
			data.WriteByte('\n')
			hasData = true
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				frame.id = value
			}
		case "event":
			frame.event = value
		case "retry":
			if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				continue
			}
			if millis, err := strconv.ParseUint(value, 10, 32); err == nil {
				retry(time.Duration(millis) * time.Millisecond)
			}
		}
	}
}

// readSSELine accepts LF, CRLF and CR, including delimiters split over reads.
func readSSELine(r *bufio.Reader, skipLF *bool) (string, error) {
	line := make([]byte, 0, 256)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if *skipLF {
			*skipLF = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' {
			*skipLF = true
			return string(line), nil
		}
		if b == '\n' {
			return string(line), nil
		}
		if len(line) >= maxLineBytes {
			return "", errors.New("SSE line exceeds bounded size")
		}
		line = append(line, b)
	}
}

// idleBody times only blocked network reads, not the whole long-lived stream.
// Closing an HTTP response body unblocks its current Read, including on cancel.
type idleBody struct {
	io.ReadCloser
	timeout time.Duration
}

func (b idleBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.timeout, func() { _ = b.ReadCloser.Close() })
	n, err := b.ReadCloser.Read(p)
	timer.Stop()
	return n, err
}
