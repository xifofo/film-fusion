// 服务器目录浏览覆盖分支分页、路径错误与会话边界，所有文件样本只在测试临时目录创建。
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/auth"
	"github.com/gin-gonic/gin"
)

func localDirectoryRequest(t *testing.T, query string, authenticated, token bool) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if authenticated {
			c.Set("user_id", uint(1))
		}
		if token {
			c.Set("api_token_id", uint(2))
		}
	})
	router.GET("/api/local-directories", NewLocalDirectoryHandler().List)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/local-directories"+query, nil))
	return response
}

func TestLocalDirectoriesOnlyReturnsDirectFoldersWithStablePages(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"b", "a", "c", "a/nested"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "video.mkv"), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		page  string
		names []string
		more  bool
	}{{"1", []string{"a", "b"}, true}, {"2", []string{"c"}, false}, {"3", []string{}, false}} {
		response := localDirectoryRequest(t, "?path="+url.QueryEscape(root)+"&page_size=2&page="+test.page, true, false)
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Items   []localDirectoryOption `json:"items"`
				Total   int                    `json:"total"`
				HasMore bool                   `json:"has_more"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || envelope.Code != 0 || envelope.Data.Total != 3 || len(envelope.Data.Items) != len(test.names) || envelope.Data.HasMore != test.more {
			t.Fatalf("page %s: %d %s", test.page, response.Code, response.Body)
		}
		for index, name := range test.names {
			if item := envelope.Data.Items[index]; item.Name != name || item.Path != filepath.Join(root, name) {
				t.Fatalf("unexpected directory: %+v", item)
			}
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("filesystem results may be cached")
		}
	}

	// 浏览没有改动样本文件，也没有把下一级目录当作直属目录返回。
	data, err := os.ReadFile(filepath.Join(root, "video.mkv"))
	if err != nil || string(data) != "sample" {
		t.Fatal("read-only browsing changed the sample file")
	}
}

func TestLocalDirectoriesHandlesLinksAndMissingTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "target"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	response := localDirectoryRequest(t, "?path="+url.QueryEscape(root), true, false)
	var envelope struct {
		Data struct {
			Items []localDirectoryOption `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(envelope.Data.Items) != 2 || envelope.Data.Items[0].Path != filepath.Join(root, "linked") {
		t.Fatalf("directory links were not preserved: %s", response.Body)
	}
}

func TestLocalDirectoriesRejectsInvalidPathsAndPagination(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		code  int
	}{
		{"?path=relative", 400},
		{"?path=" + url.QueryEscape(root+"\x00"), 400},
		{"?path=" + url.QueryEscape(file), 400},
		{"?path=" + url.QueryEscape(filepath.Join(root, "missing")), 404},
		{"?page=0", 400}, {"?page=-1", 400}, {"?page=bad", 400},
		{"?page=9223372036854775807", 400},
		{"?page_size=0", 400}, {"?page_size=201", 400},
	} {
		if response := localDirectoryRequest(t, test.query, true, false); response.Code != test.code {
			t.Errorf("query %q returned %d, want %d: %s", test.query, response.Code, test.code, response.Body)
		}
	}
}

func TestLocalDirectoriesRequiresLoginAndHasNoTokenScope(t *testing.T) {
	if response := localDirectoryRequest(t, "", false, false); response.Code != 401 {
		t.Fatalf("anonymous browsing returned %d", response.Code)
	}
	if response := localDirectoryRequest(t, "", true, true); response.Code != 403 {
		t.Fatalf("API token browsing returned %d", response.Code)
	}
	if scope := auth.APITokenScopeForRoute("/api/local-directories"); scope != "" {
		t.Fatalf("filesystem browsing inherited token scope %q", scope)
	}
	if response := localDirectoryRequest(t, "", true, false); response.Code != 200 {
		t.Fatalf("root browsing returned %d: %s", response.Code, response.Body)
	}
}
