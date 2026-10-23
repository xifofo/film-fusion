package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestTransferDownURLOverrides115BrowserUserAgent(t *testing.T) {
	for _, method := range []string{"openapi", "web", "android"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			var issuedUA string
			svc := NewWeb115Service(nil)
			svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				issuedUA = req.UserAgent()
				if strings.Contains(issuedUA, "115Browser") || !strings.HasPrefix(issuedUA, "Mozilla/5.0 ") {
					t.Fatalf("download used a proprietary or empty UA: %q", issuedUA)
				}
				switch method {
				case "openapi":
					return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{"id": map[string]any{
						"file_size": 16, "url": map[string]string{"url": "https://cdn.test/file"},
					}}}), nil
				case "web":
					return balanceOpenAPIResponse(req, map[string]any{"state": true, "file_url": "https://cdn.test/file", "file_size": 16}), nil
				default:
					// 无需伪造加密回执：这里验证 SDK 合并客户端与请求 UA 后的实际请求。
					return balanceOpenAPIResponse(req, map[string]any{"state": false, "errno": 403, "error": "fixture denied"}), nil
				}
			})}
			client := driver.New().SetHttpClient(svc.httpClient).SetUserAgent(web115BrowserUA)
			var info *driver.DownloadInfo
			var err error
			switch method {
			case "openapi":
				info, err = svc.DownloadWithOpenAPIForRange(context.Background(), "token", "pick", "")
			case "web":
				info, err = svc.DownloadWithWebAPIForRange(client, "pick")
			case "android":
				info, err = svc.DownloadWithAndroidAPIForRange(client, "pick")
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if method == "android" {
				if err == nil || !strings.Contains(err.Error(), "fixture denied") {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || info == nil || info.Header.Get("User-Agent") != issuedUA {
				t.Fatalf("info=%+v err=%v issuanceUA=%q", info, err, issuedUA)
			}
			if client.Client.Header.Get("User-Agent") != web115BrowserUA {
				t.Fatal("download changed the login client's UA")
			}
		})
	}
}

func TestOpenAPIRangeDownURLParsesJSONWithoutContentType(t *testing.T) {
	for _, size := range []string{`16`, `"16"`} {
		t.Run(size, func(t *testing.T) {
			svc := NewWeb115Service(nil)
			svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}, "Set-Cookie": {"download=grant; Path=/; HttpOnly"}},
					Body: io.NopCloser(strings.NewReader(`{"state":true,"data":{"123":{"file_size":` + size + `,"url":{"url":"https://cdn.test/file"}}}}`)), Request: req}, nil
			})}
			info, err := svc.DownloadWithOpenAPIForRange(context.Background(), "token", "pick", "test-agent")
			if err != nil {
				t.Fatal(err)
			}
			if info.FileSize != 16 || info.Header.Get("Cookie") != "download=grant" || info.Header.Get("User-Agent") != "test-agent" {
				t.Fatalf("incorrect download info: %+v", info)
			}
		})
	}
}

func TestOpenAPIRangeDownURLReportsHTTPStatus(t *testing.T) {
	svc := NewWeb115Service(nil)
	svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("Forbidden")), Request: req}, nil
	})}
	_, err := svc.DownloadWithOpenAPIForRange(context.Background(), "token", "pick", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("expected actual HTTP status, got %v", err)
	}
}

func TestResolveMatch302DownloadInfoCookieOnlySkipsOpenAPI(t *testing.T) {
	var calls []string
	info, err := resolveMatch302DownloadInfo(
		model.Match302AccessModeCookieOnly,
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodOpenAPI)
			return downloadInfoForTest("open-url"), nil
		},
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodCookie)
			return downloadInfoForTest("cookie-url"), nil
		},
	)
	if err != nil {
		t.Fatalf("resolveMatch302DownloadInfo returned error: %v", err)
	}
	if info.Url.Url != "cookie-url" || !reflect.DeepEqual(calls, []string{model.Match302AccessMethodCookie}) {
		t.Fatalf("url=%q calls=%v", info.Url.Url, calls)
	}
}

func TestResolveMatch302DownloadInfoAutoFallsBackToCookie(t *testing.T) {
	var calls []string
	info, err := resolveMatch302DownloadInfo(
		model.Match302AccessModeAuto,
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodOpenAPI)
			return nil, errors.New("OpenAPI unavailable")
		},
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodCookie)
			return downloadInfoForTest("cookie-url"), nil
		},
	)
	if err != nil {
		t.Fatalf("resolveMatch302DownloadInfo returned error: %v", err)
	}
	if info.Url.Url != "cookie-url" || !reflect.DeepEqual(calls, []string{
		model.Match302AccessMethodOpenAPI,
		model.Match302AccessMethodCookie,
	}) {
		t.Fatalf("url=%q calls=%v", info.Url.Url, calls)
	}
}

func downloadInfoForTest(rawURL string) *driver.DownloadInfo {
	return &driver.DownloadInfo{Url: driver.FileDownloadUrl{Url: rawURL}}
}
