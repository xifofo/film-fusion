package hdhive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryResourcesSendsAuthHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/resources/movie/550" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "app-secret" {
			t.Fatalf("unexpected api key header: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		_ = json.NewEncoder(w).Encode(APIResponse[[]Resource]{
			Success: true,
			Code:    "200",
			Message: "ok",
			Data: []Resource{
				{Slug: "resource-slug", IsUnlocked: true},
			},
			Meta: map[string]any{"total": 1},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "app-secret").WithAccessToken("user-token")
	resp, err := client.QueryResources(context.Background(), "movie", "550")
	if err != nil {
		t.Fatalf("query resources failed: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Slug != "resource-slug" {
		t.Fatalf("unexpected response data: %+v", resp.Data)
	}
	if resp.Meta["total"].(float64) != 1 {
		t.Fatalf("unexpected meta: %+v", resp.Meta)
	}
}

func TestUnlockResourcesDeduplicatesSlugs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Slugs []string `json:"slugs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		if len(body.Slugs) != 2 || body.Slugs[0] != "a" || body.Slugs[1] != "b" {
			t.Fatalf("unexpected slugs: %#v", body.Slugs)
		}
		_ = json.NewEncoder(w).Encode(APIResponse[BatchUnlockResult]{
			Success: true,
			Code:    "200",
			Message: "ok",
			Data: BatchUnlockResult{
				Total:        2,
				SuccessCount: 2,
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "app-secret").WithAccessToken("user-token")
	resp, err := client.UnlockResources(context.Background(), []string{"a", " ", "b", "a"})
	if err != nil {
		t.Fatalf("unlock resources failed: %v", err)
	}
	if resp.Data.Total != 2 || resp.Data.SuccessCount != 2 {
		t.Fatalf("unexpected response: %+v", resp.Data)
	}
}

func TestRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/openapi/oauth/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "app-secret" {
			t.Fatalf("unexpected api key header: %q", got)
		}
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		if body.RefreshToken != "refresh-token" {
			t.Fatalf("unexpected refresh token: %q", body.RefreshToken)
		}
		_ = json.NewEncoder(w).Encode(APIResponse[OAuthToken]{
			Success: true,
			Code:    "200",
			Message: "ok",
			Data: OAuthToken{
				AccessToken:  "new-access-token",
				RefreshToken: "new-refresh-token",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				Scope:        "query unlock",
				Scopes:       []string{"query", "unlock"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "app-secret")
	resp, err := client.RefreshToken(context.Background(), "refresh-token")
	if err != nil {
		t.Fatalf("refresh token failed: %v", err)
	}
	if resp.Data.AccessToken != "new-access-token" || resp.Data.RefreshToken != "new-refresh-token" {
		t.Fatalf("unexpected response: %+v", resp.Data)
	}
}

func TestBusinessErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(APIResponse[[]Resource]{
			Success: false,
			Code:    "OPENAPI_REFRESH_REQUIRED",
			Message: "OpenAPI access token 已过期，请使用 refresh_token 刷新",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "app-secret").WithAccessToken("expired-token")
	resp, err := client.QueryResources(context.Background(), "movie", "550")
	if err == nil {
		t.Fatalf("expected business error")
	}
	if resp == nil || resp.Code != "OPENAPI_REFRESH_REQUIRED" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(err.Error(), "OPENAPI_REFRESH_REQUIRED") {
		t.Fatalf("unexpected error: %v", err)
	}
}
