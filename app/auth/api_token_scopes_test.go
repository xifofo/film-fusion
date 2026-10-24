package auth

import "testing"

func TestEmbyLibraryCheckTokenScope(t *testing.T) {
	if got := APITokenScopeForRoute("/api/emby-library-check"); got != "emby-library" {
		t.Fatalf("library check scope = %q", got)
	}
	if got := APITokenScopeForRoute("/api/emby-library-check-extra"); got != "" {
		t.Fatalf("unrelated route inherits library scope: %q", got)
	}
}

func TestCloudBrowserTokenScope(t *testing.T) {
	for _, route := range []string{
		"/api/directories/:id/browser",
		"/api/directories/:id/browser/strm",
		"/api/directories/:id/browser/jobs",
		"/api/directories/:id/browser/jobs/:job_id",
	} {
		if got := APITokenScopeForRoute(route); got != "directories" {
			t.Errorf("%s scope = %q; want directories", route, got)
		}
	}
	if got := APITokenScopeForRoute("/api/directories-extra/1/browser"); got != "" {
		t.Fatalf("unrelated route inherits directory scope: %q", got)
	}
}
