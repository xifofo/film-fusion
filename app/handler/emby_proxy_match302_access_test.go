package handler

import (
	"errors"
	"reflect"
	"testing"

	"film-fusion/app/model"
)

func TestResolveMatch302DownloadURLHonorsStrictModes(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "OpenAPI only", mode: model.Match302AccessModeOpenAPIOnly, want: "open-url"},
		{name: "Cookie only", mode: model.Match302AccessModeCookieOnly, want: "cookie-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			got, err := resolveMatch302DownloadURL(
				tt.mode,
				func() (string, error) {
					calls = append(calls, model.Match302AccessMethodOpenAPI)
					return "open-url", nil
				},
				func() (string, error) {
					calls = append(calls, model.Match302AccessMethodCookie)
					return "cookie-url", nil
				},
			)
			if err != nil {
				t.Fatalf("resolveMatch302DownloadURL returned error: %v", err)
			}
			if got != tt.want || len(calls) != 1 {
				t.Fatalf("url=%q calls=%v", got, calls)
			}
		})
	}
}

func TestResolveMatch302DownloadURLAutoFallsBackToCookie(t *testing.T) {
	var calls []string
	got, err := resolveMatch302DownloadURL(
		model.Match302AccessModeAuto,
		func() (string, error) {
			calls = append(calls, model.Match302AccessMethodOpenAPI)
			return "", errors.New("OpenAPI unavailable")
		},
		func() (string, error) {
			calls = append(calls, model.Match302AccessMethodCookie)
			return "cookie-url", nil
		},
	)
	if err != nil {
		t.Fatalf("resolveMatch302DownloadURL returned error: %v", err)
	}
	if got != "cookie-url" || !reflect.DeepEqual(calls, []string{
		model.Match302AccessMethodOpenAPI,
		model.Match302AccessMethodCookie,
	}) {
		t.Fatalf("url=%q calls=%v", got, calls)
	}
}
