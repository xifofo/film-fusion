// 115 授权完成、账户绑定和资料保存；复用 SDK 用户响应生成快照及首次名称。
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// apply115AccountProfile 先生成可保存快照，再更新内存记录；调用方负责将资料与凭据一起保存。
// 仅新建时自动命名，重新授权始终保留用户自定义名称；无昵称时以 UID 作为可识别回退。
func apply115AccountProfile(storage *model.CloudStorage, info *sdk115.UserInfoResp, isNew bool, now time.Time) error {
	config, err := service.MergeCloudStorageAccountInfo(storage.Config, info, now)
	if err != nil {
		return err
	}

	if isNew {
		name := strings.TrimSpace(info.UserName)
		if name == "" {
			name = "115 " + strconv.FormatInt(info.UserID, 10)
		}
		storage.StorageName = name + "的网盘"
	}
	storage.Config = config

	return nil
}

// CompleteAuth 完成授权，获取token并保存
func (h *Auth115Handler) CompleteAuth(c *gin.Context) {
	var req Auth115CompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}

	session, ok := h.loadAuthSession(c, req.SessionID)
	if !ok {
		return
	}
	userID := session.UserID

	// 创建115 SDK客户端
	sdk115Client := sdk115.New()

	// 获取token
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	token, err := sdk115Client.CodeToToken(ctx, session.DeviceCode.UID, session.CodeVerifier)
	if err != nil {
		h.logger.Errorf("获取 token 错误: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "获取token失败")
		return
	}

	// 通过访问令牌获取115账号唯一标识(user_id)，作为云盘账号的稳定主键
	tokenedClient := sdk115.New(sdk115.WithAccessToken(token.AccessToken))
	userInfo, err := tokenedClient.UserInfo(ctx)
	if err != nil {
		h.logger.Errorf("获取115用户信息失败: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "获取115账号信息失败，无法完成授权")
		return
	}
	if userInfo == nil || userInfo.UserID <= 0 {
		h.error(c, http.StatusInternalServerError, 500, "115账号信息异常，无法完成授权")
		return
	}
	providerUID := strconv.FormatInt(userInfo.UserID, 10)

	// 定位目标记录：
	// - 重新登录(storage_id>0)：按 id 定位并更新指定记录，且必须属于当前用户
	// - 新建(storage_id==0)：始终新建一条；若该115账号已绑定到当前用户的某条配置，则拒绝，避免重复或覆盖
	var cloudStorage model.CloudStorage
	isUpdate := req.StorageID > 0

	if isUpdate {
		if err := database.DB.WithContext(c.Request.Context()).
			Where("id = ? AND user_id = ? AND storage_type = ?", req.StorageID, userID, model.StorageType115Open).
			First(&cloudStorage).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				h.error(c, http.StatusNotFound, 404, "指定的存储配置不存在")
				return
			}
			h.logger.Errorf("查询指定云存储失败: %v", err)
			h.error(c, http.StatusInternalServerError, 500, "查询存储配置失败")
			return
		}
		// 防止误操作：指定的记录若已绑定另一个115账号，则拒绝更新
		if cloudStorage.ProviderUID != "" && cloudStorage.ProviderUID != providerUID {
			h.error(c, http.StatusConflict, 409,
				fmt.Sprintf("扫码使用的115账号(user_id=%s)与当前存储配置绑定的账号(%s)不一致，请使用原账号扫码", providerUID, cloudStorage.ProviderUID))
			return
		}
	} else {
		// 新建场景：应用层判重——同一用户下该115账号若已绑定则拒绝
		var existing model.CloudStorage
		err := database.DB.WithContext(c.Request.Context()).
			Where("user_id = ? AND storage_type = ? AND provider_uid = ?", userID, model.StorageType115Open, providerUID).
			First(&existing).Error
		if err == nil {
			h.error(c, http.StatusConflict, 409,
				fmt.Sprintf("该115账号(user_id=%s)已绑定到存储配置「%s」，如需更新令牌请使用「重新登录」", providerUID, existing.StorageName))
			return
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Errorf("校验115账号是否已绑定失败: %v", err)
			h.error(c, http.StatusInternalServerError, 500, "查询存储配置失败")
			return
		}
	}

	// 更新配置信息
	now := time.Now()
	if err := apply115AccountProfile(&cloudStorage, userInfo, !isUpdate, now); err != nil {
		h.error(c, http.StatusBadRequest, 400, "无法保存账户资料，请检查存储额外配置是否为有效 JSON 对象")
		return
	}

	cloudStorage.UserID = userID
	cloudStorage.ProviderUID = providerUID
	cloudStorage.AppID = session.ClientID
	cloudStorage.StorageType = model.StorageType115Open
	cloudStorage.AccessToken = token.AccessToken
	cloudStorage.RefreshToken = token.RefreshToken
	cloudStorage.Status = model.StatusActive
	cloudStorage.ErrorMessage = ""
	cloudStorage.LastErrorAt = nil
	cloudStorage.LastRefreshAt = &now

	// 计算过期时间
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		cloudStorage.TokenExpiresAt = &expiresAt
	}
	cloudStorage.NormalizeMatch302Defaults()

	// 保存到数据库，GORM会根据ID自动判断是创建还是更新
	if err := database.DB.WithContext(c.Request.Context()).Save(&cloudStorage).Error; err != nil {
		h.logger.Errorf("保存云存储配置失败: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "保存配置失败")
		return
	}

	if isUpdate {
		h.logger.Infof("用户 %d 成功更新115授权(provider_uid=%s)，存储配置ID: %d", userID, providerUID, cloudStorage.ID)
	} else {
		h.logger.Infof("用户 %d 新增115授权(provider_uid=%s)，存储配置ID: %d", userID, providerUID, cloudStorage.ID)
	}

	// 清理会话
	authSessionsMu.Lock()
	delete(authSessions, req.SessionID)
	authSessionsMu.Unlock()

	var successMessage string
	if isUpdate {
		successMessage = "授权更新完成，配置已更新"
	} else {
		successMessage = "授权完成，配置已保存"
	}

	h.success(c, gin.H{
		"storage_id":    cloudStorage.ID,
		"access_token":  maskToken(token.AccessToken),
		"refresh_token": maskToken(token.RefreshToken),
		"expires_in":    token.ExpiresIn,
		"is_update":     isUpdate,
	}, successMessage)
}

// maskToken 遮盖token显示
func maskToken(token string) string {
	if len(token) <= 10 {
		return strings.Repeat("*", len(token))
	}
	return token[:5] + strings.Repeat("*", len(token)-10) + token[len(token)-5:]
}
