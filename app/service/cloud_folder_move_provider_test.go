package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type folderMoveTransport func(*http.Request) (*http.Response, error)

func (f folderMoveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func folderProviderResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

// Both auth modes exercise the actual service and HTTP adapter entirely through
// an injected transport. No request can reach a 115 server, including auth.
func TestCloudFolderProviderCookieAndOpenAPIMove(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		t.Run(method, func(t *testing.T) {
			s, f, request := folderMoveFixture(t)
			request.AccessMethod = method
			var requests, posts int
			transport := folderMoveTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Query().Get("access_token") != "" || r.URL.Query().Get("cookie") != "" {
					t.Fatal("credential in URL")
				}
				if method == "cookie" {
					if r.Header.Get("Cookie") == "" || r.URL.Host != "webapi.115.com" {
						t.Fatalf("cookie request=%s", r.URL)
					}
				} else if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Cookie") != "" {
					t.Fatal("wrong credential mode")
				}
				if r.Method == http.MethodGet {
					if r.URL.Path != "/files" && r.URL.Path != "/open/ufile/files" {
						t.Fatalf("unexpected request (auth/refresh?): %s", r.URL.Path)
					}
					q := r.URL.Query()
					offset, _ := strconv.Atoi(q.Get("offset"))
					limit, _ := strconv.Atoi(q.Get("limit"))
					cid := q.Get("cid")
					if q.Get("show_dir") != "1" || q.Get("cur") != "1" {
						t.Fatal("listing not constrained to selected directory")
					}
					page, err := f.List(r.Context(), cid, offset, limit)
					if err != nil {
						return folderProviderResponse(r, 200, `{"state":false}`), nil
					}
					rows := make([]map[string]any, 0, len(page.Items))
					for _, item := range page.Items {
						if method == "cookie" {
							// Unrelated nullable metadata must not reach driver.File.From.
							row := map[string]any{"n": item.Name, "s": item.Size, "fl": []any{nil}}
							if item.IsFile {
								row["fid"] = item.FileID
								row["cid"] = item.ParentID
							} else {
								row["cid"] = item.FileID
								row["pid"] = item.ParentID
							}
							rows = append(rows, row)
						} else {
							fc := "0"
							if item.IsFile {
								fc = "1"
							}
							rows = append(rows, map[string]any{"fid": item.FileID, "pid": item.ParentID, "fn": item.Name, "fc": fc, "fs": item.Size, "aid": "1", "fta": "1"})
						}
					}
					body, _ := json.Marshal(map[string]any{"state": true, "cid": cid, "count": page.Total, "offset": offset, "data": rows})
					return folderProviderResponse(r, 200, string(body)), nil
				}
				posts++
				if r.Method != http.MethodPost {
					t.Fatal("unexpected mutation method")
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				switch r.URL.Path {
				case "/files/add", "/open/folder/add":
					name := r.Form.Get("cname")
					if method == "openapi" {
						name = r.Form.Get("file_name")
					}
					if r.Form.Get("pid") != "20" {
						t.Fatal("folder created in source instead of chosen target")
					}
					id, _ := f.Mkdir(r.Context(), r.Form.Get("pid"), name)
					if method == "cookie" {
						return folderProviderResponse(r, 200, fmt.Sprintf(`{"state":true,"cid":%q}`, id)), nil
					}
					return folderProviderResponse(r, 200, fmt.Sprintf(`{"state":true,"data":{"file_id":%q,"file_name":"Movie"}}`, id)), nil
				case "/files/move", "/open/ufile/move":
					fileID, folderID := r.Form.Get("fid[0]"), r.Form.Get("pid")
					if method == "openapi" {
						fileID, folderID = r.Form.Get("file_ids"), r.Form.Get("to_cid")
					}
					if fileID != "100" || folderID != "30" {
						t.Fatal("wrong file/target mutation")
					}
					_ = f.Move(r.Context(), fileID, folderID)
					return folderProviderResponse(r, 200, `{"state":true,"data":true}`), nil
				default:
					t.Fatalf("unexpected mutation (refresh/fallback?): %s", r.URL.Path)
					return nil, nil
				}
			})
			s.web = &Web115Service{httpClient: &http.Client{Transport: transport}}
			s.newProvider = s.makeProvider
			result, err := s.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "moved" || posts != 2 || requests < 5 {
				t.Fatalf("result=%+v err=%v requests=%d posts=%d", result, err, requests, posts)
			}
		})
	}
}
func TestCloudFolderProviderRejectsMalformedListings(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"state":true}`, `{"state":true,"cid":"0","offset":0,"count":0,"data":[]}`,
		`{"state":true,"cid":"20","offset":0,"count":0,"data":null}`,
		`{"state":true,"cid":"20","offset":0,"count":0,"data":[{"fid":"100","pid":"20","fc":"1","fn":"Movie"}]}`,
		`{"state":true,"cid":"20","offset":0,"count":1,"data":[{"fid":"100","pid":"10","fc":"1","fn":"Movie"}]}`,
		`{"state":true,"cid":"20","offset":0,"count":1,"data":[null]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, _, _ := folderMoveFixture(t)
			s.web = &Web115Service{httpClient: &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) { return folderProviderResponse(r, 200, body), nil })}}
			s.newProvider = s.makeProvider
			if _, err := s.Browse(context.Background(), 7, 1, "openapi", "20", 0, 100); err == nil {
				t.Fatal("malformed list accepted")
			}
		})
	}
}
func TestCloudFolderProviderNeverRefreshesRetriesOrRedirectsWrites(t *testing.T) {
	for _, status := range []int{200, 302, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				resp := folderProviderResponse(r, status, `{"state":false,"code":40101032,"message":"secret-token"}`)
				resp.Header.Set("Location", "https://untrusted.example/")
				return resp, nil
			}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			p := &cloud115FolderProvider{client: client, method: "openapi", credential: "secret"}
			if err := p.Move(context.Background(), "100", "30"); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe provider error: %v", err)
			}
			if calls != 1 {
				t.Fatalf("write retried/refreshed/redirected: %d requests", calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &cloud115FolderProvider{client: &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}, method: "cookie"}
	if _, err := p.request(ctx, http.MethodGet, "https://example.invalid", url.Values{}); err == nil {
		t.Fatal("cancelled request accepted")
	}
}

