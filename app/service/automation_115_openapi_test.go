package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"film-fusion/app/model"
)

// Every request in these tests is handled in memory. No 115 account or network
// connection is used, including the complete submit/wait/rename flow.
func newAutomation115APITest(t *testing.T, handle func(*http.Request) string) (*AutomationService, model.CloudStorage) {
	t.Helper()
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{UserID: 1, StorageType: model.StorageType115Open, StorageName: "模拟账号", AccessToken: "test-token", Status: model.StatusActive}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: automationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "proapi.115.com" || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authenticated request: %s", req.URL)
		}
		if req.Method == http.MethodPost {
			if !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Fatalf("expected documented multipart form, got %s", req.Header.Get("Content-Type"))
			}
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			defer req.MultipartForm.RemoveAll()
		} else if req.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", req.Method)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(handle(req))), Request: req}, nil
	})}
	return &AutomationService{db: db, httpClient: client}, storage
}

func TestAutomation115CloudEndpointContracts(t *testing.T) {
	cases := []struct {
		name, nodeType, method, path, response, field string
		config                                        map[string]any
		form                                          map[string]string
		want                                          any
	}{
		{"quota", AutomationNodeQuota115OpenAPI, "GET", "get_quota_info", `{"state":true,"code":0,"message":"","data":{"count":100,"surplus":80,"used":20,"package":[{"name":"会员","expire_info":[{"surplus":80,"expire_time":1800000000}]}]}}`, "surplus", nil, nil, json.Number("80")},
		{"list page", AutomationNodeList115TasksOpenAPI, "GET", "get_task_list", `{"state":true,"code":0,"message":"","data":{"page":3,"page_count":3,"count":21,"tasks":[{"file_id":"3073323042143855813","status":2,"extra":"kept"}]}}`, "page", map[string]any{"page": 3}, nil, json.Number("3")},
		{"delete retains files", AutomationNodeDelete115TaskOpenAPI, "POST", "del_task", `{"state":true,"code":0,"message":"","data":[]}`, "deleted", map[string]any{"info_hash": "$vars.hash"}, map[string]string{"info_hash": "abc", "del_source_file": "0"}, true},
		{"delete files explicitly", AutomationNodeDelete115TaskOpenAPI, "POST", "del_task", `{"state":true,"code":0,"message":"","data":[]}`, "delete_files", map[string]any{"info_hash": "abc", "delete_files": true}, map[string]string{"info_hash": "abc", "del_source_file": "1"}, true},
	}
	for flag := 0; flag <= 5; flag++ {
		cases = append(cases, struct {
			name, nodeType, method, path, response, field string
			config                                        map[string]any
			form                                          map[string]string
			want                                          any
		}{"clear " + strconv.Itoa(flag), AutomationNodeClear115TasksOpenAPI, "POST", "clear_task", `{"state":true,"code":0,"message":"","data":[]}`, "flag", map[string]any{"flag": flag}, map[string]string{"flag": strconv.Itoa(flag)}, flag})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s, storage := newAutomation115APITest(t, func(req *http.Request) string {
				calls++
				if req.Method != tc.method || req.URL.Path != "/open/offline/"+tc.path {
					t.Fatalf("request = %s %s", req.Method, req.URL)
				}
				for key, value := range tc.form {
					if req.FormValue(key) != value {
						t.Fatalf("%s = %q, want %q", key, req.FormValue(key), value)
					}
				}
				if tc.path == "get_task_list" && req.URL.Query().Get("page") != "3" {
					t.Fatal("page must be sent as a GET query")
				}
				return tc.response
			})
			config := map[string]any{"cloud_storage_id": storage.ID}
			for key, value := range tc.config {
				config[key] = value
			}
			output, err := s.executeAutomation115OpenAPI(context.Background(), AutomationNode{Type: tc.nodeType, Config: config}, map[string]any{"vars": map[string]any{"hash": "abc"}})
			if err != nil || calls != 1 || !reflect.DeepEqual(output[tc.field], tc.want) {
				t.Fatalf("output=%#v, calls=%d, err=%v", output, calls, err)
			}
			if output["state"] != true || output["code"] != json.Number("0") || output["response"] == nil || output["data"] == nil {
				t.Fatalf("response missing: %#v", output)
			}
			encoded, _ := json.Marshal(output)
			if strings.Contains(string(encoded), storage.AccessToken) {
				t.Fatal("output contains account token")
			}
		})
	}
}

