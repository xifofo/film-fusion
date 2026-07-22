package service

import (
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"os"
	"path/filepath"
	"testing"
)

func TestCD2FileNotifyValidationRejectsTraversal(t *testing.T) {
	req := Cd2FileNotifyRequest{Data: []Cd2FileNotifyRequestData{{
		Action: "delete", IsDir: "true", SourceFile: "/CloudDrive/media/../../../data",
	}}}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() accepted a traversal path")
	}
}

func TestDeleteActionIsConfinedToLocalRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "victim.strm")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideDir := filepath.Join(root, "CloudDrive", "media")
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(insideDir, "escape")); err != nil {
		t.Fatal(err)
	}

	log := logger.New(config.LogConfig{Level: "error", Output: "stdout"})
	svc := NewStrmService(log, nil)
	svc.DeleteAction(root, "/CloudDrive/media/escape/victim.mkv", false)
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside file was removed through symlink: %v", err)
	}

	insideFile := filepath.Join(insideDir, "movie.strm")
	if err := os.WriteFile(insideFile, []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.DeleteAction(root, "/CloudDrive/media/movie.mkv", false)
	if _, err := os.Stat(insideFile); !os.IsNotExist(err) {
		t.Fatalf("inside STRM still exists, stat error = %v", err)
	}
}
