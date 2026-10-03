package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"film-fusion/app/auth"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const APITokenPrefix = "ffapi_"

var ErrInvalidAPIToken = errors.New("invalid API token")

type APITokenValidationError struct{ Message string }

func (e *APITokenValidationError) Error() string { return e.Message }

type APITokenService struct{ db *gorm.DB }

func NewAPITokenService(db *gorm.DB) *APITokenService { return &APITokenService{db: db} }

func apiTokenHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *APITokenService) Create(ctx context.Context, userID uint, name string, scopes []string, expiresAt *time.Time) (*model.APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return nil, "", &APITokenValidationError{"名称须为 1–80 个字符"}
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return nil, "", &APITokenValidationError{"到期时间必须晚于当前时间"}
	}
	known := make(map[string]bool)
	for _, scope := range auth.APITokenScopes() {
		known[scope.ID] = true
	}
	unique := make([]string, 0, len(scopes))
	seen := make(map[string]bool)
	for _, scope := range scopes {
		if !known[scope] {
			return nil, "", &APITokenValidationError{"包含未知的 API 模块"}
		}
		if !seen[scope] {
			unique = append(unique, scope)
			seen[scope] = true
		}
	}
	if len(unique) == 0 {
		return nil, "", &APITokenValidationError{"请至少选择一个 API 模块"}
	}
	var owner model.User
	if err := s.db.WithContext(ctx).Where("is_active = ? AND is_admin = ?", true, true).First(&owner, userID).Error; err != nil {
		return nil, "", fmt.Errorf("verify token owner: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", err
	}
	raw := APITokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	if expiresAt != nil {
		utc := expiresAt.UTC()
		expiresAt = &utc
	}
	token := &model.APIToken{
		UserID: userID, Name: name, Scopes: unique, ExpiresAt: expiresAt,
		Prefix: raw[:14], TokenHash: apiTokenHash(raw),
	}
	if err := s.db.WithContext(ctx).Create(token).Error; err != nil {
		return nil, "", err
	}
	return token, raw, nil
}

func (s *APITokenService) List(ctx context.Context, userID uint) ([]model.APIToken, error) {
	tokens := make([]model.APIToken, 0)
	err := s.db.WithContext(ctx).Omit("token_hash").Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&tokens).Error
	return tokens, err
}

// Revoke is idempotent and owner-scoped; revoked credentials can never be restored.
func (s *APITokenService) Revoke(ctx context.Context, userID, id uint) error {
	var token model.APIToken
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&token, id).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&model.APIToken{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", time.Now().UTC()).Error
}

func (s *APITokenService) Authenticate(ctx context.Context, raw string) (*model.APIToken, *model.User, error) {
	if len(raw) != len(APITokenPrefix)+43 || !strings.HasPrefix(raw, APITokenPrefix) {
		return nil, nil, ErrInvalidAPIToken
	}
	var token model.APIToken
	err := s.db.WithContext(ctx).Where("token_hash = ? AND revoked_at IS NULL", apiTokenHash(raw)).First(&token).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInvalidAPIToken
		}
		return nil, nil, err
	}
	// Compare instants in Go: SQLite textual datetime ordering depends on the
	// stored timezone offset and must not decide credential expiry.
	if token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now()) {
		return nil, nil, ErrInvalidAPIToken
	}
	var user model.User
	err = s.db.WithContext(ctx).Where("is_active = ? AND is_admin = ?", true, true).First(&user, token.UserID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInvalidAPIToken
		}
		return nil, nil, err
	}
	return &token, &user, nil
}

// Touch writes at most once per minute to avoid serializing every API request on SQLite.
func (s *APITokenService) Touch(ctx context.Context, token *model.APIToken) {
	now := time.Now().UTC()
	if token.LastUsedAt != nil && now.Sub(*token.LastUsedAt) < time.Minute {
		return
	}
	s.db.WithContext(ctx).Model(&model.APIToken{}).
		Where("id = ? AND revoked_at IS NULL AND (last_used_at IS NULL OR last_used_at < ?)", token.ID, now.Add(-time.Minute)).
		Update("last_used_at", now)
}
