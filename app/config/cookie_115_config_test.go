package config

import "testing"

func TestCookie115DefaultApp(t *testing.T) {
	if got := NormalizeCookie115App(" "); got != DefaultCookie115App {
		t.Fatalf("NormalizeCookie115App(empty) = %q, want %q", got, DefaultCookie115App)
	}
	if got := NormalizeCookie115App(" TV "); got != "tv" {
		t.Fatalf("NormalizeCookie115App(TV) = %q, want tv", got)
	}

	for _, app := range []string{"web", "android", "ios", "tv", "alipaymini", "wechatmini", "qandroid"} {
		if err := ValidateCookie115App(app); err != nil {
			t.Fatalf("ValidateCookie115App(%q) error = %v", app, err)
		}
	}
	if err := ValidateCookie115App("desktop"); err == nil {
		t.Fatal("ValidateCookie115App(desktop) unexpectedly succeeded")
	}
}
