package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestTransferSourceUsesExactOpenAPIPathSize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		size     string
		sizeByte int64
		asArray  bool
		wantSize int64
	}{
		{name: "human readable size", size: "11.48 GB", sizeByte: 12327259469, wantSize: 12327259469},
		{name: "exact size takes priority", size: "12345", sizeByte: 12327259469, wantSize: 12327259469},
		{name: "array response", size: "11.48 GB", sizeByte: 12327259469, asArray: true, wantSize: 12327259469},
		{name: "legacy byte count", size: "12345", wantSize: 12345},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha1 := strings.Repeat("A", 40)
			calls := 0
			svc := NewBalanceAssignmentService(nil)
			svc.web115Svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "proapi.115.com" || req.URL.Path != "/open/folder/get_info" || req.Method != http.MethodPost {
					t.Fatalf("complete path metadata must not require additional lookups: %s", req.URL)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.Form.Get("path") != "/library/Test.mkv" {
					t.Fatalf("unexpected path: %s", req.Form.Get("path"))
				}
				data := map[string]any{"file_id": "source-id", "file_name": "Test.mkv", "pick_code": "source-pick", "sha1": sha1, "size": tc.size}
				if tc.sizeByte > 0 {
					data["size_byte"] = tc.sizeByte
				}
				var payload any = data
				if tc.asArray {
					payload = []map[string]any{data}
				}
				return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": payload}), nil
			})}
			info, err := svc.resolveSourceFileInfo(context.Background(), &model.Match302{CloudStorage: &model.CloudStorage{
				AccessToken: "source-token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly,
			}}, "/media/Test.mkv", "/library/Test.mkv", true)
			if err != nil || info.Size != tc.wantSize || info.SHA1 != sha1 || info.PickCode != "source-pick" || calls != 1 {
				t.Fatalf("info=%+v calls=%d err=%v", info, calls, err)
			}
		})
	}
}

func TestTransferSourceCompletesPartialOpenAPIPathMetadata(t *testing.T) {
	for _, missing := range []string{"sha1", "size", "pickcode"} {
		t.Run(missing, func(t *testing.T) {
			sha1 := strings.Repeat("A", 40)
			var offsets []string
			svc := NewBalanceAssignmentService(nil)
			svc.web115Svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "proapi.115.com" || req.Header.Get("Cookie") != "" {
					t.Fatalf("unexpected Cookie or download request: %s", req.URL)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				switch req.URL.Path {
				case "/open/folder/get_info":
					if req.Form.Get("path") == "/library/Movie Name" {
						return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{"file_id": "parent-id"}}), nil
					}
					if req.Form.Get("path") != "/library/Movie Name/Test.mkv" {
						t.Fatalf("unexpected path: %s", req.Form.Get("path"))
					}
					data := map[string]any{"file_id": "source-id", "file_name": "Test.mkv", "pick_code": "source-pick", "size": "12345", "sha1": sha1}
					switch missing {
					case "sha1":
						delete(data, "sha1")
					case "size":
						data["size"] = "12.06 KB"
					case "pickcode":
						delete(data, "pick_code")
					}
					return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": data}), nil
				case "/open/ufile/files":
					if req.Form.Get("cid") != "parent-id" {
						t.Fatalf("unexpected directory: %s", req.Form.Get("cid"))
					}
					offsets = append(offsets, req.Form.Get("offset"))
					id := "source-id"
					if len(offsets) == 1 {
						id = "wrong-id"
					} else if len(offsets) > 2 {
						t.Fatal("unbounded listing")
					}
					return balanceOpenAPIResponse(req, map[string]any{"state": true, "count": 2, "data": []map[string]any{{
						"fid": id, "fc": "1", "fn": "Test.mkv", "pc": "source-pick", "sha1": sha1, "fs": 12345, "fta": "1",
					}}}), nil
				default:
					t.Fatalf("metadata lookup made an unexpected request: %s", req.URL)
					return nil, errors.New("unexpected request")
				}
			})}
			match := &model.Match302{TargetPath: "/library", CloudStorage: &model.CloudStorage{
				AccessToken: "source-token", Cookie: "must-not-be-used", Match302AccessMode: model.Match302AccessModeOpenAPIOnly,
			}}
			info, err := svc.resolveSourceFileInfo(context.Background(), match, "/media/Test.mkv", "/library/Movie%20Name/Test.mkv", true)
			if err != nil || info.SHA1 != sha1 || info.Size != 12345 || info.PickCode != "source-pick" || info.FileID != "source-id" {
				t.Fatalf("info=%+v err=%v", info, err)
			}
			if !reflect.DeepEqual(offsets, []string{"0", "1"}) {
				t.Fatalf("offsets=%v", offsets)
			}
		})
	}
}

