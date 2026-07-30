package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

const (
	jwtSecretFileName       = ".jwt-secret"
	generatedJWTSecretBytes = 48
	minJWTSecretBytes       = 32
)

var publicLegacyJWTSecrets = map[string]struct{}{
	"film-fusion-secret-key":               {},
	"your-jwt-secret-key":                  {},
	"your-secret-key-change-in-production": {},
}

// applyJWTSecret keeps JWT signing as an internal implementation detail. The
// secret is persisted beside config.yaml so Docker's /app/data volume retains
// login sessions across restarts.
func applyJWTSecret(settings *JWTConfig) {
	path := jwtSecretPath()
	secret, err := loadOrCreateJWTSecret(path, settings.Secret)
	if err == nil {
		settings.Secret = secret
		return
	}

	// A read-only or temporarily unavailable data directory must not prevent the
	// application from starting. The fallback remains cryptographically random,
	// but sessions will need to log in again after the next restart.
	secret, generateErr := generateJWTSecret()
	if generateErr != nil {
		log.Fatalf("初始化登录会话签名失败: %v", generateErr)
	}
	settings.Secret = secret
	log.Printf("警告: 无法持久化内部登录会话签名到 %s: %v；本次运行将使用临时签名，重启后需重新登录", path, err)
}

func jwtSecretPath() string {
	if configFile := viper.ConfigFileUsed(); configFile != "" {
		return filepath.Join(filepath.Dir(configFile), jwtSecretFileName)
	}
	return filepath.Join("data", jwtSecretFileName)
}

func loadOrCreateJWTSecret(path, legacySecret string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		secret := strings.TrimSpace(string(data))
		if len([]byte(secret)) >= minJWTSecretBytes {
			return secret, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("读取签名文件: %w", err)
	}

	secret := strings.TrimSpace(legacySecret)
	if !usableLegacyJWTSecret(secret) {
		var err error
		secret, err = generateJWTSecret()
		if err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("创建签名目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("写入签名文件: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("设置签名文件权限: %w", err)
	}
	return secret, nil
}

func usableLegacyJWTSecret(secret string) bool {
	if len([]byte(secret)) < minJWTSecretBytes {
		return false
	}
	_, public := publicLegacyJWTSecrets[secret]
	return !public
}

func generateJWTSecret() (string, error) {
	randomBytes := make([]byte, generatedJWTSecretBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("生成随机签名: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
