package service

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

type balanceOpenAPITransport func(*http.Request) (*http.Response, error)

func (f balanceOpenAPITransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func balanceOpenAPIResponse(req *http.Request, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(data))), Request: req}
}

func TestOpenAPITransferWithoutCookiesThroughRangeAndCleanup(t *testing.T) {
	db := setupBalanceServiceTestDB(t)
	source, target, match := createBalanceCacheBase(t, db, true)
	for _, storage := range []*model.CloudStorage{&source, &target} {
		storage.Cookie = ""
		storage.Match302AccessMode = model.Match302AccessModeOpenAPIOnly
		if storage.ID == target.ID {
			storage.AccessToken = "target-token"
		}
		if err := db.Save(storage).Error; err != nil {
			t.Fatal(err)
		}
	}
	member := model.Match302BalanceMember{Match302ID: match.ID, CloudStorageID: target.ID, TargetRootPath: "/cache"}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	assignment := model.Match302BalanceAssignment{Match302ID: match.ID, PlaybackStorageID: target.ID,
		SourceFilePath: "/media/source/Test.mkv", Status: model.BalanceAssignmentStatusPending}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	content := "0123456789abcdef"
	fullSHA := fmt.Sprintf("%X", sha1.Sum([]byte(content)))
	rangeSHA := fmt.Sprintf("%X", sha1.Sum([]byte("2345")))
	uploads, ranges, deletes, mkdirs := 0, 0, 0, 0
	ready := false
	client := &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cdn.test" {
			ranges++
			if req.Header.Get("Cookie") != "download=grant" || req.UserAgent() != web115BrowserUA || req.Header.Get("Range") != "bytes=2-5" {
				t.Fatalf("incorrect Range headers: %v", req.Header)
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("token leaked to CDN")
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 2-5/16"}}, Body: io.NopCloser(strings.NewReader("2345")), Request: req}, nil
		}
		if req.URL.Host != "proapi.115.com" || !strings.HasPrefix(req.URL.Path, "/open/") {
			t.Fatalf("unexpected non-OpenAPI request: %s", req.URL)
		}
		if req.Header.Get("Cookie") != "" {
			t.Fatal("unexpected account Cookie")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		wantToken := "Bearer target-token"
		if req.URL.Path == "/open/folder/get_info" || req.URL.Path == "/open/ufile/downurl" {
			wantToken = "Bearer source-token"
		}
		if req.Header.Get("Authorization") != wantToken {
			t.Fatalf("wrong account at %s", req.URL.Path)
		}
		respond := func(data any) *http.Response {
			return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": data})
		}
		switch req.URL.Path {
		case "/open/folder/get_info":
			if req.Form.Get("path") != "/library/Test.mkv" {
				t.Fatalf("source path: %s", req.Form.Get("path"))
			}
			return respond(map[string]any{"file_id": "source-id", "pick_code": "source-pick", "file_name": "Test.mkv", "sha1": fullSHA, "size": "16"}), nil
		case "/open/ufile/files":
			files := []map[string]any{}
			if req.Form.Get("cid") == "0" && mkdirs > 0 {
				files = append(files, map[string]any{"fid": "100", "fc": "0", "fn": "cache"})
			} else if req.Form.Get("cid") == "100" && ready {
				files = append(files, map[string]any{"fid": "target-id", "fc": "1", "fn": "Test.mkv", "pc": "target-pick", "sha1": fullSHA, "fs": 16, "fta": "1"})
			}
			return balanceOpenAPIResponse(req, map[string]any{"state": true, "count": len(files), "data": files}), nil
		case "/open/folder/add":
			mkdirs++
			if req.Form.Get("pid") != "0" || req.Form.Get("file_name") != "cache" {
				t.Fatal("incorrect mkdir")
			}
			return respond(map[string]string{"file_id": "100"}), nil
		case "/open/upload/init":
			uploads++
			if req.Form.Get("fileid") != fullSHA || req.Form.Get("file_size") != "16" || req.Form.Get("target") != "U_1_100" {
				t.Fatalf("incorrect upload form: %v", req.Form)
			}
			if uploads == 1 {
				return respond(map[string]any{"status": 7, "sign_key": "challenge", "sign_check": "2-5", "file_id": nil}), nil
			}
			if req.Form.Get("sign_key") != "challenge" || req.Form.Get("sign_val") != rangeSHA {
				t.Fatal("invalid challenge proof")
			}
			ready = true
			return respond(map[string]any{"status": 2, "pick_code": "target-pick", "file_id": nil}), nil
		case "/open/ufile/downurl":
			if req.UserAgent() != web115BrowserUA || req.Form.Get("pick_code") != "source-pick" {
				t.Fatal("incorrect downurl request")
			}
			resp := respond(map[string]any{"source-id": map[string]any{"pick_code": "source-pick", "file_size": 16, "url": map[string]string{"url": "https://cdn.test/file"}}})
			resp.Header.Set("Set-Cookie", "download=grant; Path=/; HttpOnly")
			return resp, nil
		case "/open/ufile/delete":
			deletes++
			if req.Form.Get("file_ids") != "target-id" {
				t.Fatal("incorrect delete target")
			}
			return respond([]string{"target-id"}), nil
		default:
			t.Fatalf("unexpected endpoint: %s", req.URL.Path)
			return nil, errors.New("unexpected endpoint")
		}
	})}
	svc := NewBalanceAssignmentService(nil)
	svc.web115Svc.httpClient = client
	if err := svc.RunTransfer(context.Background(), assignment.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Preload("PlaybackStorage").Preload("Match302").First(&assignment, assignment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if assignment.Status != model.BalanceAssignmentStatusReady || assignment.TargetFileID != "target-id" || assignment.TargetPickcode != "target-pick" || uploads != 2 || ranges != 1 {
		t.Fatalf("transfer receipt=%+v uploads=%d ranges=%d", assignment, uploads, ranges)
	}
	if err := db.Model(&assignment).Update("status", model.BalanceAssignmentStatusPending).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RunTransfer(context.Background(), assignment.ID); err != nil {
		t.Fatal(err)
	}
	if uploads != 2 || ranges != 1 || mkdirs != 1 {
		t.Fatal("existing file was not reused")
	}
	cleanup := NewBalanceCleanupService(nil)
	cleanup.web115Svc = svc.web115Svc
	if err := cleanup.cleanupTargetFile(context.Background(), &assignment); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatalf("deletes=%d", deletes)
	}
}

