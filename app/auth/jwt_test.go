package auth

import (
	"testing"
	"time"

	"film-fusion/app/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateTokenRequiresHS256AndConfiguredIssuer(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	cfg := &config.Config{JWT: config.JWTConfig{Secret: secret, ExpireTime: 1, Issuer: "film-fusion"}}
	service := NewJWTService(cfg)

	valid, err := service.GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if _, err := service.ValidateToken(valid); err != nil {
		t.Fatalf("ValidateToken(valid) error = %v", err)
	}

	claims := Claims{
		UserID: 1,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    cfg.JWT.Issuer,
		},
	}
	hs384 := jwt.NewWithClaims(jwt.SigningMethodHS384, claims)
	hs384Token, err := hs384.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign HS384 token: %v", err)
	}
	if _, err := service.ValidateToken(hs384Token); err == nil {
		t.Fatal("ValidateToken() accepted HS384 token")
	}

	claims.Issuer = "other-service"
	wrongIssuer := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	wrongIssuerToken, err := wrongIssuer.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign wrong-issuer token: %v", err)
	}
	if _, err := service.ValidateToken(wrongIssuerToken); err == nil {
		t.Fatal("ValidateToken() accepted token from another issuer")
	}
}
