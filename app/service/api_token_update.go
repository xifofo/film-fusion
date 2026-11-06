// API Token 元数据更新按所有者和未撤销状态执行局部写入。
package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"film-fusion/app/auth"
	"film-fusion/app/model"
)

// ErrAPITokenRevoked 表示记录已撤销，更新元数据不能重新启用凭据。
var ErrAPITokenRevoked = errors.New("API Token 已撤销，不能编辑")

// APITokenUpdate 只修改显式提供的字段；SetExpiresAt 区分保留时间和清除时间。
type APITokenUpdate struct {
	Name   *string
	Scopes *[]string

	SetExpiresAt bool
	ExpiresAt    *time.Time
}

// Update 按所有者更新元数据，不生成或返回秘密；撤销与更新竞争时始终保留撤销状态。
func (s *APITokenService) Update(ctx context.Context, userID, id uint, input APITokenUpdate) (*model.APIToken, error) {
	var token model.APIToken
	if err := s.db.WithContext(ctx).Omit("token_hash").Where("user_id = ?", userID).First(&token, id).Error; err != nil {
		return nil, err
	}
	if token.RevokedAt != nil {
		return nil, ErrAPITokenRevoked
	}

	fields := make([]string, 0, 3)
	if input.Name != nil {
		token.Name = strings.TrimSpace(*input.Name)
		if token.Name == "" || utf8.RuneCountInString(token.Name) > 80 {
			return nil, &APITokenValidationError{"名称须为 1–80 个字符"}
		}
		fields = append(fields, "name")
	}
	if input.Scopes != nil {
		known := make(map[string]bool)
		for _, scope := range auth.APITokenScopes() {
			known[scope.ID] = true
		}
		unique := make([]string, 0, len(*input.Scopes))
		seen := make(map[string]bool)
		for _, scope := range *input.Scopes {
			if !known[scope] {
				return nil, &APITokenValidationError{"包含未知的 API 模块"}
			}
			if !seen[scope] {
				unique = append(unique, scope)
				seen[scope] = true
			}
		}
		if len(unique) == 0 {
			return nil, &APITokenValidationError{"请至少选择一个 API 模块"}
		}
		token.Scopes = unique
		fields = append(fields, "scopes")
	}
	if input.SetExpiresAt {
		token.ExpiresAt = nil
		if input.ExpiresAt != nil {
			if !input.ExpiresAt.After(time.Now()) {
				return nil, &APITokenValidationError{"到期时间必须晚于当前时间"}
			}
			utc := input.ExpiresAt.UTC()
			token.ExpiresAt = &utc
		}
		fields = append(fields, "expires_at")
	}
	if len(fields) == 0 {
		return nil, &APITokenValidationError{"请提供需要修改的字段"}
	}

	// 仅写入提供的字段；名称编辑不会覆盖其他会话修改的权限、到期或最近使用时间。
	result := s.db.WithContext(ctx).Model(&model.APIToken{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Select(fields).Updates(&token)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrAPITokenRevoked
	}
	if err := s.db.WithContext(ctx).Omit("token_hash").Where("user_id = ?", userID).First(&token, id).Error; err != nil {
		return nil, err
	}

	return &token, nil
}