type fakeOpenAPIInitializer struct {
	responses []*sdk115.UploadInitResp
	requests  []sdk115.UploadInitReq
	err       error
}

func (f *fakeOpenAPIInitializer) UploadInit(_ context.Context, req *sdk115.UploadInitReq) (*sdk115.UploadInitResp, error) {
	f.requests = append(f.requests, *req)
	if f.err != nil {
		return nil, f.err
	}
	return f.responses[(len(f.requests)-1)%len(f.responses)], nil
}

func TestRapidUploadOpenAPIFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response *sdk115.UploadInitResp
		err      error
		want     string
		calls    int
	}{
		{"miss", &sdk115.UploadInitResp{Status: 1}, nil, "秒传未命中", 1},
		{"unknown", &sdk115.UploadInitResp{Status: 99}, nil, "未知状态", 1},
		{"empty", nil, nil, "响应为空", 1},
		{"missing challenge", &sdk115.UploadInitResp{Status: 7}, nil, "参数缺失", 1},
		{"repeated challenge", &sdk115.UploadInitResp{Status: 7, SignKey: "k", SignCheck: "0-3"}, nil, "重复", 2},
		{"network", nil, errors.New("connection interrupted"), "connection interrupted", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeOpenAPIInitializer{responses: []*sdk115.UploadInitResp{tt.response}, err: tt.err}
			_, err := rapidUploadOpenAPI(context.Background(), client, &sdk115.UploadInitReq{}, func(string) (string, error) { return strings.Repeat("a", 40), nil })
			if err == nil || !strings.Contains(err.Error(), tt.want) || len(client.requests) != tt.calls {
				t.Fatalf("err=%v calls=%d", err, len(client.requests))
			}
		})
	}
	client := &fakeOpenAPIInitializer{responses: []*sdk115.UploadInitResp{{Status: 2}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rapidUploadOpenAPI(ctx, client, &sdk115.UploadInitReq{}, nil); !errors.Is(err, context.Canceled) || len(client.requests) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(client.requests))
	}
}

