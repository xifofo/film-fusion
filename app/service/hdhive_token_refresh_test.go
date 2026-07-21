package service

import (
	"film-fusion/app/config"
	"testing"
	"time"
)

func TestHDHiveTokenRefreshNeedsRefreshWithoutExpiresAt(t *testing.T) {
	svc := NewHDHiveTokenRefreshService(&config.Config{
		HDHive: config.HDHiveConfig{
			Enabled:      true,
			AutoRefresh:  true,
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
		},
	}, nil)

	if !svc.needsRefresh(time.Now()) {
		t.Fatalf("expected refresh when access token expiry is unknown")
	}
}

func TestHDHiveTokenRefreshNeedsRefreshBeforeExpiry(t *testing.T) {
	now := time.Now()
	svc := NewHDHiveTokenRefreshService(&config.Config{
		HDHive: config.HDHiveConfig{
			Enabled:              true,
			AutoRefresh:          true,
			AccessToken:          "access-token",
			RefreshToken:         "refresh-token",
			AccessTokenExpiresAt: now.Add(10 * time.Minute).Format(time.RFC3339),
			RefreshBeforeMinutes: 15,
		},
	}, nil)

	if !svc.needsRefresh(now) {
		t.Fatalf("expected refresh inside refresh-before window")
	}
}

func TestHDHiveTokenRefreshSkipsFreshToken(t *testing.T) {
	now := time.Now()
	svc := NewHDHiveTokenRefreshService(&config.Config{
		HDHive: config.HDHiveConfig{
			Enabled:              true,
			AutoRefresh:          true,
			AccessToken:          "access-token",
			RefreshToken:         "refresh-token",
			AccessTokenExpiresAt: now.Add(2 * time.Hour).Format(time.RFC3339),
			RefreshBeforeMinutes: 15,
		},
	}, nil)

	if svc.needsRefresh(now) {
		t.Fatalf("did not expect refresh for a fresh access token")
	}
}

func TestHDHiveTokenRefreshSkipsWhenDisabled(t *testing.T) {
	svc := NewHDHiveTokenRefreshService(&config.Config{
		HDHive: config.HDHiveConfig{
			Enabled:      true,
			AutoRefresh:  false,
			RefreshToken: "refresh-token",
		},
	}, nil)

	if svc.needsRefresh(time.Now()) {
		t.Fatalf("did not expect refresh when auto refresh is disabled")
	}
}
