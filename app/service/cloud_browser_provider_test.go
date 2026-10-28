package service

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Real OpenAPI responses are compressed. Resty v3 exposes the decoded stream
// through Response.Body, while RawResponse.Body still contains compressed bytes.
func TestCloudBrowserOpenAPICompressedResponse(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/invalid=%t", encoding, invalid), func(t *testing.T) {
				var encoded bytes.Buffer
				var writer io.WriteCloser
				if encoding == "gzip" {
					writer = gzip.NewWriter(&encoded)
				} else {
					writer, _ = flate.NewWriter(&encoded, flate.DefaultCompression)
				}
				_, _ = io.WriteString(writer, `{"state":true,"cid":10,"offset":0,"count":1,"path":[{"cid":0,"pid":0,"name":"root"},{"cid":"10","pid":"0","name":"Movies"}],"data":[{"fid":"100","pid":"10","fn":"Movie.mkv","fc":"1","fs":1234,"fta":"1"}]}`)
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				calls := 0
				limiter := newCloudBrowserLimiter(0)
				base := cloudBrowserRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if !strings.Contains(req.Header.Get("Accept-Encoding"), encoding) {
						t.Fatalf("SDK did not request %s", encoding)
					}
					resp := cloudBrowserProviderResponse(req, encoded.String())
					resp.Header.Set("Content-Encoding", encoding)
					if invalid {
						resp.Body = io.NopCloser(strings.NewReader("invalid compressed body"))
					}
					return resp, nil
				})
				provider := &cloudBrowser115Provider{method: "openapi", credential: "offline-token", client: &http.Client{Transport: cloudBrowserTransport{base: base, limiter: limiter}}}
				page, err := provider.List(context.Background(), "10", 0, 1150)
				if calls != 1 || len(limiter.slots) != 0 {
					t.Fatalf("calls=%d unreleased slots=%d", calls, len(limiter.slots))
				}
				if invalid {
					if err == nil {
						t.Fatal("invalid compressed response accepted")
					}
					return
				}
				if err != nil || len(page.Items) != 1 || page.Items[0].Name != "Movie.mkv" || page.HasMore {
					t.Fatalf("page=%+v err=%v", page, err)
				}
			})
		}
	}
}

func TestCloudBrowserProviderFullPage(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		t.Run(method, func(t *testing.T) {
			rows := make([]map[string]any, 1150)
			for i := range rows {
				row := map[string]any{"fid": fmt.Sprint(1000 + i), "cid": "10", "n": "Movie.mkv", "s": 1234}
				if method == "openapi" {
					row = map[string]any{"fid": fmt.Sprint(1000 + i), "pid": "10", "fn": "Movie.mkv", "fc": "1", "fs": 1234, "fta": "1"}
				}
				// A large page can exceed the old 2 MiB ceiling with extra metadata.
				row["fdesc"] = strings.Repeat("x", 2048)
				rows[i] = row
			}
			data, err := json.Marshal(map[string]any{"state": true, "cid": "10", "count": 2301, "offset": 1150, "data": rows, "path": []map[string]any{{"cid": "0", "pid": "0"}, {"cid": "10", "pid": "0", "name": "Movies"}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			limiter := newCloudBrowserLimiter(0)
			base := cloudBrowserRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Query().Get("limit") != "1150" || req.URL.Query().Get("offset") != "1150" {
					t.Fatal("unexpected pagination parameters")
				}
				return cloudBrowserProviderResponse(req, string(data)), nil
			})
			provider := &cloudBrowser115Provider{method: method, credential: "offline", client: &http.Client{Transport: cloudBrowserTransport{base: base, limiter: limiter}}}
			page, err := provider.List(context.Background(), "10", 1150, 1150)
			if err != nil || len(page.Items) != 1150 || !page.HasMore || page.Offset != 1150 || calls != 1 || len(limiter.slots) != 0 {
				t.Fatalf("full page err=%v calls=%d unreleased slots=%d", err, calls, len(limiter.slots))
			}
		})
	}
}
