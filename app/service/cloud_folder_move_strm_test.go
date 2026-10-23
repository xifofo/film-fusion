package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestCloudMovedSTRMMappingSourceSemantics(t *testing.T) {
	for _, test := range []struct {
		name, sourceType, source, actual, wantEvent string
	}{
		{"CD2 mount", model.SourceTypeCloudDrive2, "/挂载/电影", "/电影/A/A.mkv", "/挂载/电影/A/A.mkv"},
		{"CD2 windows", model.SourceTypeCloudDrive2, `X:\挂载\电影`, "/电影/A/A.mkv", "/挂载/电影/A/A.mkv"},
		{"CD2 whole drive", model.SourceTypeCloudDrive2, "/挂载", "/电影/A/A.mkv", "/挂载/电影/A/A.mkv"},
		{"CD2 empty mount", model.SourceTypeCloudDrive2, "/", "/电影/A/A.mkv", ""},
		{"MP2", model.SourceTypeMoviePilot2, "/电影", "/电影/A/A.mkv", "/电影/A/A.mkv"},
		{"MP2 root", model.SourceTypeMoviePilot2, "/", "/电影/A/A.mkv", "/电影/A/A.mkv"},
		{"prefix collision", model.SourceTypeCloudDrive2, "/挂载/电影", "/电影2/A/A.mkv", ""},
		{"different root", model.SourceTypeMoviePilot2, "/电视", "/电影/A/A.mkv", ""},
		{"traversal source", model.SourceTypeCloudDrive2, "/挂载/../电影", "/电影/A/A.mkv", ""},
		{"unknown source", "other", "/电影", "/电影/A/A.mkv", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapping, event, err := cloudMovedSTRMMapping([]model.CloudPath{{ID: 1, SourceType: test.sourceType, SourcePath: test.source}}, test.actual)
			if err != nil || event != test.wantEvent || ((mapping != nil) != (test.wantEvent != "")) {
				t.Fatalf("mapping=%+v event=%q err=%v", mapping, event, err)
			}
		})
	}
	mappings := []model.CloudPath{
		{ID: 1, SourceType: model.SourceTypeMoviePilot2, SourcePath: "/"},
		{ID: 2, SourceType: model.SourceTypeCloudDrive2, SourcePath: "/挂载/电影"},
		{ID: 3, SourceType: model.SourceTypeMoviePilot2, SourcePath: "/电影/A"},
	}
	selected, _, err := cloudMovedSTRMMapping(mappings, "/电影/A/A.mkv")
	if err != nil || selected == nil || selected.ID != 3 {
		t.Fatalf("longest mapping lost: %+v %v", selected, err)
	}
	mappings = append(mappings, model.CloudPath{ID: 4, SourceType: model.SourceTypeCloudDrive2, SourcePath: "/另一挂载/电影/A"})
	if _, _, err := cloudMovedSTRMMapping(mappings, "/电影/A/A.mkv"); err == nil {
		t.Fatal("equally specific mappings silently selected")
	}
}