func TestAutomation115BTParseSubmitWaitRename(t *testing.T) {
	paths := []string{}
	listCalls := 0
	s, storage := newAutomation115APITest(t, func(req *http.Request) string {
		paths = append(paths, req.URL.Path)
		switch req.URL.Path {
		case "/open/offline/torrent":
			if req.FormValue("torrent_sha1") != "seed-sha1" || req.FormValue("pick_code") != "pick123" {
				t.Fatal("seed inputs were not resolved")
			}
			return `{"state":true,"code":0,"message":"","data":{"file_size":1024,"torrent_name":"Show","file_count":2,"info_hash":"ABC","torrent_filelist":[{"size":1000,"path":"Show.mkv","wanted":1},{"size":24,"path":"readme.txt","wanted":0}]}}`
		case "/open/offline/add_task_bt":
			want := map[string]string{"info_hash": "ABC", "torrent_sha1": "seed-sha1", "pick_code": "pick123", "wanted": "0", "save_path": "Show", "wp_path_id": "9527"}
			for key, value := range want {
				if req.FormValue(key) != value {
					t.Fatalf("%s=%q", key, req.FormValue(key))
				}
			}
			return `{"state":true,"code":0,"message":"","data":[]}`
		case "/open/offline/get_task_list":
			listCalls++
			if listCalls == 1 {
				return `{"state":true,"code":0,"message":"","data":{"page":1,"page_count":1,"count":1,"tasks":[{"info_hash":"abc","status":1,"percentDone":50,"file_id":"","name":"Show","wp_path_id":"9527"}]}}`
			}
			return `{"state":true,"code":0,"message":"","data":{"page":1,"page_count":1,"count":1,"tasks":[{"info_hash":"abc","status":2,"percentDone":100,"file_id":"3073323042143855813","name":"Show","wp_path_id":"9527","add_time":1700000000,"delete_file_id":"3073323042143855813"}]}}`
		case "/open/ufile/update":
			if req.FormValue("file_id") != "3073323042143855813" || req.FormValue("file_name") != "Example.S01" {
				t.Fatalf("wrong rename target: %#v", req.Form)
			}
			return `{"state":true,"code":0,"message":"","data":{"file_name":"Example.S01","star":1}}`
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return ""
		}
	})
	ctx := context.Background()
	outputs := map[string]any{}
	runContext := map[string]any{"nodes": outputs, "vars": map[string]any{"seed": "seed-sha1", "folder": "9527"}, "item": map[string]any{"title": "Example.S01"}}
	parsed, err := s.executeAutomation115OpenAPI(ctx, AutomationNode{Type: AutomationNodeTorrent115OpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID, "torrent_sha1": "$vars.seed", "pick_code": "pick123"}}, runContext)
	if err != nil {
		t.Fatal(err)
	}
	outputs["parse"] = map[string]any{"output": parsed}
	config := map[string]any{"cloud_storage_id": storage.ID, "directory_id": "$vars.folder", "save_path": "$nodes.parse.output.torrent_name"}
	for _, key := range []string{"info_hash", "wanted", "torrent_sha1", "pick_code"} {
		config[key] = "$nodes.parse.output." + key
	}
	submitted, err := s.executeAutomation115OpenAPI(ctx, AutomationNode{Type: AutomationNodeOffline115BTOpenAPI, Config: config}, runContext)
	if err != nil || submitted["submitted"] != true || submitted["info_hash"] != "ABC" {
		t.Fatalf("submitted=%#v err=%v", submitted, err)
	}
	outputs["add"] = map[string]any{"output": submitted}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "add", Target: "wait", SourcePort: "success"}}}
	wait := AutomationNode{ID: "wait", Type: AutomationNodeWait115}
	pending, err := s.executeAutomationWait115(ctx, model.AutomationNodeRun{}, wait, definition, runContext)
	var deferred *automationNodeDeferred
	if !errors.As(err, &deferred) || pending["completed"] != false || pending["file_id"] != nil {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	encoded, _ := json.Marshal(pending)
	completed, err := s.executeAutomationWait115(ctx, model.AutomationNodeRun{OutputJSON: string(encoded)}, wait, definition, runContext)
	if err != nil || completed["completed"] != true || completed["file_id"] != "3073323042143855813" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	tasks := completed["tasks"].([]automation115Task)
	if tasks[0].Response["delete_file_id"] != "3073323042143855813" {
		t.Fatal("wait lost provider task fields")
	}
	outputs["wait"] = map[string]any{"output": completed}
	renamed, err := s.executeAutomationRename115OpenAPI(ctx, AutomationNode{Type: AutomationNodeRename115OpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID, "file_id": "$nodes.wait.output.file_id", "new_name": "{{item.title}}"}}, runContext)
	if err != nil || renamed["file_name"] != "Example.S01" || len(paths) != 5 {
		t.Fatalf("renamed=%#v err=%v paths=%v", renamed, err, paths)
	}
}

