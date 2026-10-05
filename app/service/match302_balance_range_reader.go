package service

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

type httpRangeReadSeeker struct {
	url     string
	headers http.Header
	client  *http.Client
	offset  int64
	size    int64
}

func newHTTPRangeReadSeeker(rawURL string, headers http.Header, size int64) *httpRangeReadSeeker {
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		for _, value := range values {
			cloned.Add(key, value)
		}
	}
	return &httpRangeReadSeeker{
		url:     rawURL,
		headers: cloned,
		client:  &http.Client{Timeout: 30 * time.Second},
		size:    size,
	}
}

func (r *httpRangeReadSeeker) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.size > 0 && r.offset >= r.size {
		return 0, io.EOF
	}
	end := r.offset + int64(len(p)) - 1
	if r.size > 0 && end >= r.size {
		end = r.size - 1
	}
	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	for key, values := range r.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.offset, end))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("源直链不支持 Range 请求: HTTP %d range=bytes=%d-%d", resp.StatusCode, r.offset, end)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if len(body) == 0 {
		return 0, io.EOF
	}
	n := copy(p, body)
	r.offset += int64(n)
	return n, nil
}

func (r *httpRangeReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	r.offset = next
	return r.offset, nil
}
