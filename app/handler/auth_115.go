package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"resty.dev/v3"
)

// Auth115Handler 115授权处理器
type Auth115Handler struct {
	config *config.Config
	logger *logger.Logger
}

// NewAuth115Handler 创建115授权处理器
func NewAuth115Handler(cfg *config.Config, log *logger.Logger) *Auth115Handler {
	return &Auth115Handler{
		config: cfg,
		logger: log,
	}
}

// LoginResp 登录相关的响应结构体
type LoginResp[T any] struct {
	State   int    `json:"state"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// LoginQrCodeStatusResp 二维码状态响应
type LoginQrCodeStatusResp struct {
	Msg     string `json:"msg"`
	Status  int    `json:"status"`
	Version string `json:"version"`
}

// Auth115QrCodeRequest 获取二维码请求
type Auth115QrCodeRequest struct {
	ClientID string `json:"client_id" binding:"required"`
	Name     string `json:"name" binding:"required"` // 存储配置名称
}

// Auth115QrCodeResponse 获取二维码响应
type Auth115QrCodeResponse struct {
	QrCodeData string `json:"qr_code_data"` // 二维码数据
	SessionID  string `json:"session_id"`   // 用于轮询的会话ID
}

// Auth115StatusRequest 查询授权状态请求
type Auth115StatusRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

// Auth115StatusResponse 查询授权状态响应
type Auth115StatusResponse struct {
	Status  int    `json:"status"` // 0: 等待扫码, 1: 已扫码待确认, 2: 确认成功, -2: 已取消
	Message string `json:"message"`
}

// Auth115CompleteRequest 完成授权请求
type Auth115CompleteRequest struct {
	StorageID uint   `json:"storage_id"` // 云存储配置ID，用于重新登录场景指定更新目标
	SessionID string `json:"session_id" binding:"required"`
}

// ApiResponse 统一的API响应格式
type ApiResponse struct {
	Code    int    `json:"code"`    // 状态码，0表示成功
	Message string `json:"message"` // 响应消息
	Data    any    `json:"data"`    // 响应数据
}

// 创建成功响应
func (h *Auth115Handler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *Auth115Handler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// generateLoginCodeVerifier 生成符合 OAuth2 PKCE 标准的随机 code verifier
func generateLoginCodeVerifier(length int) (string, error) {
	if length < 43 {
		length = 43 // 最小长度43
	}
	if length > 128 {
		length = 128 // 最大长度128
	}

	// 计算需要的字节数 (base64编码后长度约为原始字节数的4/3)
	byteLength := (length * 3) / 4

	// 生成随机字节
	randomBytes := make([]byte, byteLength)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", fmt.Errorf("生成随机字节失败: %v", err)
	}

	// 使用 URL 安全的 base64 编码，并移除填充符
	codeVerifier := base64.RawURLEncoding.EncodeToString(randomBytes)

	// 确保长度符合要求
	if len(codeVerifier) < length {
		// 如果不够长，补充随机字符
		additionalBytes := make([]byte, length-len(codeVerifier))
		rand.Read(additionalBytes)
		codeVerifier += base64.RawURLEncoding.EncodeToString(additionalBytes)
	}

	// 截取到指定长度
	if len(codeVerifier) > length {
		codeVerifier = codeVerifier[:length]
	}

	return codeVerifier, nil
}

// maskToken 遮盖token显示
func maskToken(token string) string {
	if len(token) <= 10 {
		return strings.Repeat("*", len(token))
	}
	return token[:5] + strings.Repeat("*", len(token)-10) + token[len(token)-5:]
}

// 存储会话信息
var authSessions = make(map[string]*authSession)

// 简化的设备码结构体
type DeviceCode struct {
	QrCode string `json:"qr_code"`
	Sign   string `json:"sign"`
	Time   int64  `json:"time"`
	UID    string `json:"uid"`
}

type authSession struct {
	DeviceCode   *DeviceCode
	CodeVerifier string
	ClientID     string
	Name         string
	UserID       uint
	CreatedAt    time.Time
}

// GetQrCode 获取115登录二维码
func (h *Auth115Handler) GetQrCode(c *gin.Context) {
	var req Auth115QrCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}

	// 获取当前用户ID
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	// 生成随机 code verifier
	codeVerifier, err := generateLoginCodeVerifier(43)
	if err != nil {
		h.logger.Errorf("生成 code verifier 错误: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "生成授权码失败")
		return
	}

	// 创建115 SDK客户端
	sdk115Client := sdk115.New()

	// 获取设备码
	deviceCodeResp, err := sdk115Client.AuthDeviceCode(context.Background(), req.ClientID, codeVerifier)
	if err != nil {
		h.logger.Errorf("获取 115 设备码错误: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "获取设备码失败")
		return
	}

	// 转换为简化的设备码结构体
	deviceCode := &DeviceCode{
		QrCode: deviceCodeResp.QrCode,
		Sign:   deviceCodeResp.Sign,
		Time:   deviceCodeResp.Time,
		UID:    deviceCodeResp.UID,
	}

	// 生成会话ID
	sessionID := fmt.Sprintf("%d_%d", userID.(uint), time.Now().UnixNano())

	// 保存会话信息
	authSessions[sessionID] = &authSession{
		DeviceCode:   deviceCode,
		CodeVerifier: codeVerifier,
		ClientID:     req.ClientID,
		Name:         req.Name,
		UserID:       userID.(uint),
		CreatedAt:    time.Now(),
	}

	// 清理过期会话（15分钟）
	go h.cleanExpiredSessions()

	h.success(c, gin.H{
		"qr_code_data": deviceCode.QrCode,
		"session_id":   sessionID,
	}, "请使用115手机客户端扫描二维码")
}

// CheckStatus 检查授权状态
func (h *Auth115Handler) CheckStatus(c *gin.Context) {
	var req Auth115StatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}

	// 获取会话信息
	session, exists := authSessions[req.SessionID]
	if !exists {
		h.error(c, http.StatusNotFound, 404, "会话不存在或已过期")
		return
	}

	// 检查会话是否过期（15分钟）
	if time.Since(session.CreatedAt) > 15*time.Minute {
		delete(authSessions, req.SessionID)
		h.error(c, http.StatusGone, 410, "会话已过期")
		return
	}

	// 查询二维码状态
	client := resty.New()
	defer client.Close()

	resp, err := client.R().
		SetQueryParams(map[string]string{
			"sign": session.DeviceCode.Sign,
			"time": strconv.FormatInt(session.DeviceCode.Time, 10),
			"uid":  session.DeviceCode.UID,
		}).
		Get("https://qrcodeapi.115.com/get/status/")

	if err != nil {
		h.logger.Errorf("查询二维码状态失败: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "查询状态失败")
		return
	}

	// 解析响应
	var qrResponse LoginResp[LoginQrCodeStatusResp]
	if err := json.Unmarshal([]byte(resp.String()), &qrResponse); err != nil {
		h.logger.Errorf("解析状态响应失败: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "解析响应失败")
		return
	}

	var message string
	switch qrResponse.Data.Status {
	case 0:
		message = "等待扫码"
	case 1:
		message = "扫码成功，等待确认"
	case 2:
		message = "确认登录成功"
	case -2:
		message = "已取消登录"
		// 清理会话
		delete(authSessions, req.SessionID)
	default:
		message = fmt.Sprintf("未知状态: %d", qrResponse.Data.Status)
	}

	h.success(c, gin.H{
		"status": qrResponse.Data.Status,
	}, message)
}

// CompleteAuth 完成授权，获取token并保存
func (h *Auth115Handler) CompleteAuth(c *gin.Context) {
	var req Auth115CompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}

	// 获取当前用户ID
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	// 获取会话信息
	session, exists := authSessions[req.SessionID]
	if !exists {
		h.error(c, http.StatusNotFound, 404, "会话不存在或已过期")
		return
	}

	// 验证用户身份
	if session.UserID != userID.(uint) {
		h.error(c, http.StatusForbidden, 403, "无权访问此会话")
		return
	}

	// 创建115 SDK客户端
	sdk115Client := sdk115.New()

	// 获取token
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if userInfo.UserID == 0 {
		h.error(c, http.StatusInternalServerError, 500, "115账号信息异常，无法完成授权")
		return
	}
	providerUID := strconv.FormatInt(userInfo.UserID, 10)

	// 按优先级定位目标记录：
	// 1) 若请求显式指定 storage_id（重新登录场景），按 id 定位，且必须属于当前用户
	// 2) 否则按 (user_id, storage_type, provider_uid) 定位同一个115账号
	// 3) 仍未命中，兼容尚未回填 provider_uid 的旧数据：按 (user_id, storage_type, app_id) 定位 provider_uid 为空的记录
	var cloudStorage model.CloudStorage
	var found bool

	if req.StorageID > 0 {
		if err := database.DB.
			Where("id = ? AND user_id = ? AND storage_type = ?", req.StorageID, userID.(uint), model.StorageType115Open).
			First(&cloudStorage).Error; err == nil {
			found = true
			// 防止误操作：指定的记录若已绑定另一个115账号，则拒绝更新
			if cloudStorage.ProviderUID != "" && cloudStorage.ProviderUID != providerUID {
				h.error(c, http.StatusConflict, 409,
					fmt.Sprintf("扫码使用的115账号(user_id=%s)与当前存储配置绑定的账号(%s)不一致，请使用原账号扫码", providerUID, cloudStorage.ProviderUID))
				return
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Errorf("查询指定云存储失败: %v", err)
			h.error(c, http.StatusInternalServerError, 500, "查询存储配置失败")
			return
		}
	}

	if !found {
		if err := database.DB.
			Where("user_id = ? AND storage_type = ? AND provider_uid = ?", userID.(uint), model.StorageType115Open, providerUID).
			First(&cloudStorage).Error; err == nil {
			found = true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Errorf("按 provider_uid 查询云存储失败: %v", err)
			h.error(c, http.StatusInternalServerError, 500, "查询存储配置失败")
			return
		}
	}

	if !found {
		if err := database.DB.
			Where("user_id = ? AND storage_type = ? AND app_id = ? AND (provider_uid IS NULL OR provider_uid = '')",
				userID.(uint), model.StorageType115Open, session.ClientID).
			First(&cloudStorage).Error; err == nil {
			found = true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Errorf("按 app_id 兼容查询云存储失败: %v", err)
			h.error(c, http.StatusInternalServerError, 500, "查询存储配置失败")
			return
		}
	}

	wasNew := !found

	// 更新配置信息
	now := time.Now()
	cloudStorage.UserID = userID.(uint)
	cloudStorage.ProviderUID = providerUID
	cloudStorage.AppID = session.ClientID
	if session.Name != "" {
		cloudStorage.StorageName = session.Name
	}
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

	// 保存到数据库，GORM会根据ID自动判断是创建还是更新
	if err := database.DB.Save(&cloudStorage).Error; err != nil {
		h.logger.Errorf("保存云存储配置失败: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "保存配置失败")
		return
	}

	isUpdate := !wasNew
	if isUpdate {
		h.logger.Infof("用户 %d 成功更新115授权(provider_uid=%s)，存储配置ID: %d", userID.(uint), providerUID, cloudStorage.ID)
	} else {
		h.logger.Infof("用户 %d 新增115授权(provider_uid=%s)，存储配置ID: %d", userID.(uint), providerUID, cloudStorage.ID)
	}

	// 清理会话
	delete(authSessions, req.SessionID)

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

// cleanExpiredSessions 清理过期会话
func (h *Auth115Handler) cleanExpiredSessions() {
	now := time.Now()
	for sessionID, session := range authSessions {
		if now.Sub(session.CreatedAt) > 15*time.Minute {
			delete(authSessions, sessionID)
		}
	}
}

// GetAuthSessions 获取当前用户的活跃授权会话（用于调试）
func (h *Auth115Handler) GetAuthSessions(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var sessions []gin.H
	for sessionID, session := range authSessions {
		if session.UserID == userID.(uint) {
			sessions = append(sessions, gin.H{
				"session_id": sessionID,
				"name":       session.Name,
				"client_id":  session.ClientID,
				"created_at": session.CreatedAt,
				"expires_at": session.CreatedAt.Add(15 * time.Minute),
			})
		}
	}

	h.success(c, gin.H{
		"sessions": sessions,
		"count":    len(sessions),
	}, "获取会话列表成功")
}
