package pathhelper

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestIsSubPathRejectsTraversalAndPrefixCollision(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		prefix    string
		want      bool
	}{
		{name: "child", candidate: "/CloudDrive/media/movie.mkv", prefix: "/CloudDrive/media", want: true},
		{name: "same", candidate: "/CloudDrive/media", prefix: "/CloudDrive/media", want: true},
		{name: "prefix collision", candidate: "/CloudDrive/media-evil/movie.mkv", prefix: "/CloudDrive/media", want: false},
		{name: "unix traversal", candidate: "/CloudDrive/media/../../etc/passwd", prefix: "/CloudDrive/media", want: false},
		{name: "windows traversal", candidate: `C:\CloudDrive\media\..\..\Windows\win.ini`, prefix: `C:\CloudDrive\media`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSubPath(tt.candidate, tt.prefix); got != tt.want {
				t.Fatalf("IsSubPath(%q, %q) = %v, want %v", tt.candidate, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestJoinUnderRootRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := JoinUnderRoot(root, "/CloudDrive/media/../../../outside"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("JoinUnderRoot traversal error = %v, want ErrUnsafePath", err)
	}

	got, err := JoinUnderRoot(root, "/CloudDrive/media/movie.mkv")
	if err != nil {
		t.Fatalf("JoinUnderRoot valid path: %v", err)
	}
	want := filepath.Join(root, "CloudDrive", "media", "movie.mkv")
	if got != want {
		t.Fatalf("JoinUnderRoot = %q, want %q", got, want)
	}
}