func TestAutomation115ListMatchesHashAcrossPagesWithoutGuessingFileID(t *testing.T) {
	for _, state := range []string{"absent", "pending", "failed", "missing-id", "complete"} {
		t.Run(state, func(t *testing.T) {
			pages := []string{}
			s, storage := newAutomation115APITest(t, func(req *http.Request) string {
				page := req.URL.Query().Get("page")
				pages = append(pages, page)
				tasks := []map[string]any{{"info_hash": "other", "status": 2, "file_id": "wrong-id"}}
				if page == "2" && state != "absent" {
					status, fileID := 2, "3073323042143855813"
					if state == "pending" {
						status = 1
					}
					if state == "failed" {
						status = -1
					}
					if state == "missing-id" {
						fileID = ""
					}
					tasks = []map[string]any{{"info_hash": "aBc", "status": status, "file_id": fileID, "name": "Show", "percentDone": 50, "wp_path_id": "0", "custom_field": "preserved"}}
				}
				encoded, _ := json.Marshal(map[string]any{"state": true, "code": 0, "message": "", "data": map[string]any{"page": page, "page_count": 2, "count": 2, "tasks": tasks}})
				return string(encoded)
			})
			output, err := s.executeAutomation115OpenAPI(context.Background(), AutomationNode{Type: AutomationNodeList115TasksOpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID, "page": 9, "info_hash": "ABC"}}, nil)
			if err != nil || !reflect.DeepEqual(pages, []string{"1", "2"}) {
				t.Fatalf("output=%#v pages=%v err=%v", output, pages, err)
			}
			if output["completed"] != (state == "complete") || output["matched"] != (state != "absent") {
				t.Fatalf("state flags: %#v", output)
			}
			if state == "complete" {
				if output["file_id"] != "3073323042143855813" {
					t.Fatal(output)
				}
			} else if output["file_id"] != "" {
				t.Fatalf("exposed premature or unrelated file ID: %#v", output)
			}
			if state != "absent" && output["task"].(map[string]any)["custom_field"] != "preserved" {
				t.Fatal("lost raw task fields")
			}
		})
	}
}

func TestAutomation115LinkPartialResultsDoNotRetry(t *testing.T) {
	calls := 0
	s, storage := newAutomation115APITest(t, func(req *http.Request) string {
		calls++
		if req.FormValue("urls") != "ftp://example.com/a\ned2k://|file|b|1|abc|/" {
			t.Fatalf("urls=%s", req.FormValue("urls"))
		}
		return `{"state":true,"code":0,"message":"","data":[{"state":true,"code":0,"message":"","info_hash":"ABC","url":"ftp://example.com/a"},{"state":false,"code":990002,"message":"bad link","url":"ed2k://|file|b|1|abc|/"}]}`
	})
	output, err := s.executeAutomationOffline115OpenAPI(context.Background(), AutomationNode{Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID, "url": "ftp://example.com/a\ned2k://|file|b|1|abc|/"}}, nil)
	if err == nil || output["partial"] != true || output["submitted"] != false || output["success_count"] != 1 || output["failure_count"] != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	nodeRun := model.AutomationNodeRun{RunID: 1, NodeID: "add", NodeType: AutomationNodeOffline115OpenAPI, Status: model.AutomationNodeRunning, Attempt: 1, MaxAttempts: 3}
	if err := s.db.Create(&nodeRun).Error; err != nil {
		t.Fatal(err)
	}
	s.completeAutomationNode(nodeRun, output, err)
	var stored model.AutomationNodeRun
	if err := s.db.First(&stored, nodeRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.AutomationNodeFailed || calls != 1 || !strings.Contains(stored.OutputJSON, "bad link") {
		t.Fatalf("retry/lost error: %#v", stored)
	}
}

