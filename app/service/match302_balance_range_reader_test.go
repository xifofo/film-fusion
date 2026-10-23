package service

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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
			digest, err := hashTransferHTTPRange(context.Background(), server.Client(), server.URL, nil, 16, 2, 5)
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

func TestTransferRangeRejectionReportsOnlySafeDetails(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		cookie             bool
	}{
		{"missing cookie", `{"message":"no cookie value","request_id":"secret"}`, "no cookie value", false},
		{"cookie sent but rejected", `{"message":"no cookie value"}`, "no cookie value", true},
		{"untrusted message", `{"message":"signature=secret; UID=secret; https://cdn.test/secret"}`, "未识别", true},
		{"html", `<html>secret</html>`, "未识别", false},
		{"oversized", `{"message":"no cookie value","extra":"` + strings.Repeat("x", 5000) + `"}`, "未识别", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(tc.body)
			calls := 0
			client := &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(body), Request: req}, nil
			})}
			headers := http.Header{"User-Agent": {web115TransferUA}}
			if tc.cookie {
				headers.Set("Cookie", "download=secret")
			}
			_, err := hashTransferHTTPRange(context.Background(), client, "https://cdn.test/secret?signature=secret", headers, 16, 2, 5)
			if err == nil || !strings.Contains(err.Error(), "CDN 原因="+tc.reason) ||
				!strings.Contains(err.Error(), fmt.Sprintf("已携带下载 Cookie=%t", tc.cookie)) || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if len(tc.body)-body.Len() > 4097 {
				t.Fatal("read too much error body")
			}
		})
	}
}

func TestTransferRangeReadsWholeChallengeOnce(t *testing.T) {
	const start, length = int64(12327226701), int64(128 * 1024)
	end, size := start+length-1, start+length+100
	content := strings.Repeat("abcd", int(length/4))
	file := BalanceSourceFile{FileID: "123", PickCode: "pick", Size: size, SHA1: strings.Repeat("A", 40)}
	jar, _ := cookiejar.New(nil)
	for _, address := range []string{"https://proapi.115.com", "https://cdn.test"} {
		u, _ := url.Parse(address)
		jar.SetCookies(u, []*http.Cookie{{Name: "account", Value: "must-not-leak"}})
	}
	apiCalls, rangeCalls := 0, 0
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Jar: jar, Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "proapi.115.com" {
			apiCalls++
			if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "Bearer source-token" || req.UserAgent() != web115TransferUA {
				t.Fatalf("incorrect issuance headers: %v", req.Header)
			}
			resp := balanceOpenAPIResponse(req, map[string]any{"state": 1, "data": map[string]any{"123": map[string]any{
				"file_size": fmt.Sprint(size), "sha1": file.SHA1, "url": map[string]string{"url": "https://cdn.test/file"},
			}}})
			resp.Header.Del("Content-Type")
			resp.Header.Add("Set-Cookie", "download=grant; Path=/; HttpOnly")
			resp.Header.Add("Set-Cookie", "signature=proof; Path=/; Secure")
			return resp, nil
		}
		if req.URL.Host != "cdn.test" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		rangeCalls++
		if req.Header.Get("Range") != fmt.Sprintf("bytes=%d-%d", start, end) || req.Header.Get("Accept-Encoding") != "identity" ||
			req.Header.Get("Cookie") != "download=grant; signature=proof" || req.UserAgent() != web115TransferUA || req.Header.Get("Authorization") != "" {
			t.Fatalf("incorrect range headers: %v", req.Header)
		}
		return &http.Response{StatusCode: 206, ContentLength: -1, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, size)}},
			Body: io.NopCloser(strings.NewReader(content)), Request: req}, nil
	})}
	got, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "source-token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}, file, fmt.Sprintf("%d-%d", start, end))
	if err != nil || got != fmt.Sprintf("%X", sha1.Sum([]byte(content))) || apiCalls != 1 || rangeCalls != 1 {
		t.Fatalf("hash=%s err=%v api=%d range=%d", got, err, apiCalls, rangeCalls)
	}
	if svc.httpClient.Jar != jar {
		t.Fatal("changed caller's CookieJar")
	}
}