func TestCloudFolderProviderResolvesTrustedDirectoryAncestry(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			provider := &cloud115FolderProvider{method: method, credential: "secret", client: &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				wantPath := "/files"
				if method == "openapi" {
					wantPath = "/open/ufile/files"
				}
				if r.Method != http.MethodGet || r.URL.Path != wantPath || r.URL.Query().Get("cid") != "20" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("record_open_time") != "0" {
					t.Fatalf("unexpected ancestry request: %s %s", r.Method, r.URL)
				}
				if method == "cookie" && r.Header.Get("Cookie") != "secret" {
					t.Fatal("missing selected Cookie")
				}
				if method == "openapi" && r.Header.Get("Authorization") != "Bearer secret" {
					t.Fatal("missing selected OpenAPI token")
				}
				return folderProviderResponse(r, 200, `{"state":true,"cid":20,"path":[{"cid":0,"pid":0,"name":"/"},{"cid":"10","pid":"0","name":"影视 📺"},{"cid":"20","pid":"10","name":"星际穿越 (2014)"}]}`), nil
			})}}
			got, err := provider.ResolveDirectoryPath(context.Background(), "20")
			if err != nil || got != "/影视 📺/星际穿越 (2014)" || calls != 1 {
				t.Fatalf("path=%q err=%v calls=%d", got, err, calls)
			}
			got, err = provider.ResolveDirectoryPath(context.Background(), "0")
			if err != nil || got != "/" || calls != 1 {
				t.Fatalf("root should not request provider: %q %v calls=%d", got, err, calls)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = provider.ResolveDirectoryPath(ctx, "20"); err == nil || calls != 1 {
				t.Fatal("cancelled ancestry resolution sent request")
			}
		})
	}
}

func TestCloudFolderProviderRejectsUntrustedDirectoryAncestry(t *testing.T) {
	type node struct {
		CID  string `json:"cid"`
		PID  string `json:"pid"`
		Name string `json:"name"`
	}
	valid := []node{{"0", "0", "根目录"}, {"10", "0", "影视"}, {"20", "10", "Movie"}}
	chains := map[string][]node{
		"missing root":        valid[1:],
		"truncated chain":     valid[:2],
		"disconnected parent": {{"0", "0", "根目录"}, {"10", "0", "影视"}, {"20", "99", "Movie"}},
		"repeated id":         {{"0", "0", "根目录"}, {"10", "0", "影视"}, {"10", "10", "Movie"}, {"20", "10", "last"}},
		"wrong root parent":   {{"0", "99", "根目录"}, {"20", "0", "Movie"}},
		"negative id":         {{"0", "0", "根目录"}, {"-10", "0", "影视"}, {"20", "-10", "Movie"}},
		"empty chain":         {},
	}
	for _, name := range []string{".", "..", "../escape", "one/two", `one\two`, "", "line\nfeed", "nul\x00byte", strings.Repeat("字", 86)} {
		copyChain := append([]node{}, valid...)
		copyChain[2].Name = name
		chains["invalid name "+name] = copyChain
	}
	deep := []node{{"0", "0", "root"}}
	for i := 1; i <= 65; i++ {
		deep = append(deep, node{strconv.Itoa(i), strconv.Itoa(i - 1), "x"})
	}
	chains["too deep"] = deep
	long := []node{{"0", "0", "root"}}
	for i := 1; i <= 19; i++ {
		long = append(long, node{strconv.Itoa(i), strconv.Itoa(i - 1), strings.Repeat("a", 250)})
	}
	long = append(long, node{"20", "19", "last"})
	chains["too long"] = long
	for name, chain := range chains {
		t.Run(name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"state": true, "cid": "20", "path": chain})
			provider := &cloud115FolderProvider{method: "openapi", client: &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) {
				return folderProviderResponse(r, 200, string(payload)), nil
			})}}
			if got, err := provider.ResolveDirectoryPath(context.Background(), "20"); err == nil {
				t.Fatalf("unsafe path accepted: %q", got)
			}
		})
	}
	for _, payload := range []string{
		`{"state":true,"cid":"20"}`, `{"state":true,"cid":"20","path":[null,null]}`,
		`{"state":true,"cid":"30","path":[{"cid":0,"pid":0},{"cid":20,"pid":0,"name":"Movie"}]}`,
		`{"state":true,"cid":"20","path":[{"cid":0,"pid":0},{"cid":20,"name":"Movie"}]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			provider := &cloud115FolderProvider{method: "cookie", client: &http.Client{Transport: folderMoveTransport(func(r *http.Request) (*http.Response, error) { return folderProviderResponse(r, 200, payload), nil })}}
			if got, err := provider.ResolveDirectoryPath(context.Background(), "20"); err == nil {
				t.Fatalf("malformed ancestry accepted: %q", got)
			}
		})
	}
}
