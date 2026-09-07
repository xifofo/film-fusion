package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type expiryTestEmby struct {
	mu          sync.Mutex
	disabled    map[string]bool
	posts       map[string]int
	failedUser  string
	listStatus  int
	listed      chan struct{}
	releaseList chan struct{}
}

func (f *expiryTestEmby) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	user := func(id string) embyhelper.EmbyAccount {
		disabled, _ := json.Marshal(f.disabled[id])
		return embyhelper.EmbyAccount{ID: id, ServerID: "s1", Name: id, Policy: map[string]json.RawMessage{"IsDisabled": disabled, "IsAdministrator": json.RawMessage("false")}}
	}
	if r.URL.Path == "/Users" {
		if f.listed != nil {
			listed, release := f.listed, f.releaseList
			f.listed = nil
			close(listed)
			// Allow a concurrent SetExpiry GET while this scan is paused.
			f.mu.Unlock()
			<-release
			f.mu.Lock()
		}
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			return
		}
		users := make([]embyhelper.EmbyAccount, 0, len(f.disabled))
		for id := range f.disabled {
			users = append(users, user(id))
		}
		_ = json.NewEncoder(w).Encode(users)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Users/"), "/Policy")
	if _, exists := f.disabled[id]; !exists {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		f.posts[id]++
		if f.failedUser == id {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.disabled[id] = true
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = json.NewEncoder(w).Encode(user(id))
}

func newExpiryTestService(t *testing.T) (*EmbyAccountService, *expiryTestEmby) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "accounts.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.EmbyAccountExpiry{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	fake := &expiryTestEmby{disabled: map[string]bool{"expired": false, "future": false, "unlimited": false, "disabled": true}, posts: make(map[string]int)}
	upstream := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(upstream.Close)
	cfg := &config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}}
	return NewEmbyAccountService(db, cfg, nil), fake
}

func mustSetExpiry(t *testing.T, svc *EmbyAccountService, id string, at *time.Time) {
	t.Helper()
	if _, err := svc.SetExpiry(context.Background(), "s1", id, at); err != nil {
		t.Fatal(err)
	}
}

func expiryRecord(t *testing.T, svc *EmbyAccountService, id string) model.EmbyAccountExpiry {
	t.Helper()
	var record model.EmbyAccountExpiry
	if err := svc.db.Where("server_id = ? AND emby_user_id = ?", "s1", id).First(&record).Error; err != nil {
		t.Fatal(err)
	}
	return record
}

func TestEmbyAccountExpiryPersistsAndDisablesOnlyDueUsers(t *testing.T) {
	svc, fake := newExpiryTestService(t)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for _, id := range []string{"expired", "disabled"} {
		mustSetExpiry(t, svc, id, &past)
	}
	mustSetExpiry(t, svc, "future", &future)
	mustSetExpiry(t, svc, "unlimited", nil)
	// Simulate process restart: only the persisted DB/settings survive.
	restarted := NewEmbyAccountService(svc.db, svc.cfg, nil)
	restarted.Start()
	defer restarted.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for expiryRecord(t, svc, "expired").DisabledAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("startup did not process overdue account")
		}
		time.Sleep(10 * time.Millisecond)
	}
	restarted.Stop()
	if err := restarted.CheckExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["expired"] != 1 || len(fake.posts) != 1 {
		t.Fatalf("unexpected policy writes: %v", fake.posts)
	}
	if fake.disabled["future"] || fake.disabled["unlimited"] {
		t.Fatal("non-expired user was disabled")
	}
}

func TestEmbyAccountExpiryRetriesFailureAndContinuesOtherUsers(t *testing.T) {
	svc, fake := newExpiryTestService(t)
	past := time.Now().Add(-time.Minute)
	mustSetExpiry(t, svc, "expired", &past)
	mustSetExpiry(t, svc, "future", &past)
	fake.failedUser = "expired"
	if err := svc.CheckExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := expiryRecord(t, svc, "expired")
	if failed.LastError == "" || failed.DisabledAt != nil || failed.LastCheckedAt == nil {
		t.Fatalf("failure was not persisted: %+v", failed)
	}
	if expiryRecord(t, svc, "future").DisabledAt == nil {
		t.Fatal("one upstream failure blocked another account")
	}
	fake.failedUser = ""
	if err := svc.CheckExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := expiryRecord(t, svc, "expired")
	if recovered.LastError != "" || recovered.DisabledAt == nil || fake.posts["expired"] != 2 {
		t.Fatalf("retry did not recover: %+v", recovered)
	}
}

func TestEmbyAccountExpiryCancellationAndRenewalPreventStaleScan(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "renew"}[cancel], func(t *testing.T) {
			svc, fake := newExpiryTestService(t)
			past := time.Now().Add(-time.Minute)
			mustSetExpiry(t, svc, "expired", &past)
			fake.listed, fake.releaseList = make(chan struct{}), make(chan struct{})
			listed, release := fake.listed, fake.releaseList
			finished := make(chan error, 1)
			go func() { finished <- svc.CheckExpired(context.Background()) }()
			select {
			case <-listed:
			case <-time.After(3 * time.Second):
				t.Fatal("scan did not reach user list")
			}
			future := time.Now().Add(time.Hour)
			var at *time.Time
			if !cancel {
				at = &future
			}
			_, saveErr := svc.SetExpiry(context.Background(), "s1", "expired", at)
			close(release)
			if saveErr != nil {
				t.Fatal(saveErr)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if fake.posts["expired"] != 0 {
				t.Fatal("stale scan disabled renewed/cancelled account")
			}
		})
	}
}

func TestEmbyAccountExpiryServerIsolationAndListMerge(t *testing.T) {
	svc, fake := newExpiryTestService(t)
	past := time.Now().Add(-time.Minute)
	if err := svc.db.Create(&model.EmbyAccountExpiry{ServerID: "old-server", EmbyUserID: "expired", ExpiresAt: &past}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.posts) != 0 {
		t.Fatal("old server expiry leaked to new server")
	}
	if _, err := svc.SetExpiry(context.Background(), "old-server", "expired", &past); err == nil {
		t.Fatal("stale browser server was accepted")
	}
	rows, err := svc.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("users=%d", len(rows))
	}
	for _, row := range rows {
		if row.ExpiresAt != nil {
			t.Fatal("other server expiry shown")
		}
	}
}

func TestEmbyAccountExpiryPollAndWakeAfterSaving(t *testing.T) {
	svc, fake := newExpiryTestService(t)
	svc.interval = 20 * time.Millisecond
	svc.Start()
	defer svc.Stop()
	soon := time.Now().Add(150 * time.Millisecond)
	mustSetExpiry(t, svc, "expired", &soon)
	deadline := time.Now().Add(3 * time.Second)
	for expiryRecord(t, svc, "expired").DisabledAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("ticker did not disable newly expired user")
		}
		time.Sleep(10 * time.Millisecond)
	}
	svc.Stop()
	// Removing the deadline must not silently re-enable an existing disabled user.
	mustSetExpiry(t, svc, "expired", nil)
	if !fake.disabled["expired"] || fake.posts["expired"] != 1 {
		t.Fatal("cancellation changed Emby policy")
	}
	if record := expiryRecord(t, svc, "expired"); record.ExpiresAt != nil || record.DisabledAt != nil {
		t.Fatalf("cancellation failed: %+v", record)
	}
}
