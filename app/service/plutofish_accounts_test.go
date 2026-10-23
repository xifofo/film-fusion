// 已保存密钥仅能用于原服务和原本地管理员；查询本身不保存新密钥或启动消费。
package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPlutoFishAccountsSavedSecretScope(t *testing.T) {
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/events/accounts" || r.Header.Get("Authorization") != "Bearer local-fake" {
			t.Error("unexpected endpoint or key")
		}
		fmt.Fprint(w, `{"ownerUID":"actual-owner","items":[],"nextAfter":"","hasMore":false}`)
	}))
	defer remote.Close()
	ctx := context.Background()
	svc := NewPlutoFishService(openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db")))
	if _, err := svc.Accounts(ctx, remote.URL, "local-fake", 1); err != nil {
		t.Fatal(err)
	}
	cfg, _ := svc.Config(ctx)
	if cfg.KeyConfigured {
		t.Fatal("discovery persisted a secret")
	}
	cfg.BaseURL, cfg.OwnerUID, cfg.APIKey = remote.URL, "actual-owner", "local-fake"
	if err := svc.Save(ctx, cfg, 1); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Accounts(ctx, remote.URL+"/", "", 1)
	if err != nil || result.OwnerUID != "actual-owner" {
		t.Fatal(result, err)
	}
	before := calls.Load()
	if _, err := svc.Accounts(ctx, remote.URL, "", 2); err == nil {
		t.Fatal("another local user reused saved secret")
	}
	if _, err := svc.Accounts(ctx, remote.URL+"/other", "", 1); err == nil {
		t.Fatal("saved secret forwarded to changed service URL")
	}
	if calls.Load() != before {
		t.Fatal("invalid requests contacted a server")
	}
}