func TestAutomation115ProviderErrorsRetainResponse(t *testing.T) {
	s, storage := newAutomation115APITest(t, func(*http.Request) string {
		return `{"state":false,"code":1000012,"message":"quota exhausted","data":[]}`
	})
	output, err := s.executeAutomation115OpenAPI(context.Background(), AutomationNode{Type: AutomationNodeQuota115OpenAPI, Config: map[string]any{"cloud_storage_id": storage.ID}}, nil)
	if err == nil || output["code"] != json.Number("1000012") || output["message"] != "quota exhausted" || output["response"] == nil {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func TestAutomation115InvalidConfigurationDoesNotRequest(t *testing.T) {
	s, storage := newAutomation115APITest(t, func(*http.Request) string { t.Fatal("invalid config must not call provider"); return "" })
	for _, tc := range []struct {
		typ    string
		config map[string]any
	}{
		{AutomationNodeList115TasksOpenAPI, map[string]any{"page": 0}},
		{AutomationNodeList115TasksOpenAPI, map[string]any{"page": 1.5}},
		{AutomationNodeList115TasksOpenAPI, map[string]any{"info_hash": "$vars.missing"}},
		{AutomationNodeClear115TasksOpenAPI, map[string]any{"flag": 6}},
		{AutomationNodeClear115TasksOpenAPI, map[string]any{}},
		{AutomationNodeOffline115BTOpenAPI, map[string]any{"info_hash": "abc", "wanted": "", "save_path": "Show", "torrent_sha1": "seed", "pick_code": "pick"}},
		{AutomationNodeOffline115BTOpenAPI, map[string]any{"info_hash": "abc", "wanted": "0,-1", "save_path": "Show", "torrent_sha1": "seed", "pick_code": "pick"}},
	} {
		tc.config["cloud_storage_id"] = storage.ID
		if _, err := s.executeAutomation115OpenAPI(context.Background(), AutomationNode{Type: tc.typ, Config: tc.config}, nil); err == nil {
			t.Fatalf("accepted invalid %#v", tc)
		}
	}
	for _, urls := range []string{"", "bc://bt/abc", strings.Repeat("https://example.com/a\n", 116)} {
		if _, err := s.executeAutomationOffline115OpenAPI(context.Background(), AutomationNode{Config: map[string]any{"cloud_storage_id": storage.ID, "url": urls}}, nil); err == nil {
			t.Fatalf("accepted invalid links %q", urls)
		}
	}
}

func TestAutomation115DownloadGraphValidation(t *testing.T) {
	for _, kind := range []string{AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI} {
		definition := AutomationDefinition{SchemaVersion: 1, Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "download", Type: kind, Config: map[string]any{"cloud_storage_id": 1, "info_hash": "abc", "wanted": "0", "save_path": "Show", "torrent_sha1": "seed", "pick_code": "pick", "page": 1}},
			{ID: "wait", Type: AutomationNodeWait115},
			{ID: "rename", Type: AutomationNodeRename115OpenAPI, Config: map[string]any{"cloud_storage_id": 1, "file_id": "$nodes.wait.output.file_id", "new_name": "Show"}},
			{ID: "end", Type: AutomationNodeEnd},
		}, Edges: []AutomationEdge{
			{ID: "a", Source: "trigger", Target: "download", SourcePort: "next"},
			{ID: "b", Source: "download", Target: "wait", SourcePort: "success"},
			{ID: "c", Source: "wait", Target: "rename", SourcePort: "success"},
			{ID: "d", Source: "rename", Target: "end", SourcePort: "success"},
		}}
		if result := ValidateAutomationDefinition(definition); !result.Valid {
			t.Fatalf("%s chain invalid: %#v", kind, result)
		}
		if kind == AutomationNodeList115TasksOpenAPI {
			delete(definition.Nodes[1].Config, "info_hash")
			if result := ValidateAutomationDefinition(definition); result.Valid {
				t.Fatal("unfiltered list must not be used as a wait target")
			}
		}
	}
}
