package embyhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
)

func TestDisableAccountPreservesCompletePolicyAndVerifies(t *testing.T) {
	policy := map[string]json.RawMessage{
		"IsDisabled": json.RawMessage("false"), "IsAdministrator": json.RawMessage("false"),
		"EnabledFolders": json.RawMessage(`["private-library"]`), "EnableAllFolders": json.RawMessage("false"),
		"UnknownFutureField": json.RawMessage(`{"limit":9007199254740993}`),
	}
	gets, posts := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "test-key" || r.URL.Query().Has("api_key") {
			t.Error("API key must be sent in a header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /emby/Users/u1":
			gets++
			_ = json.NewEncoder(w).Encode(EmbyAccount{ID: "u1", Name: "Alice", ServerID: "s1", Policy: policy})
		case "POST /emby/Users/u1/Policy":
			posts++
			var got map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			for key, value := range policy {
				if key != "IsDisabled" && string(got[key]) != string(value) {
					t.Errorf("policy %s changed: %s", key, got[key])
				}
			}
			if string(got["IsDisabled"]) != "true" {
				t.Error("account was not disabled")
			}
			policy = got
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewAccountClient(&config.Config{Emby: config.EmbyConfig{URL: server.URL + "/emby/", APIKey: "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DisableAccount(context.Background(), "s1", "u1"); err != nil {
		t.Fatal(err)
	}
	if gets != 2 || posts != 1 {
		t.Fatalf("GET=%d POST=%d; expected read/write/verify", gets, posts)
	}
	if err := client.DisableAccount(context.Background(), "s1", "u1"); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatal("already disabled account was updated again")
	}
}

func TestDisableAccountRejectsUnsafeOrUnconfirmedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, user            string
		getStatus, postStatus int
		wantPosts             int
	}{
		{"wrong server", `{"Id":"u1","ServerId":"other","Policy":{"IsDisabled":false}}`, 200, 200, 0},
		{"wrong user", `{"Id":"other","ServerId":"s1","Policy":{"IsDisabled":false}}`, 200, 200, 0},
		{"missing policy", `{"Id":"u1","ServerId":"s1"}`, 200, 200, 0},
		{"missing disabled", `{"Id":"u1","ServerId":"s1","Policy":{"IsHidden":false}}`, 200, 200, 0},
		{"not found", `secret-test-key`, 404, 200, 0},
		{"forbidden", `{"Id":"u1","ServerId":"s1","Policy":{"IsDisabled":false}}`, 200, 403, 1},
		{"not actually disabled", `{"Id":"u1","ServerId":"s1","Policy":{"IsDisabled":false}}`, 200, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					posts++
					w.WriteHeader(tc.postStatus)
					_, _ = fmt.Fprint(w, "secret-test-key")
					return
				}
				w.WriteHeader(tc.getStatus)
				_, _ = fmt.Fprint(w, tc.user)
			}))
			defer server.Close()
			client, _ := NewAccountClient(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "secret-test-key"}})
			err := client.DisableAccount(context.Background(), "s1", "u1")
			if err == nil || strings.Contains(err.Error(), "secret-test-key") {
				t.Fatalf("unsafe result: %v", err)
			}
			if posts != tc.wantPosts {
				t.Fatalf("POST=%d, want %d", posts, tc.wantPosts)
			}
		})
	}
}
