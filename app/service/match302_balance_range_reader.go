package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type httpRangeReadSeeker struct {
	ctx     context.Context
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
		ctx:     context.Background(),
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
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	for key, values := range r.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.offset, end))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return 0, fmt.Errorf("源直链拒绝片段读取: HTTP %d，需检查直链有效期、下载请求头及出口 IP；range=bytes=%d-%d", resp.StatusCode, r.offset, end)
		}
		return 0, fmt.Errorf("源直链未返回分段内容: HTTP %d range=bytes=%d-%d", resp.StatusCode, r.offset, end)
	}
	wantRange := fmt.Sprintf("bytes %d-%d/%d", r.offset, end, r.size)
	if got := resp.Header.Get("Content-Range"); got != wantRange {
		return 0, fmt.Errorf("源直链 Content-Range 不匹配: got=%q want=%q", got, wantRange)
	}
	length := end - r.offset + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil {
		return 0, err
	}
	if int64(len(body)) != length {
		return 0, fmt.Errorf("源直链片段长度不匹配: got=%d want=%d", len(body), length)
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
