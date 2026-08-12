package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const rssGeneratorSecretEnvelope = "v1:"

type rssGeneratorCipher struct {
	aead cipher.AEAD
}

func newRSSGeneratorCipher(keyFile string) (*rssGeneratorCipher, error) {
	key, err := loadRSSGeneratorEncryptionKey(keyFile)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 RSS Generator AES 密钥失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 RSS Generator GCM 失败: %w", err)
	}
	return &rssGeneratorCipher{aead: aead}, nil
}

func loadRSSGeneratorEncryptionKey(keyFile string) ([]byte, error) {
	if encoded := strings.TrimSpace(os.Getenv("RSS_GENERATOR_ENCRYPTION_KEY")); encoded != "" {
		key, err := decodeRSSGeneratorKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("RSS_GENERATOR_ENCRYPTION_KEY 无效: %w", err)
		}
		return key, nil
	}
	if strings.TrimSpace(keyFile) == "" {
		keyFile = "data/rss-generator.key"
	}
	if content, err := os.ReadFile(keyFile); err == nil {
		key, decodeErr := decodeRSSGeneratorKey(strings.TrimSpace(string(content)))
		if decodeErr != nil {
			return nil, fmt.Errorf("RSS Generator 密钥文件无效: %w", decodeErr)
		}
		_ = os.Chmod(keyFile, 0600)
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 RSS Generator 密钥失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyFile), 0700); err != nil {
		return nil, fmt.Errorf("创建 RSS Generator 密钥目录失败: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成 RSS Generator 密钥失败: %w", err)
	}
	encoded := base64.RawStdEncoding.EncodeToString(key) + "\n"
	file, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		content, readErr := os.ReadFile(keyFile)
		if readErr != nil {
			return nil, fmt.Errorf("并发读取 RSS Generator 密钥失败: %w", readErr)
		}
		return decodeRSSGeneratorKey(strings.TrimSpace(string(content)))
	}
	if err != nil {
		return nil, fmt.Errorf("创建 RSS Generator 密钥失败: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(encoded); err != nil {
		return nil, fmt.Errorf("写入 RSS Generator 密钥失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("同步 RSS Generator 密钥失败: %w", err)
	}
	return key, nil
}

func decodeRSSGeneratorKey(encoded string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		hex.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	}
	for _, decode := range decoders {
		if key, err := decode(encoded); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("密钥必须是 base64 或 hex 编码的 32 字节值")
}

func (c *rssGeneratorCipher) encrypt(field, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte("rss-generator:"+field))
	return rssGeneratorSecretEnvelope + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *rssGeneratorCipher) decrypt(field, envelope string) (string, error) {
	if envelope == "" {
		return "", nil
	}
	if !strings.HasPrefix(envelope, rssGeneratorSecretEnvelope) {
		return "", errors.New("不支持的 RSS Generator 密文版本")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(envelope, rssGeneratorSecretEnvelope))
	if err != nil || len(raw) < c.aead.NonceSize() {
		return "", errors.New("RSS Generator 密文损坏")
	}
	nonce, ciphertext := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ciphertext, []byte("rss-generator:"+field))
	if err != nil {
		return "", errors.New("RSS Generator 密文认证失败")
	}
	return string(plain), nil
}
