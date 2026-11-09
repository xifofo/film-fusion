// 115 账户资料按需读取并合并至存储配置；只写资料快照，不刷新或覆盖授权凭据。
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"gorm.io/gorm"
)

// StorageAccountInfoError 将可预期的校验、上游失败和并发冲突转换为不含凭据的提示。
type StorageAccountInfoError struct {
	Status  int
	Message string
}

func (e *StorageAccountInfoError) Error() string { return e.Message }

func accountInfoError(status int, message string) error {
	return &StorageAccountInfoError{Status: status, Message: message}
}

// RefreshCloudStorageAccountInfo 仅允许所有者读取 115 Open 账户，返回最新记录供调用方投影。
// HTTP 客户端可由测试注入；保存时比较原配置及凭据，拒绝覆盖获取期间的并发编辑或重新授权。
func RefreshCloudStorageAccountInfo(ctx context.Context, db *gorm.DB, client *http.Client, userID, storageID uint) (*model.CloudStorage, error) {
	if userID == 0 {
		return nil, accountInfoError(401, "用户未认证")
	}

	var storage model.CloudStorage
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", storageID, userID).First(&storage).Error; err != nil {
		return nil, err
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, accountInfoError(400, "仅支持 115 OpenAPI 存储")
	}
	if strings.TrimSpace(storage.AccessToken) == "" {
		return nil, accountInfoError(400, "请先完成 115 OpenAPI 授权")
	}

	// 在访问上游前校验配置，避免无效配置触发无意义的资料读取。
	if _, err := parseAccountInfoConfig(storage.Config); err != nil {
		return nil, err
	}

	info, err := fetch115AccountInfo(ctx, client, storage.AccessToken)
	if err != nil {
		return nil, err
	}
	uid := strconv.FormatInt(info.UserID, 10)
	if storage.ProviderUID != "" && storage.ProviderUID != uid {
		return nil, accountInfoError(409, "115 账户与存储绑定不一致，请重新授权后重试")
	}

	now := time.Now().UTC()
	encoded, err := MergeCloudStorageAccountInfo(storage.Config, info, now)
	if err != nil {
		return nil, err
	}

	// 只更新本次负责的字段；COALESCE 兼容旧记录中的 SQL NULL，不做整行 Save。
	result := db.WithContext(ctx).Model(&model.CloudStorage{}).
		Where("id = ? AND user_id = ? AND storage_type = ?", storageID, userID, model.StorageType115Open).
		Where("COALESCE(config, '') = ? AND COALESCE(access_token, '') = ? AND COALESCE(provider_uid, '') = ?", storage.Config, storage.AccessToken, storage.ProviderUID).
		Updates(map[string]any{"config": encoded, "provider_uid": uid, "updated_at": now})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, accountInfoError(409, "存储配置或授权已变化，请刷新后重试")
	}

	storage.Config = encoded
	storage.ProviderUID = uid
	storage.UpdatedAt = now

	return &storage, nil
}

// parseAccountInfoConfig 保留配置的原始 JSON 数值精度；拒绝非对象而不是覆盖用户配置。
func parseAccountInfoConfig(raw string) (map[string]json.RawMessage, error) {
	config := make(map[string]json.RawMessage)
	if strings.TrimSpace(raw) != "" {
		if json.Unmarshal([]byte(raw), &config) != nil || config == nil {
			return nil, accountInfoError(400, "额外配置必须是 JSON 对象，请先修正配置")
		}
	}

	return config, nil
}

// MergeCloudStorageAccountInfo 合并已取得的 SDK 用户资料，供授权保存与手动更新共用。
// 不发起网络请求、不修改传入资料；保留额外配置的其他字段，不接收或保存授权凭据。
func MergeCloudStorageAccountInfo(raw string, info *sdk115.UserInfoResp, fetchedAt time.Time) (string, error) {
	if info == nil || info.UserID <= 0 {
		return "", accountInfoError(502, "115 未返回有效账户信息，请检查授权后重试")
	}
	config, err := parseAccountInfoConfig(raw)
	if err != nil {
		return "", err
	}

	snapshot, err := json.Marshal(struct {
		FetchedAt time.Time            `json:"fetched_at"`
		Data      *sdk115.UserInfoResp `json:"data"`
	}{FetchedAt: fetchedAt.UTC(), Data: info})
	if err != nil {
		return "", err
	}
	config["115_account_info"] = snapshot
	encoded, err := json.Marshal(config)

	return string(encoded), err
}

// 单次只读调用不进入 SDK 的隐式令牌刷新链；令牌失效时保留旧快照并提示重新授权。
func fetch115AccountInfo(ctx context.Context, injected *http.Client, token string) (*sdk115.UserInfoResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := http.Client{Timeout: 30 * time.Second}
	if injected != nil {
		client = *injected
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sdk115.ApiUserInfo, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, accountInfoError(502, "获取 115 账户信息失败，请稍后重试")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, accountInfoError(502, "115 账户信息接口不可用，请检查授权后重试")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, accountInfoError(502, "读取 115 账户信息失败")
	}
	var envelope struct {
		State bool                 `json:"state"`
		Code  json.Number          `json:"code"`
		Data  *sdk115.UserInfoResp `json:"data"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &envelope) != nil || !envelope.State || (envelope.Code != "" && envelope.Code != "0") || envelope.Data == nil || envelope.Data.UserID <= 0 {
		return nil, accountInfoError(502, "115 未返回有效账户信息，请检查授权后重试")
	}

	return envelope.Data, nil
}
