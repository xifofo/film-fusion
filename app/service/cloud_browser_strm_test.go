package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func browserSTRMFixture(t *testing.T) (*CloudBrowserService, model.MediaDirectory, []CloudBrowserBreadcrumb, CloudBrowserEntry) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strm.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.OrganizeManagedStrm{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	config := model.MediaDirectory{OrganizeEnabled: true, ID: 5, UserID: 7, CloudStorageID: 1, DirectoryID: "10", DirectoryName: "Library", SavePath: t.TempDir(), ContentPrefix: "https://media.example/play", IncludeExtensions: `["mkv","mp4"]`}
	ancestors := []CloudBrowserBreadcrumb{{ID: "0", Name: "根目录"}, {ID: "10", ParentID: "0", Name: "actual 115 root"}, {ID: "20", ParentID: "10", Name: "Season 1"}}
	item := CloudBrowserEntry{FileID: "30", ParentID: "20", Name: "Movie.mkv", IsFile: true, Size: 100 << 20, Complete: true}
	return &CloudBrowserService{db: db}, config, ancestors, item
}

func TestCloudBrowserSTRMMatchesDeepestDirectoryID(t *testing.T) {
	_, config, ancestors, _ := browserSTRMFixture(t)
	configs := []model.MediaDirectory{{OrganizeEnabled: true, ID: 1, DirectoryID: "0"}, config, {OrganizeEnabled: true, ID: 9, DirectoryID: "20"}}
	matched, depth, err := cloudBrowserMatchDirectory(configs, ancestors)
	if err != nil || matched == nil || matched.ID != 9 || depth != 2 {
		t.Fatalf("match=%+v depth=%d err=%v", matched, depth, err)
	}
	configs = append(configs, model.MediaDirectory{OrganizeEnabled: true, ID: 11, DirectoryID: "20"})
	if _, _, err := cloudBrowserMatchDirectory(configs, ancestors); err == nil {
		t.Fatal("ambiguous configuration accepted")
	}
	for _, mode := range []string{"wrong parent", "duplicate ancestor", "unsafe name"} {
		t.Run(mode, func(t *testing.T) {
			broken := append([]CloudBrowserBreadcrumb(nil), ancestors...)
			switch mode {
			case "wrong parent":
				broken[2].ParentID = "99"
			case "duplicate ancestor":
				broken[2].ID = "10"
			case "unsafe name":
				broken[2].Name = "../outside"
			}
			if _, _, err := cloudBrowserMatchDirectory([]model.MediaDirectory{config}, broken); err == nil {
				t.Fatal("unsafe ancestry accepted")
			}
		})
	}
	if selected, _, err := cloudBrowserMatchDirectory([]model.MediaDirectory{{OrganizeEnabled: true, DirectoryID: "999"}}, ancestors); err != nil || selected != nil {
		t.Fatalf("unmatched directory=%+v err=%v", selected, err)
	}
}

func TestCloudBrowserSTRMConfiguredAliasAndRegeneration(t *testing.T) {
	svc, config, ancestors, item := browserSTRMFixture(t)
	config.DirectoryName, config.ContentEncodeURI = "媒体/Film", true
	item.Name = "A #片.mkv"
	wantPath := filepath.Join(config.SavePath, "媒体/Film/Season 1/A #片.strm")
	wantContent := "https://media.example/play/%E5%AA%92%E4%BD%93/Film/Season%201/A%20%23%E7%89%87.mkv"
	preflight, err := cloudBrowserSTRMTarget([]model.MediaDirectory{config}, ancestors, item)
	if err != nil || preflight != wantPath {
		t.Fatalf("preflight=%q err=%v", preflight, err)
	}
	collision := item
	collision.FileID, collision.Name = "31", "A #片.mp4"
	if target, err := cloudBrowserSTRMTarget([]model.MediaDirectory{config}, ancestors, collision); err != nil || target != preflight {
		t.Fatalf("same-stem collision was not exposed: target=%q err=%v", target, err)
	}
	if entries, err := os.ReadDir(config.SavePath); err != nil || len(entries) != 0 {
		t.Fatalf("preflight wrote files: %+v %v", entries, err)
	}
	result := svc.writeSTRM(context.Background(), []model.MediaDirectory{config}, ancestors, item)
	if result.Status != "created" || result.DirectoryID != config.ID || result.LocalPath != wantPath {
		t.Fatalf("result=%+v", result)
	}
	if data, err := os.ReadFile(wantPath); err != nil || string(data) != wantContent {
		t.Fatalf("content=%q err=%v", data, err)
	}
	result = svc.writeSTRM(context.Background(), []model.MediaDirectory{config}, ancestors, item)
	if result.Status != "unchanged" {
		t.Fatalf("idempotency=%+v", result)
	}
	config.ContentPrefix = "https://new.example/stream/"
	result = svc.writeSTRM(context.Background(), []model.MediaDirectory{config}, ancestors, item)
	if result.Status != "updated" {
		t.Fatalf("regeneration=%+v", result)
	}
	if data, err := os.ReadFile(wantPath); err != nil || !strings.HasPrefix(string(data), "https://new.example/stream/") {
		t.Fatalf("old STRM not replaced: %q %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(wantPath))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %+v %v", entries, err)
	}
}

