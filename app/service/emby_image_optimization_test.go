package service

import (
	"film-fusion/app/config"
	"net/url"
	"testing"
)

func TestClassifyEmbyImageRequest(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		query   string
		profile string
	}{
		{"library cover", "/emby/Items/1/Images/Primary", "maxWidth=676&maxHeight=380", EmbyImageProfileLibraryCover},
		{"mobile library cover", "/Items/1/Images/Primary", "maxWidth=360&maxHeight=203", EmbyImageProfileLibraryCover},
		{"poster", "/Items/1/Images/Primary", "maxWidth=356&maxHeight=534", EmbyImageProfilePoster},
		{"mobile poster", "/Items/1/Images/Primary", "maxWidth=240&maxHeight=360", EmbyImageProfilePoster},
		{"list poster", "/Items/1/Images/Primary", "maxWidth=160&maxHeight=240", EmbyImageProfileListPoster},
		{"continue backdrop", "/Items/1/Images/Backdrop", "maxWidth=674", EmbyImageProfileContinueBackdrop},
		{"detail backdrop by size", "/Items/1/Images/Backdrop", "maxWidth=1920", EmbyImageProfileDetailBackdrop},
		{"detail backdrop by index", "/Items/1/Images/Backdrop/0", "maxWidth=674", EmbyImageProfileDetailBackdrop},
		{"detail logo", "/Items/1/Images/Logo", "maxHeight=152", EmbyImageProfileDetailLogo},
		{"thumb", "/Items/1/Images/Thumb", "maxWidth=800&maxHeight=800", EmbyImageProfileOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			profile, ok := ClassifyEmbyImageRequest(tt.path, query)
			if !ok || profile != tt.profile {
				t.Fatalf("profile=%q ok=%v, want %q", profile, ok, tt.profile)
			}
		})
	}
}

func TestApplyEmbyImageOptimizationOnlyClamps(t *testing.T) {
	settings := config.EmbyImageOptimizationConfig{
		Enabled: true,
		Poster: config.EmbyImageRuleConfig{
			Enabled: true, MaxWidth: 356, MaxHeight: 534, Quality: 80,
		},
	}

	query, _ := url.ParseQuery("maxWidth=500&maxHeight=750&quality=90&tag=abc")
	result := ApplyEmbyImageOptimization("/Items/1/Images/Primary", query, settings)
	if !result.Changed {
		t.Fatal("expected query to change")
	}
	if got := result.Query.Get("maxWidth"); got != "356" {
		t.Fatalf("maxWidth=%s", got)
	}
	if got := result.Query.Get("maxHeight"); got != "534" {
		t.Fatalf("maxHeight=%s", got)
	}
	if got := result.Query.Get("quality"); got != "80" {
		t.Fatalf("quality=%s", got)
	}
	if got := result.Query.Get("tag"); got != "abc" {
		t.Fatalf("tag=%s", got)
	}

	smaller, _ := url.ParseQuery("maxWidth=160&maxHeight=240&quality=70")
	unchanged := ApplyEmbyImageOptimization("/Items/1/Images/Primary", smaller, settings)
	if unchanged.Changed {
		t.Fatalf("smaller client request was changed: %s", unchanged.Query.Encode())
	}
}

func TestApplyEmbyImageOptimizationDisabled(t *testing.T) {
	query, _ := url.ParseQuery("maxWidth=676&maxHeight=380&quality=90")
	result := ApplyEmbyImageOptimization(
		"/Items/1/Images/Primary",
		query,
		config.EmbyImageOptimizationConfig{},
	)
	if result.Changed || result.Query.Get("quality") != "90" {
		t.Fatalf("disabled optimization changed query: %s", result.Query.Encode())
	}
}