func TestCloudMovedSTRMFilterRules(t *testing.T) {
	for _, test := range []struct {
		name, file, rules string
		allowed, invalid  bool
	}{
		{"empty", "/Movie.MKV", "", true, false},
		{"whitespace", "/Movie.mkv", "  ", true, false},
		{"include case insensitive", "/Movie.MKV", `{"include":["mkv"],"download":["srt"]}`, true, false},
		{"download empty", "/Movie.mkv", `{"include":[".mkv"],"download":[]}`, true, false},
		{"download excluded", "/Movie.SRT", `{"include":["mkv","srt"],"download":["srt"]}`, false, false},
		{"not included", "/Movie.avi", `{"include":["mkv"]}`, false, false},
		{"bad json", "/Movie.mkv", `{`, false, true},
		{"bad shape", "/Movie.mkv", `[]`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			allowed, err := cloudMovedSTRMFilter(test.file, test.rules)
			if allowed != test.allowed || (err != nil) != test.invalid {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func saveCloudSTRMMapping(t *testing.T, svc *CloudFolderMoveService, mapping model.CloudPath) model.CloudPath {
	t.Helper()
	if mapping.UserID == 0 {
		mapping.UserID = 7
	}
	if mapping.CloudStorageID == 0 {
		mapping.CloudStorageID = 1
	}
	if mapping.LinkType == "" {
		mapping.LinkType = model.LinkTypeStrm
	}
	if err := svc.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	return mapping
}

func TestCloudFolderMoveGeneratesSTRMAndRetryDoesNotMoveAgain(t *testing.T) {
	for _, sourceType := range []string{model.SourceTypeCloudDrive2, model.SourceTypeMoviePilot2} {
		t.Run(sourceType, func(t *testing.T) {
			svc, provider, request := folderMoveFixture(t)
			root := t.TempDir()
			source, event := "/自定义位置", "/自定义位置/Movie/Movie.mkv"
			if sourceType == model.SourceTypeCloudDrive2 {
				source, event = "/挂载/自定义位置", "/挂载/自定义位置/Movie/Movie.mkv"
			}
			mapping := saveCloudSTRMMapping(t, svc, model.CloudPath{SourceType: sourceType, SourcePath: source, LocalPath: root, ContentPrefix: "https://media.example/play", FilterRules: `{"include":["mkv"],"download":["srt"]}`})
			result, err := svc.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "moved" || result.STRM == nil || result.STRM.Status != "created" || result.STRM.CloudPathID != mapping.ID {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			wantPath := filepath.Join(root, strings.TrimPrefix(strings.TrimSuffix(event, ".mkv")+".strm", "/"))
			data, readErr := os.ReadFile(wantPath)
			if readErr != nil || string(data) != "https://media.example/play"+event || result.STRM.LocalPath != wantPath {
				t.Fatalf("data=%q err=%v result=%+v", data, readErr, result.STRM)
			}
			request.FolderID = result.FolderID
			result, err = svc.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "already_moved" || result.STRM.Status != "created" || provider.mkdirCalls != 1 || provider.moveCalls != 1 || provider.resolveCalls != 2 {
				t.Fatalf("retry=%+v err=%v mutations=%d/%d", result, err, provider.mkdirCalls, provider.moveCalls)
			}
		})
	}
}

func TestCloudFolderMoveSTRMURIEncodingAndOwnerIsolation(t *testing.T) {
	svc, provider, request := folderMoveFixture(t)
	provider.dirs["10"][0].Name, request.FileName = "A #片.mkv", "A #片.mkv"
	root := t.TempDir()
	mapping := saveCloudSTRMMapping(t, svc, model.CloudPath{SourceType: model.SourceTypeCloudDrive2, SourcePath: "/挂载/自定义位置", LocalPath: root, ContentPrefix: "https://media.example/play/", ContentEncodeURI: true})
	otherRoot := t.TempDir()
	saveCloudSTRMMapping(t, svc, model.CloudPath{UserID: 8, SourceType: model.SourceTypeMoviePilot2, SourcePath: "/自定义位置/Movie", LocalPath: otherRoot})
	saveCloudSTRMMapping(t, svc, model.CloudPath{CloudStorageID: 2, SourceType: model.SourceTypeMoviePilot2, SourcePath: "/自定义位置/Movie", LocalPath: otherRoot})
	result, err := svc.Move(context.Background(), 7, 1, request)
	if err != nil || result.STRM.Status != "created" || result.STRM.CloudPathID != mapping.ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.STRM.LocalPath)
	want := "https://media.example/play/%E6%8C%82%E8%BD%BD/%E8%87%AA%E5%AE%9A%E4%B9%89%E4%BD%8D%E7%BD%AE/Movie/A%20%23%E7%89%87.mkv"
	if err != nil || string(data) != want {
		t.Fatalf("URI content=%q want=%q err=%v", data, want, err)
	}
	if entries, err := os.ReadDir(otherRoot); err != nil || len(entries) != 0 {
		t.Fatalf("wrote other account's root: %+v %v", entries, err)
	}
}

func TestCloudFolderMoveSTRMSkipsUnmappedAndFilteredFiles(t *testing.T) {
	for _, mode := range []string{"no mappings", "outside mapping", "prefix boundary", "filter excluded", "download extension"} {
		t.Run(mode, func(t *testing.T) {
			svc, provider, request := folderMoveFixture(t)
			root := t.TempDir()
			mapping := model.CloudPath{SourceType: model.SourceTypeMoviePilot2, SourcePath: "/自定义位置", LocalPath: root}
			switch mode {
			case "outside mapping":
				mapping.SourcePath = "/other"
			case "prefix boundary":
				mapping.SourcePath = "/自定义位"
			case "filter excluded":
				mapping.FilterRules = `{"include":["mp4"]}`
			case "download extension":
				mapping.FilterRules = `{"include":["mkv"],"download":["mkv"]}`
			}
			if mode != "no mappings" {
				saveCloudSTRMMapping(t, svc, mapping)
			}
			result, err := svc.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "moved" || result.STRM.Status != "skipped" || provider.moveCalls != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if mode == "no mappings" && provider.resolveCalls != 0 {
				t.Fatal("unmapped move added a provider request")
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("filtered file generated local artifact: %+v %v", entries, err)
			}
		})
	}
}

func TestCloudFolderMoveSTRMFailurePreservesMoveAndCanRetry(t *testing.T) {
	for _, mode := range []string{"local conflict", "root is file", "managed path", "ambiguous mappings", "path lookup failure", "invalid provider path", "relative local root", "invalid filter"} {
		t.Run(mode, func(t *testing.T) {
			svc, provider, request := folderMoveFixture(t)
			root := t.TempDir()
			mapping := model.CloudPath{SourceType: model.SourceTypeMoviePilot2, SourcePath: "/自定义位置", LocalPath: root}
			localPath := filepath.Join(root, "自定义位置/Movie/Movie.strm")
			switch mode {
			case "local conflict":
				if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(localPath, []byte("preserve me"), 0644); err != nil {
					t.Fatal(err)
				}
			case "root is file":
				mapping.LocalPath = filepath.Join(root, "not-a-directory")
				if err := os.WriteFile(mapping.LocalPath, []byte("preserve me"), 0644); err != nil {
					t.Fatal(err)
				}
			case "managed path":
				if err := svc.db.Create(&model.OrganizeManagedStrm{Path: localPath, CloudStorageID: 2, UserID: 8}).Error; err != nil {
					t.Fatal(err)
				}
			case "ambiguous mappings":
				saveCloudSTRMMapping(t, svc, mapping)
			case "path lookup failure":
				provider.resolveErr = errors.New("https://115.example?token=secret")
			case "invalid provider path":
				provider.resolvePath = "/自定义位置/../outside"
			case "relative local root":
				mapping.LocalPath = "relative"
			case "invalid filter":
				mapping.FilterRules = "{invalid"
			}
			saved := saveCloudSTRMMapping(t, svc, mapping)
			result, err := svc.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "moved" || result.STRM == nil || result.STRM.Status != "failed" || provider.moveCalls != 1 {
				t.Fatalf("cloud success lost: %+v err=%v", result, err)
			}
			if strings.Contains(result.STRM.Message, "secret") || strings.Contains(result.STRM.Message, "115.example") {
				t.Fatalf("error leaked: %+v", result.STRM)
			}
			if mode == "local conflict" {
				if data, err := os.ReadFile(localPath); err != nil || string(data) != "preserve me" {
					t.Fatalf("conflict overwritten: %q %v", data, err)
				}
			}
			// Repair only the failed local mapping, then repeat the same request.
			fixedRoot := t.TempDir()
			if err := svc.db.Where("id <> ?", saved.ID).Delete(&model.CloudPath{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.db.Model(&model.CloudPath{}).Where("id = ?", saved.ID).Updates(map[string]any{"local_path": fixedRoot, "filter_rules": ""}).Error; err != nil {
				t.Fatal(err)
			}
			provider.resolveErr, provider.resolvePath = nil, ""
			request.FolderID = result.FolderID
			result, err = svc.Move(context.Background(), 7, 1, request)
			if err != nil || result.Status != "already_moved" || result.STRM.Status != "created" || provider.moveCalls != 1 || provider.mkdirCalls != 1 {
				t.Fatalf("retry moved twice or failed: %+v %v", result, err)
			}
		})
	}
}

func TestCloudMovedSTRMRejectsUnsafeFilenameAndSymlinkEscape(t *testing.T) {
	for _, name := range []string{"../outside.mkv", "a/b.mkv", `a\b.mkv`, ".", "..", "bad\x00.mkv", "bad\n.mkv", string([]byte{0xff})} {
		t.Run(name, func(t *testing.T) {
			svc, provider, _ := folderMoveFixture(t)
			root := t.TempDir()
			saveCloudSTRMMapping(t, svc, model.CloudPath{SourceType: model.SourceTypeMoviePilot2, SourcePath: "/", LocalPath: root})
			result := svc.createMovedFileSTRM(context.Background(), provider, 7, 1, &CloudFolderMoveResult{FolderID: "20", FileName: name})
			if result.Status != "failed" || provider.resolveCalls != 0 {
				t.Fatalf("unsafe name accepted: %+v", result)
			}
		})
	}
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := writeCloudMovedSTRM(context.Background(), root, "escape/Movie.strm", "content"); err == nil {
		t.Fatal("escaped local root through symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "Movie.strm")); !os.IsNotExist(err) {
		t.Fatalf("outside artifact written: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeCloudMovedSTRM(ctx, root, "Movie.strm", "content"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
}

func TestCloudMovedSTRMConcurrentPublishPreservesWinner(t *testing.T) {
	for _, identical := range []bool{true, false} {
		t.Run(map[bool]string{true: "same content", false: "different content"}[identical], func(t *testing.T) {
			root := t.TempDir()
			contents := []string{"https://media.example/first.mkv", "https://media.example/second.mkv"}
			if identical {
				contents[1] = contents[0]
			}
			start := make(chan struct{})
			done := make(chan error, 2)
			for _, content := range contents {
				go func() { <-start; done <- writeCloudMovedSTRM(context.Background(), root, "Movie.strm", content) }()
			}
			close(start)
			failures := 0
			for range 2 {
				if err := <-done; err != nil {
					if !errors.Is(err, errCloudSTRMContentConflict) {
						t.Errorf("unexpected publish error: %v", err)
					}
					failures++
				}
			}
			if (identical && failures != 0) || (!identical && failures != 1) {
				t.Fatalf("concurrent writes failures=%d identical=%v", failures, identical)
			}
			data, err := os.ReadFile(filepath.Join(root, "Movie.strm"))
			if err != nil || (string(data) != contents[0] && string(data) != contents[1]) {
				t.Fatalf("incomplete or absent result: %q %v", data, err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
				t.Fatalf("temporary publish files left behind: %+v %v", entries, err)
			}
		})
	}
}
