package service

import (
	"reflect"
	"testing"

	"film-fusion/app/utils/embyhelper"
)

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
