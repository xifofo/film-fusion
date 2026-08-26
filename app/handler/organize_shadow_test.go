package handler

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
