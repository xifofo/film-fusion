package service

import (
	"errors"
	"reflect"
	"testing"

	"film-fusion/app/model"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestResolveMatch302DownloadInfoCookieOnlySkipsOpenAPI(t *testing.T) {
	var calls []string
	info, err := resolveMatch302DownloadInfo(
		model.Match302AccessModeCookieOnly,
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodOpenAPI)
			return downloadInfoForTest("open-url"), nil
		},
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodCookie)
			return downloadInfoForTest("cookie-url"), nil
		},
	)
	if err != nil {
		t.Fatalf("resolveMatch302DownloadInfo returned error: %v", err)
	}
	if info.Url.Url != "cookie-url" || !reflect.DeepEqual(calls, []string{model.Match302AccessMethodCookie}) {
		t.Fatalf("url=%q calls=%v", info.Url.Url, calls)
	}
}

func TestResolveMatch302DownloadInfoAutoFallsBackToCookie(t *testing.T) {
	var calls []string
	info, err := resolveMatch302DownloadInfo(
		model.Match302AccessModeAuto,
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodOpenAPI)
			return nil, errors.New("OpenAPI unavailable")
		},
		func() (*driver.DownloadInfo, error) {
			calls = append(calls, model.Match302AccessMethodCookie)
			return downloadInfoForTest("cookie-url"), nil
		},
	)
	if err != nil {
		t.Fatalf("resolveMatch302DownloadInfo returned error: %v", err)
	}
	if info.Url.Url != "cookie-url" || !reflect.DeepEqual(calls, []string{
		model.Match302AccessMethodOpenAPI,
		model.Match302AccessMethodCookie,
	}) {
		t.Fatalf("url=%q calls=%v", info.Url.Url, calls)
	}
}

func downloadInfoForTest(rawURL string) *driver.DownloadInfo {
	return &driver.DownloadInfo{Url: driver.FileDownloadUrl{Url: rawURL}}
}