func TestOpenAPITransferProtocolSelection(t *testing.T) {
	for _, tt := range []struct {
		mode, token, cookie string
		want                bool
	}{
		{"auto", "token", "", true}, {"auto", "token", "cookie", true}, {"auto", "", "cookie", false},
		{"openapi_only", "", "cookie", true}, {"cookie_only", "token", "cookie", false},
	} {
		if got := useOpenAPITransfer(&model.CloudStorage{Match302AccessMode: tt.mode, AccessToken: tt.token, Cookie: tt.cookie}); got != tt.want {
			t.Fatalf("%+v got=%v", tt, got)
		}
	}
}

func TestOpenAPITransferFilePaginationAndCollision(t *testing.T) {
	var offsets []string
	client := sdk115.New().SetHttpClient(&http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		offset := req.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		name := "other"
		if offset == "1" {
			name = "Test.mkv"
		}
		return balanceOpenAPIResponse(req, map[string]any{"state": true, "count": 2, "data": []map[string]any{{"fid": "id", "fc": "1", "fn": name, "sha1": "wrong", "fs": 16, "fta": "1", "pc": "pick"}}}), nil
	})})
	file, found, err := findOpenAPITransferFile(context.Background(), client, "100", "Test.mkv", "")
	if err != nil || !found || !reflect.DeepEqual(offsets, []string{"0", "1"}) {
		t.Fatalf("found=%v err=%v offsets=%v", found, err, offsets)
	}
	if _, err := checkedOpenAPITransferResult(file, BalanceSourceFile{SHA1: strings.Repeat("a", 40), Size: 16}, "/Test.mkv"); err == nil {
		t.Fatal("reused a different file")
	}
}

func TestOpenAPIHardCleanupUsesOnlyMatchingRecycleEntry(t *testing.T) {
	for _, alreadyRecycled := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyRecycled), func(t *testing.T) {
			recycled := alreadyRecycled
			var deletes []string
			svc := NewWeb115Service(nil)
			svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "proapi.115.com" || req.Header.Get("Cookie") != "" {
					t.Fatal("unexpected cookie request")
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				data := any([]string{})
				switch req.URL.Path {
				case "/open/rb/list":
					items := map[string]any{"count": "1", "0": map[string]string{"id": "other", "pick_code": "other-pick", "file_name": "Test.mkv"}}
					if recycled {
						items["count"] = "2"
						items["1"] = map[string]string{"id": "recycle-id", "pick_code": "target-pick"}
					}
					data = items
				case "/open/ufile/delete":
					deletes = append(deletes, req.Form.Get("file_ids"))
					recycled = true
				case "/open/rb/del":
					deletes = append(deletes, req.Form.Get("tid"))
				default:
					t.Fatalf("unexpected request: %s", req.URL.Path)
				}
				return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": data}), nil
			})}
			err := svc.cleanupTransferFileOpenAPI(context.Background(), &model.Match302BalanceAssignment{TargetFileID: "target-id", TargetPickcode: "target-pick",
				PlaybackStorage: &model.CloudStorage{AccessToken: "target-token"}, Match302: &model.Match302{CleanupMode: model.Match302CleanupModeHardDelete}})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"target-id", "recycle-id"}
			if alreadyRecycled {
				want = []string{"recycle-id"}
			}
			if !reflect.DeepEqual(deletes, want) {
				t.Fatalf("deletes=%v want=%v", deletes, want)
			}
		})
	}
}
