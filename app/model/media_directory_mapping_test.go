package model

import (
	"net/url"
	"path/filepath"
	"testing"
)

func TestMappingRuntimeConfigPreservesLegacyCoordinates(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		for _, mount := range []string{"115LongTerm", "挂载 #1"} {
			base := t.TempDir()
			contentMount := mount
			if encoded {
				contentMount = url.PathEscape(mount)
			}
			d := MediaDirectory{MappingEnabled: true, OrganizeEnabled: true, SourceType: SourceTypeCloudDrive2, SourcePath: "/" + mount + "/影视中心", MappingPathPrefix: "/" + mount, SavePath: filepath.Join(base, mount), ContentPrefix: "https://play.invalid/root/" + contentMount, ContentEncodeURI: encoded, FilterRules: `{"include":["mkv"],"download":["srt"]}`, EmbyPathPrefix: "/emby"}
			runtime, err := d.MappingRuntimeConfig()
			if err != nil || runtime.SavePath != base || runtime.ContentPrefix != "https://play.invalid/root" || runtime.SourcePath != d.SourcePath || runtime.FilterRules != d.FilterRules || runtime.EmbyPathPrefix != d.EmbyPathPrefix {
				t.Fatalf("runtime=%+v err=%v", runtime, err)
			}
			again, err := runtime.MappingRuntimeConfig()
			if err != nil || again.SavePath != runtime.SavePath || again.ContentPrefix != runtime.ContentPrefix {
				t.Fatalf("conversion not idempotent: %+v %v", again, err)
			}
			if d.MappingPathPrefix == "" || d.SavePath != filepath.Join(base, mount) {
				t.Fatal("stored configuration mutated")
			}
		}
	}
}

func TestMappingRuntimeConfigRejectsUnsafeOrIncompatiblePrefixes(t *testing.T) {
	for _, prefix := range []string{"/", "/other", "/mount/..", "mount", "/mount/extra"} {
		d := MediaDirectory{MappingEnabled: true, OrganizeEnabled: true, SourceType: SourceTypeCloudDrive2, SourcePath: "/mount/Movies", MappingPathPrefix: prefix, SavePath: "/strm/mount", ContentPrefix: "/play/mount"}
		if _, err := d.MappingRuntimeConfig(); err == nil {
			t.Fatalf("accepted %q", prefix)
		}
	}
	d := MediaDirectory{SavePath: "/old", ContentPrefix: "/legacy"}
	if got, err := d.MappingRuntimeConfig(); err != nil || got.SavePath != d.SavePath || got.ContentPrefix != d.ContentPrefix {
		t.Fatal("legacy configuration changed")
	}
}
