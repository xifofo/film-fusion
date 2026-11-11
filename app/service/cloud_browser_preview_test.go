package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
)

// installPreviewProvider serves fixed rows per directory and counts 115 calls.
func installPreviewProvider(svc *CloudBrowserService, rows map[string][]CloudBrowserEntry, calls *int) {
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		return cloudBrowserListFunc(func(_ context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
			*calls++
			return cloudBrowserTestPage(cid, offset, limit, rows[cid]), nil
		}), nil
	}
}

func TestCloudBrowserPreviewFileStatesWithoutWriting(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	var calls int
	installPreviewProvider(svc, map[string][]CloudBrowserEntry{
		"10": {{FileID: "100", ParentID: "10", Name: "Movie.mkv", IsFile: true, Complete: true, Size: 4096}},
	}, &calls)
	page, err := svc.Browse(context.Background(), 7, directory.ID, "cookie", "", 0, CloudBrowserMaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	token := page.Items[0].SelectionToken
	target := filepath.Join(directory.SavePath, "影片", "Movie.strm")
	preview := func() *CloudBrowserSTRMPreview {
		t.Helper()
		result, err := svc.PreviewSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: token})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := preview()
	if first.Status != "create" || first.Sampled || first.LocalPath != target || first.Content != "https://media.example/play/影片/Movie.mkv" || first.MatchedDirectoryName != "影片" {
		t.Fatalf("create preview=%+v", first)
	}
	if entries, err := os.ReadDir(directory.SavePath); err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %+v %v", entries, err)
	}

	if _, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: token}); err != nil {
		t.Fatal(err)
	}
	if got := preview(); got.Status != "unchanged" {
		t.Fatalf("unchanged preview=%+v", got)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := preview(); got.Status != "update" {
		t.Fatalf("update preview=%+v", got)
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Fatalf("preview changed existing file: %q", data)
	}
	if err := svc.db.Create(&model.OrganizeManagedStrm{Path: target, UserID: 7, CloudStorageID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if got := preview(); got.Status != "managed" {
		t.Fatalf("managed preview=%+v", got)
	}
	if calls != 1 {
		t.Fatalf("file preview queried 115: calls=%d", calls)
	}
	if _, err := svc.PreviewSTRM(context.Background(), 8, directory.ID, CloudBrowserGenerateRequest{SelectionToken: token}); err == nil {
		t.Fatal("cross-user preview accepted")
	}
}

func TestCloudBrowserPreviewDirectorySamplesFirstEligibleFile(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	var calls int
	installPreviewProvider(svc, map[string][]CloudBrowserEntry{
		"10": {
			{FileID: "20", ParentID: "10", Name: "子目录"},
			{FileID: "101", ParentID: "10", Name: "Movie.srt", IsFile: true, Complete: true},
			{FileID: "102", ParentID: "10", Name: "Movie.mkv", IsFile: true, Complete: true},
		},
		"20": {{FileID: "201", ParentID: "20", Name: "Only.srt", IsFile: true, Complete: true}},
	}, &calls)
	page, err := svc.Browse(context.Background(), 7, directory.ID, "cookie", "", 0, CloudBrowserMaxPageSize)
	if err != nil {
		t.Fatal(err)
	}

	current, err := svc.PreviewSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.CurrentDirectoryToken})
	if err != nil || current.Status != "create" || !current.Sampled || current.SampleName != "Movie.mkv" || current.LocalPath != filepath.Join(directory.SavePath, "影片", "Movie.strm") {
		t.Fatalf("directory preview=%+v err=%v", current, err)
	}
	empty, err := svc.PreviewSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.Items[0].SelectionToken})
	if err != nil || empty.Status != "empty" || !empty.Sampled || empty.LocalPath != "" {
		t.Fatalf("empty preview=%+v err=%v", empty, err)
	}
	// One browse plus one first-page listing per directory preview; no recursion.
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
	if entries, err := os.ReadDir(directory.SavePath); err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %+v %v", entries, err)
	}
}