func TestTransferRangeRejectsMismatchedSourceBeforeCDN(t *testing.T) {
	for _, field := range []string{"file_id", "file_size", "sha1", "pick_code"} {
		t.Run(field, func(t *testing.T) {
			svc := NewWeb115Service(nil)
			calls := 0
			svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "proapi.115.com" {
					t.Fatal("mismatched source reached CDN")
				}
				id := "123"
				data := map[string]any{"file_size": 16, "sha1": strings.Repeat("A", 40), "pick_code": "pick", "url": map[string]string{"url": "https://cdn.test/file"}}
				switch field {
				case "file_id":
					id = "456"
				case "file_size":
					data[field] = 17
				default:
					data[field] = "different"
				}
				return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{id: data}}), nil
			})}
			_, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly},
				BalanceSourceFile{FileID: "123", PickCode: "pick", Size: 16, SHA1: strings.Repeat("A", 40)}, "2-5")
			if err == nil || !strings.Contains(err.Error(), "不匹配") || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestTransferRangeAutoFallsBackUsingIssuedCookie(t *testing.T) {
	var calls []string
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Path)
		switch req.URL.Path {
		case "/open/ufile/downurl":
			return balanceOpenAPIResponse(req, map[string]any{"state": false, "code": 403, "message": "denied", "data": []any{}}), nil
		case "/files/download":
			if !strings.Contains(req.Header.Get("Cookie"), "UID=123") || req.UserAgent() != web115TransferUA {
				t.Fatalf("incorrect cookie API headers: %v", req.Header)
			}
			resp := balanceOpenAPIResponse(req, map[string]any{"state": true, "file_url": "https://cdn.test/file", "file_size": 16})
			resp.Header.Set("Set-Cookie", "download=grant; Path=/; HttpOnly")
			return resp, nil
		case "/file":
			if req.Header.Get("Cookie") != "download=grant" || req.UserAgent() != web115TransferUA || req.Header.Get("Authorization") != "" || req.Header.Get("Origin") != "" {
				t.Fatalf("incorrect CDN headers: %v", req.Header)
			}
			return &http.Response{StatusCode: 206, ContentLength: 4, Header: http.Header{"Content-Range": {"bytes 2-5/16"}}, Body: io.NopCloser(strings.NewReader("2345")), Request: req}, nil
		default:
			if req.URL.Host != "my.115.com" {
				t.Fatalf("unexpected request: %s", req.URL)
			}
			return balanceOpenAPIResponse(req, map[string]any{"state": true}), nil
		}
	})}
	got, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "token", Cookie: "UID=123; CID=cid; SEID=seid", Match302AccessMode: model.Match302AccessModeAuto}, BalanceSourceFile{Size: 16, PickCode: "pick", IsCollect: true}, "2-5")
	if err != nil || got != "D2F75E8204FEDF2EACD261E2461B2964E3BFD5BE" || len(calls) != 4 {
		t.Fatalf("hash=%s err=%v calls=%v", got, err, calls)
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
	if _, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{}, BalanceSourceFile{Size: 10 << 20}, "0-8388608"); err == nil {
		t.Fatal("accepted a challenge larger than 8 MiB")
	}
}

type unreadRangeBody struct {
	t      *testing.T
	closed bool
}

func (b *unreadRangeBody) Read([]byte) (int, error) {
	b.t.Fatal("invalid response body must not be consumed")
	return 0, io.EOF
}

func (b *unreadRangeBody) Close() error { b.closed = true; return nil }

func TestTransferRangeRejectsUnsafeResponseWithoutReading(t *testing.T) {
	for _, name := range []string{"whole file", "compressed", "decompressed", "wrong length", "redirect"} {
		t.Run(name, func(t *testing.T) {
			body := &unreadRangeBody{t: t}
			calls := 0
			svc := NewWeb115Service(nil)
			svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 {
					t.Fatal("followed a redirect with signed download credentials")
				}
				resp := &http.Response{StatusCode: 206, ContentLength: 4, Header: http.Header{"Content-Range": {"bytes 2-5/16"}}, Body: body, Request: req}
				switch name {
				case "whole file":
					resp.StatusCode = 200
				case "compressed":
					resp.Header.Set("Content-Encoding", "gzip")
				case "decompressed":
					resp.Uncompressed = true
				case "wrong length":
					resp.ContentLength = 16
				case "redirect":
					resp.StatusCode = 302
					resp.Header.Set("Location", "https://cdn.test/other")
				}
				return resp, nil
			})}
			_, err := hashTransferHTTPRange(context.Background(), svc.transferHTTPClient(), "https://cdn.test/file", http.Header{"Cookie": {"download=grant"}}, 16, 2, 5)
			if err == nil || !body.closed || calls != 1 {
				t.Fatalf("err=%v closed=%v calls=%d", err, body.closed, calls)
			}
		})
	}
}

func TestTransferRangeHonorsCancellationBeforeNetwork(t *testing.T) {
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled challenge caused a network request")
		return nil, io.EOF
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.readTransferRangeHash(ctx, &model.CloudStorage{AccessToken: "token"}, BalanceSourceFile{Size: 16}, "2-5")
	if err != context.Canceled {
		t.Fatalf("err=%v", err)
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
		return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{"id": map[string]any{"file_size": 16, "url": map[string]string{"url": "https://cdn.test/file"}}}}), nil
	})}
	_, err := svc.readTransferRangeHash(context.Background(), &model.CloudStorage{AccessToken: "token", Cookie: "must-not-be-used", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}, BalanceSourceFile{Size: 16, PickCode: "pick"}, "2-5")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
