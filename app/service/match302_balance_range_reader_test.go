package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestTransferRangeReaderValidatesResponse(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		status                        int
		contentRange, body, wantError string
	}{
		{"valid", 206, "bytes 2-5/16", "2345", ""},
		{"forbidden", 403, "", "forbidden", "拒绝片段读取"},
		{"whole file", 200, "", "0123456789abcdef", "未返回分段内容"},
		{"wrong offset", 206, "bytes 0-3/16", "0123", "Content-Range"},
		{"wrong total", 206, "bytes 2-5/20", "2345", "Content-Range"},
		{"missing range", 206, "", "2345", "Content-Range"},
		{"truncated", 206, "bytes 2-5/16", "23", "长度不匹配"},
		{"oversized", 206, "bytes 2-5/16", strings.Repeat("x", 10000), "长度不匹配"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=2-5" || r.Header.Get("Accept-Encoding") != "identity" {
					t.Errorf("incorrect headers: %v", r.Header)
				}
				w.Header().Set("Content-Range", tt.contentRange)
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			reader := newHTTPRangeReadSeeker(server.URL, nil, 16)
			digest, err := hashWeb115RangeSHA1(reader, "2-5")
			if tt.wantError == "" {
				if err != nil || digest != "D2F75E8204FEDF2EACD261E2461B2964E3BFD5BE" {
					t.Fatalf("digest=%s err=%v", digest, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("err=%v want=%s", err, tt.wantError)
			}
		})
	}
}

func TestTransferRangeRejectsInvalidChallengeBeforeNetwork(t *testing.T) {
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid range caused a network request")
		return nil, io.EOF
	})}
	for _, spec := range []string{"-1-3", "4-2", "2-5junk", "2-5-9", "0-16", "0-9223372036854775807", ""} {
		if _, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}, BalanceSourceFile{Size: 16}, spec); err == nil {
			t.Fatalf("accepted range %q", spec)
		}
	}
}

func TestOpenAPIRange403DoesNotFallBackInOpenAPIOnlyMode(t *testing.T) {
	calls := 0
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "cdn.test" {
			return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("Forbidden")), Request: req}, nil
		}
		if req.URL.Path != "/open/ufile/downurl" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{"id": map[string]any{"url": map[string]string{"url": "https://cdn.test/file"}}}}), nil
	})}
	_, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "token", Cookie: "must-not-be-used", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}, BalanceSourceFile{Size: 16, PickCode: "pick"}, "2-5")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
