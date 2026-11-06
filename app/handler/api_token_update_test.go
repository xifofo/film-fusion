// API Token 更新集成测试使用隔离数据库验证认证、字段更新及撤销边界。
package handler

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"film-fusion/app/auth"
	"film-fusion/app/model"
)

// TestAPITokenUpdate 验证局部写入、凭据保持和授权即时生效。
func TestAPITokenUpdate(t *testing.T) {
	f := newAPITokenFixture(t)
	raw, record := f.issue(t, "emby-stats")
	path := fmt.Sprintf("/api/api-tokens/%d", record.ID)
	var before model.APIToken
	if err := f.db.First(&before, record.ID).Error; err != nil {
		t.Fatal(err)
	}

	w := f.request("PATCH", path, f.jwt, `{"name":"  renamed  "}`)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), raw) || strings.Contains(w.Body.String(), before.TokenHash) {
		t.Fatal("update leaked credential")
	}
	var after model.APIToken
	if err := f.db.First(&after, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != "renamed" || !after.ExpiresAt.Equal(*before.ExpiresAt) || !reflect.DeepEqual(after.Scopes, before.Scopes) || after.TokenHash != before.TokenHash || after.Prefix != before.Prefix {
		t.Fatal("rename changed other token fields")
	}

	w = f.request("PATCH", path, f.jwt, `{"scopes":["system-info","system-info"],"expires_at":null}`)
	if w.Code != 200 {
		t.Fatalf("scope update failed: %s", w.Body.String())
	}
	var response struct{ Data model.APIToken }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ExpiresAt != nil || !reflect.DeepEqual(response.Data.Scopes, []string{"system-info"}) {
		t.Fatal("scope deduplication or clearing expiry failed")
	}
	if w := f.request("GET", "/api/emby-stats", raw, ""); w.Code != 403 {
		t.Fatal("removed scope is still authorized")
	}
	if w := f.request("GET", "/api/system-info", raw, ""); w.Code != 200 {
		t.Fatal("same credential cannot use updated scope")
	}

	// 只改名称允许保留已过期状态；显式续期才恢复凭据效力。
	expired := time.Now().Add(-time.Hour).UTC()
	if err := f.db.Model(&after).Update("expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.request("PATCH", path, f.jwt, `{"name":"expired record"}`); w.Code != 200 {
		t.Fatalf("expired metadata edit failed: %s", w.Body.String())
	}
	if w := f.request("GET", "/api/system-info", raw, ""); w.Code != 401 {
		t.Fatal("rename renewed expired credential")
	}
	expires := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	if w := f.request("PATCH", path, f.jwt, `{"expires_at":"`+expires+`"}`); w.Code != 200 {
		t.Fatalf("renewal failed: %s", w.Body.String())
	}
	if w := f.request("GET", "/api/system-info", raw, ""); w.Code != 200 {
		t.Fatal("renewed token is not usable")
	}
}

// TestAPITokenUpdateRejectsInvalidAndUnauthorized 验证无效输入、账号隔离和不可逆撤销。
func TestAPITokenUpdateRejectsInvalidAndUnauthorized(t *testing.T) {
	f := newAPITokenFixture(t)
	raw, record := f.issue(t, "emby-stats")
	path := fmt.Sprintf("/api/api-tokens/%d", record.ID)
	for _, body := range []string{
		`{}`, `null`, `{"name":"  "}`, `{"name":null}`, `{"scopes":[]}`, `{"scopes":["unknown"]}`,
		`{"name":"changed","scopes":["*"]}`, `{"expires_at":"2000-01-01T00:00:00Z"}`,
		`{"expires_at":"bad"}`, `{"revoked_at":null}`, `{"token_hash":"replacement"}`,
		`{"name":"new"} {}`, `{"name":"new"}` + strings.Repeat(" ", 8192),
		`{"name":"` + strings.Repeat("长", 81) + `"}`,
	} {
		if w := f.request("PATCH", path, f.jwt, body); w.Code != 400 {
			t.Fatalf("invalid update accepted: %s, %d", body, w.Code)
		}
	}
	var stored model.APIToken
	if err := f.db.First(&stored, record.ID).Error; err != nil || stored.Name != record.Name {
		t.Fatal("invalid request partially wrote fields")
	}
	for _, id := range []string{"0", "-1", "abc", "4294967296"} {
		if w := f.request("PATCH", "/api/api-tokens/"+id, f.jwt, `{"name":"new"}`); w.Code != 400 {
			t.Fatalf("invalid ID accepted: %s", id)
		}
	}

	other := model.User{Username: "other", Password: "unused", IsActive: true, IsAdmin: true}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherJWT, err := auth.NewJWTService(f.cfg).GenerateToken(other.ID, other.Username)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{otherJWT, 404}, {raw, 403}, {"", 401}} {
		if w := f.request("PATCH", path, tc.token, `{"name":"new"}`); w.Code != tc.want {
			t.Fatalf("authorization: got %d want %d", w.Code, tc.want)
		}
	}
	if w := f.request("POST", path+"/revoke", f.jwt, ""); w.Code != 200 {
		t.Fatal("revoke failed")
	}
	if w := f.request("PATCH", path, f.jwt, `{"name":"new","expires_at":null}`); w.Code != 409 {
		t.Fatalf("revoked token accepted update: %d", w.Code)
	}
	if w := f.request("GET", "/api/emby-stats", raw, ""); w.Code != 401 {
		t.Fatal("update restored revoked token")
	}
}