func TestCloudBrowserSTRMUsesSpecificMappingAndGuardsManagedPaths(t *testing.T) {
	svc, config, ancestors, item := browserSTRMFixture(t)
	nested := config
	nested.ID, nested.DirectoryID, nested.DirectoryName, nested.SavePath = 6, "20", "Nested", t.TempDir()
	result := svc.writeSTRM(context.Background(), []model.MediaDirectory{config, nested}, ancestors, item)
	if result.Status != "created" || result.DirectoryID != nested.ID || result.LocalPath != filepath.Join(nested.SavePath, "Nested/Movie.strm") {
		t.Fatalf("nested mapping=%+v", result)
	}
	if entries, err := os.ReadDir(config.SavePath); err != nil || len(entries) != 0 {
		t.Fatalf("less specific mapping written: %+v %v", entries, err)
	}
	if err := svc.db.Create(&model.OrganizeManagedStrm{Path: result.LocalPath, UserID: 99, CloudStorageID: 99}).Error; err != nil {
		t.Fatal(err)
	}
	nested.ContentPrefix = "https://replacement.example"
	guarded := svc.writeSTRM(context.Background(), []model.MediaDirectory{config, nested}, ancestors, item)
	if guarded.Status != "skipped" || !strings.Contains(guarded.Message, "整理任务") {
		t.Fatalf("managed STRM overwritten: %+v", guarded)
	}
	if data, err := os.ReadFile(result.LocalPath); err != nil || strings.Contains(string(data), "replacement") {
		t.Fatalf("managed content changed: %q %v", data, err)
	}
}

func TestCloudBrowserSTRMFiltersAndIncompleteFiles(t *testing.T) {
	_, config, _, original := browserSTRMFixture(t)
	for _, test := range []struct {
		name, filename, include, exclude string
		minimum                          int
		size                             int64
		complete, allowed                bool
	}{
		{"case insensitive", "Film.MKV", `[".mkv"]`, "", 100, 100 << 20, true, true},
		{"exclude wins", "Film.mkv", `"mkv"`, ".mkv", 0, 1, true, false},
		{"below threshold", "Film.mkv", "mkv,mp4", "", 100, (100 << 20) - 1, true, false},
		{"unfinished", "Film.mkv", "", "", 0, 1, false, false},
		{"not included", "Film.avi", `["mkv"]`, "", 0, 1, true, false},
		{"subtitle", "Film.SRT", "", "", 0, 1, true, false},
		{"compressed subtitle", "Film.ass.zip", "", "", 0, 1, true, false},
		{"strm input", "Film.strm", "", "", 0, 1, true, false},
		{"invalid rules", "Film.mkv", `[7]`, "", 0, 1, true, false},
		{"all extensions", "Film", "", "", 0, 1, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			current, item := config, original
			current.IncludeExtensions, current.ExcludeExtensions, current.ExcludeSmallerThanMB = test.include, test.exclude, test.minimum
			item.Name, item.Size, item.Complete = test.filename, test.size, test.complete
			if reason := cloudBrowserSTRMUnavailable(&current, item); (reason == "") != test.allowed {
				t.Fatalf("allowed=%v reason=%q", test.allowed, reason)
			}
		})
	}
}

func TestCloudBrowserSTRMRejectsUnmatchedUnsafeAndWrongMembership(t *testing.T) {
	for _, mode := range []string{"no mapping", "wrong parent", "traversal filename", "unsafe alias", "unsafe ancestor", "relative root", "control prefix", "no database"} {
		t.Run(mode, func(t *testing.T) {
			svc, config, ancestors, item := browserSTRMFixture(t)
			originalRoot := config.SavePath
			switch mode {
			case "no mapping":
				config.DirectoryID = "99"
			case "wrong parent":
				item.ParentID = "99"
			case "traversal filename":
				item.Name = "../outside.mkv"
			case "unsafe alias":
				config.DirectoryName = "../outside"
			case "unsafe ancestor":
				ancestors[2].Name = "../outside"
			case "relative root":
				config.SavePath = "relative"
			case "control prefix":
				config.ContentPrefix += "\nsecond-line"
			case "no database":
				svc.db = nil
			}
			result := svc.writeSTRM(context.Background(), []model.MediaDirectory{config}, ancestors, item)
			if result.Status != "failed" && result.Status != "skipped" {
				t.Fatalf("invalid input wrote a file: %+v", result)
			}
			if entries, err := os.ReadDir(originalRoot); err != nil || len(entries) != 0 {
				t.Fatalf("unsafe file created: %+v %v", entries, err)
			}
		})
	}
}

func TestCloudBrowserSTRMRejectsSymlinksAndCancellation(t *testing.T) {
	for _, mode := range []string{"outside directory", "inside alias", "target symlink", "root symlink", "target directory"} {
		t.Run(mode, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			relative := "Films/Movie.strm"
			if err := os.WriteFile(filepath.Join(outside, "Movie.strm"), []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "outside directory":
				if err := os.Symlink(outside, filepath.Join(root, "Films")); err != nil {
					t.Fatal(err)
				}
			case "inside alias":
				if err := os.Mkdir(filepath.Join(root, "Elsewhere"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("Elsewhere", filepath.Join(root, "Films")); err != nil {
					t.Fatal(err)
				}
			case "target symlink", "target directory":
				if err := os.Mkdir(filepath.Join(root, "Films"), 0755); err != nil {
					t.Fatal(err)
				}
				var err error
				if mode == "target symlink" {
					err = os.Symlink(filepath.Join(outside, "Movie.strm"), filepath.Join(root, relative))
				} else {
					err = os.Mkdir(filepath.Join(root, relative), 0755)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "root symlink":
				link := filepath.Join(root, "linked-root")
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				root = link
			}
			if _, err := writeCloudBrowserSTRM(context.Background(), root, relative, "replace"); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if data, err := os.ReadFile(filepath.Join(outside, "Movie.strm")); err != nil || string(data) != "keep" {
				t.Fatalf("outside file changed: %q %v", data, err)
			}
		})
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writeCloudBrowserSTRM(ctx, root, "Movie.strm", "new"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write=%v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("cancelled write created files: %+v %v", entries, err)
	}
}
