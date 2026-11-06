// 使用实际认证中间件验证管理员边界、严格输入和版本冲突 HTTP 契约。
package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"film-fusion/app/middleware"
	"film-fusion/app/model"
	"film-fusion/app/service"
)

func newDictionaryFixture(t *testing.T) *apiTokenFixture {
	t.Helper()
	f := newAPITokenFixture(t)
	if err := f.db.AutoMigrate(&model.Dictionary{}, &model.DictionaryItem{}); err != nil {
		t.Fatal(err)
	}
	h := NewDictionaryHandler(service.NewDictionaryService(f.db))
	api := f.router.Group("/api", middleware.APIAuth(f.cfg, service.NewAPITokenService(f.db)), middleware.RequireAdmin())
	api.GET("/dictionaries", h.List)
	api.POST("/dictionaries", h.Create)
	api.GET("/dictionaries/:code", h.Get)
	api.PATCH("/dictionaries/:code", h.Update)
	api.GET("/dictionaries/:code/items", h.Items)
	api.POST("/dictionaries/:code/items", h.CreateItem)
	api.GET("/dictionaries/:code/items/:id", h.GetItem)
	api.PATCH("/dictionaries/:code/items/:id", h.UpdateItem)
	api.DELETE("/dictionaries/:code/items/:id", h.DeleteItem)
	api.GET("/dictionary-options/:code", h.Options)
	return f
}

func TestDictionaryHTTPInputsAndConflict(t *testing.T) {
	f := newDictionaryFixture(t)
	for _, body := range []string{`null`, `[]`, `{}`, `{"code":"sample","name":null}`, `{"code":"sample","name":"Name","unexpected":true}`, `{"code":"sample","name":"Name"} {}`, `{"code":"sample","name":"` + strings.Repeat("x", 65<<10) + `"}`} {
		if w := f.request("POST", "/api/dictionaries", f.jwt, body); w.Code != 400 {
			t.Fatalf("invalid body accepted: %d", w.Code)
		}
	}
	w := f.request("POST", "/api/dictionaries", f.jwt, `{"code":"sample","name":"Sample"}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"expectedVersion":1,"code":"renamed"}`, 400},
		{`{"expectedVersion":1,"name":null}`, 400},
		{`{"expectedVersion":1}`, 400},
		{`{"name":"No version"}`, 400},
		{`{"expectedVersion":1,"name":"Changed","enabled":false}`, 200},
		{`{"expectedVersion":1,"name":"Stale"}`, 409},
	} {
		w = f.request("PATCH", "/api/dictionaries/sample", f.jwt, tc.body)
		if w.Code != tc.want {
			t.Fatalf("patch %s: %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.want == 409 && !strings.Contains(w.Body.String(), `"reason":"VERSION_CONFLICT"`) {
			t.Fatal("missing conflict reason")
		}
	}
	w = f.request("POST", "/api/dictionaries/sample/items", f.jwt, `{"value":"001","label":"Item","enabled":false,"sortOrder":0}`)
	var created struct {
		Data model.DictionaryItem `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Data.Value != "001" || created.Data.Enabled {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}
	path := "/api/dictionaries/sample/items/" + created.Data.ID
	for _, body := range []string{`{"expectedVersion":1,"value":"002"}`, `{"expectedVersion":1,"dictionaryId":"other"}`, `{"expectedVersion":1,"sortOrder":null}`, `{"expectedVersion":1,"sortOrder":0.5}`} {
		if w = f.request("PATCH", path, f.jwt, body); w.Code != 400 {
			t.Fatalf("immutable/invalid item input accepted: %s", body)
		}
	}
	for _, tc := range []struct {
		query string
		want  int
	}{{"", 400}, {"?expectedVersion=2", 409}, {"?expectedVersion=1", 200}, {"?expectedVersion=1", 404}} {
		if w = f.request("DELETE", path+tc.query, f.jwt, ""); w.Code != tc.want {
			t.Fatalf("delete %s: %d", tc.query, w.Code)
		}
	}
	for _, query := range []string{"?page=0", "?pageSize=101", "?enabled=maybe", "?sort=bad", "?order=bad"} {
		if w = f.request("GET", "/api/dictionaries"+query, f.jwt, ""); w.Code != 400 {
			t.Fatalf("invalid query %s returned %d", query, w.Code)
		}
	}
	if w = f.request("GET", "/api/dictionary-options/sample", f.jwt, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "DICTIONARY_DISABLED") {
		t.Fatal("disabled type candidates not rejected")
	}
}

func TestDictionaryHTTPRequiresAdminSession(t *testing.T) {
	f := newDictionaryFixture(t)
	token, _ := f.issue(t, "cloud-storage")
	for _, path := range []string{"/api/dictionaries", "/api/dictionary-options/cloud.115_apps"} {
		if w := f.request("GET", path, "", ""); w.Code != 401 {
			t.Fatalf("anonymous access=%d", w.Code)
		}
		if w := f.request("GET", path, token, ""); w.Code != 403 {
			t.Fatalf("API token access=%d", w.Code)
		}
	}
	if err := f.db.Model(&f.user).Update("is_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.request("POST", "/api/dictionaries", f.jwt, `{"code":"unauthorized","name":"No"}`); w.Code != 403 {
		t.Fatalf("non-admin write=%d", w.Code)
	}
	var count int64
	f.db.Model(&model.Dictionary{}).Count(&count)
	if count != 0 {
		t.Fatal("unauthorized request wrote dictionary")
	}
}
