package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestPlutoFishConfigAllowsSeveralCloudPathsPerAccount(t *testing.T) {
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "config.db"))
	for _, mapping := range []model.CloudPath{
		{ID: 1, UserID: 7, SourcePath: "/Movies"},
		{ID: 2, UserID: 7, SourcePath: "/TV"},
		{ID: 3, UserID: 8, SourcePath: "/Other"},
	} {
		if err := db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewPlutoFishService(db)
	cfg := PlutoFishConfig{BaseURL: "https://plutofish.example", OwnerUID: "owner", APIKey: "offline-test", Mode: "sse", PollSeconds: 15,
		Mappings: []PlutoFishAccountMapping{{TargetAccountID: "account", CloudPathID: 1}, {TargetAccountID: "account", CloudPathID: 2}}}
	ctx := context.Background()
	if err := svc.Save(ctx, cfg, 7); err != nil {
		t.Fatalf("second path rejected: %v", err)
	}
	loaded, err := svc.Config(ctx)
	if err != nil || !reflect.DeepEqual(loaded.Mappings, cfg.Mappings) {
		t.Fatalf("mapping round trip: %+v, %v", loaded.Mappings, err)
	}
	for _, bad := range []PlutoFishAccountMapping{
		{TargetAccountID: "account", CloudPathID: 1},
		{TargetAccountID: "account", CloudPathID: 3},
		{TargetAccountID: "account", CloudPathID: 999},
	} {
		invalid := cfg
		invalid.Mappings = append(append([]PlutoFishAccountMapping{}, cfg.Mappings...), bad)
		if err := svc.Save(ctx, invalid, 7); err == nil {
			t.Fatalf("invalid mapping accepted: %+v", bad)
		}
		unchanged, err := svc.Config(ctx)
		if err != nil || !reflect.DeepEqual(unchanged.Mappings, cfg.Mappings) {
			t.Fatal("invalid save replaced existing mappings", err)
		}
	}
}

func TestPlutoFishMultipleMappingsRouteByDirectory(t *testing.T) {
	for _, cd2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "MP2", true: "CD2"}[cd2], func(t *testing.T) {
			b, movies, event := plutoFishBusinessFixture(t)
			tv := movies
			tv.ID, tv.SourcePath, tv.LocalPath = 3, "/TV", t.TempDir()
			mount := ""
			if cd2 {
				movies.SourceType, movies.SourcePath = model.SourceTypeCloudDrive2, "/115Media/Movies"
				tv.SourceType, tv.SourcePath = model.SourceTypeCloudDrive2, "/115Media/TV"
				mount = "115Media/"
				if err := b.db.Save(&movies).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := b.db.Create(&tv).Error; err != nil {
				t.Fatal(err)
			}
			b.options.Mappings = append(b.options.Mappings, PlutoFishAccountMapping{TargetAccountID: "account", CloudPathID: tv.ID})
			for _, entry := range []struct{ directory, root, task string }{{"/Movies/Title", movies.LocalPath, "movie"}, {"/TV/Show/Season 01", tv.LocalPath, "tv"}} {
				event.TargetDirectoryPath, event.TransferID, event.FileName = entry.directory, entry.task, "Episode.mkv"
				result, err := b.Handle(context.Background(), "scope", event)
				wantPath := filepath.Join(entry.root, mount, entry.directory, "Episode.strm")
				if err != nil || result.LocalPath != wantPath {
					t.Fatalf("directory=%s result=%+v want=%s err=%v", entry.directory, result, wantPath, err)
				}
				content, err := os.ReadFile(result.LocalPath)
				wantContent := "https://existing-player.example/play/" + mount + strings.ReplaceAll(strings.TrimPrefix(entry.directory, "/"), " ", "%20") + "/Episode.mkv"
				if err != nil || string(content) != wantContent {
					t.Fatalf("content=%q want=%q err=%v", content, wantContent, err)
				}
			}
			event.TargetDirectoryPath, event.TransferID = "/Movies2", "outside"
			if _, err := b.Handle(context.Background(), "scope", event); err == nil {
				t.Fatal("matched directory without segment boundary")
			}
			event.TargetDirectoryPath, event.TargetAccountID = "/Movies/Title", "another-account"
			if _, err := b.Handle(context.Background(), "scope", event); err == nil {
				t.Fatal("used another account's mapping")
			}
		})
	}
}

func TestPlutoFishMultipleMappingsPreferSpecificPathAndDoNotFallBack(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		b, broad, event := plutoFishBusinessFixture(t)
		specific := broad
		specific.ID, specific.SourcePath, specific.LocalPath = 3, "/Movies/Title", t.TempDir()
		if err := b.db.Create(&specific).Error; err != nil {
			t.Fatal(err)
		}
		b.options.Mappings = append(b.options.Mappings, PlutoFishAccountMapping{TargetAccountID: "account", CloudPathID: specific.ID})
		if reverse {
			b.options.Mappings[0], b.options.Mappings[1] = b.options.Mappings[1], b.options.Mappings[0]
		}
		plan, err := b.plan(context.Background(), "scope", event)
		if err != nil || plan.RootPath != specific.LocalPath {
			t.Fatalf("specific mapping not used: %+v %v", plan, err)
		}
		specific.FilterRules = `{"include":["mp4"]}`
		if err := b.db.Save(&specific).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := b.Handle(context.Background(), "scope", event); err == nil {
			t.Fatal("excluded file fell back to broader mapping")
		}
		if entries, err := os.ReadDir(broad.LocalPath); err != nil || len(entries) != 0 {
			t.Fatal("unexpected broad output", entries, err)
		}
	}
}

func TestPlutoFishMultipleMappingsRejectEqualEffectiveSources(t *testing.T) {
	b, first, event := plutoFishBusinessFixture(t)
	second := first
	second.ID, second.SourceType, second.SourcePath, second.LocalPath = 3, model.SourceTypeCloudDrive2, "/AnotherMount/Movies/", t.TempDir()
	if err := b.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	b.options.Mappings = append(b.options.Mappings, PlutoFishAccountMapping{TargetAccountID: "account", CloudPathID: second.ID})
	if _, err := b.Handle(context.Background(), "scope", event); err == nil || !strings.Contains(err.Error(), "同等优先级") {
		t.Fatalf("ambiguous mapping accepted: %v", err)
	}
	for _, root := range []string{first.LocalPath, second.LocalPath} {
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatal("ambiguous event wrote a file", entries, err)
		}
	}
}
