package config

import (
	"strings"
	"testing"
)

func TestValidateJWTSecret(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{name: "empty", secret: "", wantErr: true},
		{name: "public runtime example", secret: "film-fusion-secret-key", wantErr: true},
		{name: "public code default", secret: "your-secret-key-change-in-production", wantErr: true},
		{name: "documentation placeholder", secret: "your-jwt-secret-key", wantErr: true},
		{name: "too short", secret: strings.Repeat("x", minJWTSecretBytes-1), wantErr: true},
		{name: "minimum length", secret: strings.Repeat("x", minJWTSecretBytes), wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJWTSecret(tt.secret)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateJWTSecret() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