type transferMetadataResolver struct {
	fakeBalanceSourceFileResolver
	client *driver.Pan115Client
}

func (f *transferMetadataResolver) NewClient(cookie string) (*driver.Pan115Client, error) {
	f.cookieCalls++
	return f.client, nil
}

func TestTransferSourceAutoFallbackEnrichesCookieDespiteConfiguredToken(t *testing.T) {
	for _, openFailed := range []bool{true, false} {
		t.Run(fmt.Sprint(openFailed), func(t *testing.T) {
			sha1 := strings.Repeat("B", 40)
			calls := 0
			client := driver.New().SetHttpClient(&http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/files/supervision" {
					t.Fatalf("unexpected Cookie request: %s", req.URL)
				}
				return balanceOpenAPIResponse(req, map[string]any{"state": true, "data": map[string]any{
					"file_id": "source-id", "file_sha1": sha1, "file_size": "12345", "pick_code": "source-pick",
				}}), nil
			})})
			partial := Web115File{FileID: "source-id", Name: "Test.mkv", PickCode: "source-pick", IsFile: true}
			resolver := &transferMetadataResolver{client: client, fakeBalanceSourceFileResolver: fakeBalanceSourceFileResolver{
				file: partial, found: true, openFile: partial, openFound: true,
			}}
			if openFailed {
				resolver.openErr = errors.New("OpenAPI unavailable")
			}
			svc := NewBalanceAssignmentService(nil)
			svc.sourceFile115Svc = resolver
			svc.web115Svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
				return balanceOpenAPIResponse(req, map[string]any{"state": false, "code": 403, "message": "directory unavailable"}), nil
			})}
			info, err := svc.resolveSourceFileInfo(context.Background(), &model.Match302{CloudStorage: &model.CloudStorage{
				AccessToken: "configured-token", Cookie: "cookie", Match302AccessMode: model.Match302AccessModeAuto,
			}}, "/media/Test.mkv", "/library/Test.mkv", true)
			if err != nil || calls != 1 || resolver.cookieCalls != 1 || info.SHA1 != sha1 || info.Size != 12345 {
				t.Fatalf("info=%+v err=%v supervision=%d cookie=%d", info, err, calls, resolver.cookieCalls)
			}
		})
	}
}

func TestTransferSourceReportsMissingMetadataWithoutTargetWrites(t *testing.T) {
	svc := NewBalanceAssignmentService(nil)
	svc.sourceFile115Svc = &fakeBalanceSourceFileResolver{openFound: true, openFile: Web115File{
		FileID: "source-id", Name: "Test.mkv", PickCode: "source-pick", IsFile: true,
	}}
	svc.web115Svc.httpClient = &http.Client{Transport: balanceOpenAPITransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/open/ufile/files" || req.Method != http.MethodGet {
			t.Fatalf("unexpected lookup or target write: %s", req.URL)
		}
		return balanceOpenAPIResponse(req, map[string]any{"state": true, "count": 1, "data": []map[string]any{{
			"fid": "source-id", "fc": "1", "fn": "Test.mkv", "pc": "source-pick", "fs": 0,
		}}}), nil
	})}
	_, err := svc.prepareTransferSource(context.Background(), &model.Match302BalanceAssignment{
		SourceFilePath: "/Test.mkv", Match302: &model.Match302{CloudStorage: &model.CloudStorage{
			AccessToken: "token", Cookie: "must-not-be-used", Match302AccessMode: model.Match302AccessModeOpenAPIOnly,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "SHA1 缺失") || !strings.Contains(err.Error(), "文件大小无效（0 字节）") {
		t.Fatalf("err=%v", err)
	}
}
