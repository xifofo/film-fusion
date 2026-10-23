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
