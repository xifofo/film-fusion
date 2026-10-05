package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/utils/embyhelper"
)

func TestEmbyStatsCancellationReachesLibraryAndCountRequests(t *testing.T) {
	for _, blockList := range []bool{true, false} {
		t.Run(map[bool]string{true: "libraries", false: "counts"}[blockList], func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !blockList && r.URL.Path == "/Library/MediaFolders" {
					_, _ = w.Write([]byte(`{"Items":[{"Id":"movies","Name":"Movies","CollectionType":"movies"}]}`))
					return
				}
				entered <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			cfg := &config.Config{Emby: config.EmbyConfig{URL: server.URL}}
			client := embyhelper.New(cfg)
			defer client.Close()
			log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
			defer log.Close()
			svc := NewEmbyStatsService(cfg, log, client)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				stats, err := svc.Collect(ctx)
				if blockList && err == nil {
					t.Error("cancelled library listing succeeded")
				}
				if !blockList && (err != nil || stats == nil || len(stats.PartialErrors) != 1) {
					t.Errorf("count failure lost partial-result contract: %+v, %v", stats, err)
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("Emby request did not start")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Emby stats request ignored caller cancellation")
			}
		})
	}
}

func TestEmbyStatsItemTypesFollowCollectionType(t *testing.T) {
	tests := []struct {
		name           string
		collectionType string
		want           []string
	}{
		{name: "movies", collectionType: "movies", want: []string{"Movie"}},
		{name: "tv shows", collectionType: "tvshows", want: []string{"Series"}},
		{name: "mixed", collectionType: "mixed", want: []string{"Movie", "Series"}},
		{name: "legacy empty", collectionType: "", want: []string{"Movie", "Series"}},
		{name: "box sets", collectionType: "boxsets", want: []string{"BoxSet"}},
		{name: "music", collectionType: "music", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embyStatsItemTypes(tt.collectionType); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("embyStatsItemTypes(%q) = %#v, want %#v", tt.collectionType, got, tt.want)
			}
		})
	}
}

func TestEmbyStatsItemTypeLabel(t *testing.T) {
	tests := map[string]string{
		"Movie":  "电影",
		"Series": "剧集",
		"BoxSet": "合集",
	}

	for itemType, want := range tests {
		if got := embyStatsItemTypeLabel(itemType); got != want {
			t.Fatalf("embyStatsItemTypeLabel(%q) = %q, want %q", itemType, got, want)
		}
	}
}

func TestEmbyLibraryImageMetaPrefersPrimary(t *testing.T) {
	lib := embyhelper.EmbyLibrary{
		ImageTags:         map[string]string{"Primary": "primary-tag"},
		BackdropImageTags: []string{"backdrop-tag"},
	}

	imageType, imageTag := embyLibraryImageMeta(lib)
	if imageType != "Primary" || imageTag != "primary-tag" {
		t.Fatalf("embyLibraryImageMeta() = (%q, %q), want Primary primary-tag", imageType, imageTag)
	}
}

func TestEmbyLibraryImageMetaFallsBackToBackdrop(t *testing.T) {
	lib := embyhelper.EmbyLibrary{BackdropImageTags: []string{"backdrop-tag"}}

	imageType, imageTag := embyLibraryImageMeta(lib)
	if imageType != "Backdrop" || imageTag != "backdrop-tag" {
		t.Fatalf("embyLibraryImageMeta() = (%q, %q), want Backdrop backdrop-tag", imageType, imageTag)
	}
}
