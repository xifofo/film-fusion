package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
)

func testEmbySecurityConfig() *config.Config {
	return &config.Config{Emby: config.EmbyConfig{Security: config.EmbySecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 2,
		MaxFailuresPerIP: 4, BlockMinutes: 30,
	}}}
}

func loginRequest(remoteAddr, forwardedFor string) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://emby.test/emby/Users/AuthenticateByName", strings.NewReader(`{"Username":"Alice","Pw":"secret"}`))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return req
}

func TestEmbyLoginProtectionBlocksAndUnblocksWithoutReadingPassword(t *testing.T) {
	p := NewEmbyLoginProtection(testEmbySecurityConfig(), nil)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		req := loginRequest("198.51.100.8:4567", "203.0.113.99")
		attempt, blocked := p.Inspect(req)
		if blocked || attempt == nil {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
		if attempt.IP != "198.51.100.8" || attempt.Username != "Alice" {
			t.Fatalf("unexpected attempt: %#v", attempt)
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"Pw":"secret"`) {
			t.Fatal("Inspect did not restore request body")
		}
		p.ObserveResponse(attempt, http.StatusUnauthorized)
	}

	attempt, blocked := p.Inspect(loginRequest("198.51.100.8:4567", "203.0.113.99"))
	if !blocked || attempt == nil {
		t.Fatal("expected account/IP pair to be blocked")
	}
	snapshot := p.Snapshot()
	if snapshot.BlockedCount != 1 || snapshot.Blocks[0].Scope != "account_ip" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if !p.Unblock("account_ip", attempt.IP, attempt.Username) {
		t.Fatal("expected unblock to remove the block")
	}
	if _, blocked := p.Inspect(loginRequest("198.51.100.8:4567", "")); blocked {
		t.Fatal("request remained blocked after manual unblock")
	}
}

func TestEmbyLoginProtectionOnlyTrustsConfiguredProxy(t *testing.T) {
	cfg := testEmbySecurityConfig()
	p := NewEmbyLoginProtection(cfg, nil)

	attempt, _ := p.Inspect(loginRequest("198.51.100.8:4567", "203.0.113.9"))
	if attempt.IP != "198.51.100.8" {
		t.Fatalf("untrusted peer spoofed IP: %s", attempt.IP)
	}

	cfg.Emby.Security.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	attempt, _ = p.Inspect(loginRequest("10.0.0.2:4567", "203.0.113.9, 10.0.0.3"))
	if attempt.IP != "203.0.113.9" {
		t.Fatalf("trusted proxy client IP = %s", attempt.IP)
	}
}

func TestEmbyLoginProtectionSuccessClearsPairFailures(t *testing.T) {
	p := NewEmbyLoginProtection(testEmbySecurityConfig(), nil)
	req := loginRequest("198.51.100.9:4567", "")
	attempt, _ := p.Inspect(req)
	p.ObserveResponse(attempt, http.StatusUnauthorized)
	p.ObserveResponse(attempt, http.StatusOK)
	p.ObserveResponse(attempt, http.StatusUnauthorized)

	if _, blocked := p.Inspect(loginRequest("198.51.100.9:4567", "")); blocked {
		t.Fatal("successful login did not clear pair failures")
	}
}
