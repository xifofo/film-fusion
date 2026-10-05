package organize

import (
	"film-fusion/app/service"
	"testing"
)

func TestRefreshOrganize115ShadowStatus(t *testing.T) {
	t.Run("all stages matched", func(t *testing.T) {
		comparison := &Organize115ShadowComparison{
			Recognition: &service.MediaRecognitionShadowComparison{Status: service.MediaRecognitionShadowMatched, Matched: true},
			Transfer:    &service.MediaTransferShadowComparison{Status: service.MediaRecognitionShadowMatched, Matched: true},
			Differences: []Organize115ShadowDifference{},
		}
		refreshOrganize115ShadowStatus(comparison)
		if !comparison.Matched || comparison.Status != service.MediaRecognitionShadowMatched {
			t.Fatalf("comparison=%+v", comparison)
		}
	})

	t.Run("target path differs", func(t *testing.T) {
		comparison := &Organize115ShadowComparison{
			Recognition: &service.MediaRecognitionShadowComparison{Status: service.MediaRecognitionShadowMatched, Matched: true},
			Transfer:    &service.MediaTransferShadowComparison{Status: service.MediaRecognitionShadowMatched, Matched: true},
			Differences: []Organize115ShadowDifference{{Field: "target_path"}},
		}
		refreshOrganize115ShadowStatus(comparison)
		if comparison.Matched || comparison.Status != service.MediaRecognitionShadowDifferent {
			t.Fatalf("comparison=%+v", comparison)
		}
	})

	t.Run("local error wins over differences", func(t *testing.T) {
		comparison := &Organize115ShadowComparison{
			Recognition: &service.MediaRecognitionShadowComparison{Status: service.MediaRecognitionShadowLocalError},
			Differences: []Organize115ShadowDifference{{Field: "tmdb_id"}},
		}
		refreshOrganize115ShadowStatus(comparison)
		if comparison.Matched || comparison.Status != service.MediaRecognitionShadowLocalError {
			t.Fatalf("comparison=%+v", comparison)
		}
	})
}

func TestBuildOrganize115ShadowVariableComparisonIncludesAllVariables(t *testing.T) {
	primary := service.MediaRenameInput{
		Info:      service.MoviePilotMediaInfo{MediaType: "movie", Title: "示例", Year: "2026", TmdbID: "123"},
		MediaType: "movie", OriginalName: "Example.2026.NF.WEB-DL.mkv",
		RecognitionRaw: map[string]any{"meta_info": map[string]any{"web_source": "Netflix"}},
	}
	local := primary
	local.RecognitionRaw = map[string]any{"meta_info": map[string]any{}}
	rows := buildOrganize115ShadowVariableComparison(primary, local)
	if len(rows) != len(service.MediaRenameCommonVariables()) {
		t.Fatalf("rows=%d want=%d", len(rows), len(service.MediaRenameCommonVariables()))
	}
	foundWebSource := false
	foundEmpty := false
	for _, row := range rows {
		switch row.Name {
		case "webSource":
			foundWebSource = row.MoviePilot == "Netflix" && row.Local == "" && !row.Matched
		case "customization":
			foundEmpty = row.MoviePilot == "" && row.Local == "" && row.Matched
		}
	}
	if !foundWebSource || !foundEmpty {
		t.Fatalf("webSource or empty variable row missing: %+v", rows)
	}
}
