// 115 扫码会话与状态读取；SDK 负责上游协议，凭据交换和资料保存见 auth_115_account.go。
package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
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

// Auth115QrCodeRequest 获取二维码请求
type Auth115QrCodeRequest struct {
	ClientID string `json:"client_id" binding:"required"`
	Name     string `json:"name"` // 兼容旧客户端；新建名称由授权账户资料生成
}

// Auth115StatusRequest 查询授权状态请求
type Auth115StatusRequest struct {
	SessionID string `json:"session_id" binding:"required"`
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

// generateLoginCodeVerifier 生成 PKCE verifier，长度按协议限定为 43–128 个 URL 安全字符。
func generateLoginCodeVerifier(length int) (string, error) {
	length = min(128, max(43, length))
	bytes := make([]byte, (length*3+3)/4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(bytes)[:length], nil
}

const authSessionTTL = 15 * time.Minute

// 存储会话信息
var (
	authSessions   = make(map[string]*authSession)
	authSessionsMu sync.Mutex
)

type authSession struct {
	DeviceCode   *sdk115.AuthDeviceCodeResp
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
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.Name = strings.TrimSpace(req.Name)
	if !database.Valid115ClientID(req.ClientID) {
		h.error(c, http.StatusBadRequest, 400, "请填写有效的应用 ID")
		return
	}

	// 获取当前用户ID
	userID := c.GetUint("user_id")
	if userID == 0 {
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	deviceCode, err := sdk115Client.AuthDeviceCode(ctx, req.ClientID, codeVerifier)
	if err != nil {
		h.logger.Errorf("获取 115 设备码错误: %v", err)
		h.error(c, http.StatusInternalServerError, 500, "获取设备码失败")
		return
	}

	// 生成会话ID
	sessionID := fmt.Sprintf("%d_%d", userID, time.Now().UnixNano())

	// 保存会话信息
	authSessionsMu.Lock()
	authSessions[sessionID] = &authSession{
		DeviceCode:   deviceCode,
		CodeVerifier: codeVerifier,
		ClientID:     req.ClientID,
		Name:         req.Name,
		UserID:       userID,
		CreatedAt:    time.Now(),
	}
	authSessionsMu.Unlock()

	// 清理过期会话（15分钟）
	h.cleanExpiredSessions()

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

	session, ok := h.loadAuthSession(c, req.SessionID)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	status, err := sdk115.New().QrCodeStatus(ctx, session.DeviceCode.UID, strconv.FormatInt(session.DeviceCode.Time, 10), session.DeviceCode.Sign)
	if err != nil {
		h.logger.Errorf("查询二维码状态失败: %v", err)
		h.error(c, http.StatusBadGateway, 502, "查询授权状态失败")
		return
	}

	var message string
	switch status.Status {
	case 0:
		message = "等待扫码"
	case 1:
		message = "扫码成功，等待确认"
	case 2:
		message = "确认登录成功"
	case -2:
		message = "已取消登录"
		// 清理会话
		authSessionsMu.Lock()
		delete(authSessions, req.SessionID)
		authSessionsMu.Unlock()
	default:
		message = fmt.Sprintf("未知状态: %d", status.Status)
	}

	h.success(c, gin.H{
		"status": status.Status,
	}, message)
}

// loadAuthSession 所有授权入口共用身份和有效期检查，不允许跨用户轮询或交换凭据。
func (h *Auth115Handler) loadAuthSession(c *gin.Context, id string) (*authSession, bool) {
	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return nil, false
	}

	authSessionsMu.Lock()
	defer authSessionsMu.Unlock()
	session, exists := authSessions[id]
	switch {
	case !exists:
		h.error(c, http.StatusNotFound, 404, "会话不存在或已过期")
	case session.UserID != userID:
		h.error(c, http.StatusForbidden, 403, "无权访问此会话")
	case time.Since(session.CreatedAt) > authSessionTTL:
		delete(authSessions, id)
		h.error(c, http.StatusGone, 410, "会话已过期")
	default:
		return session, true
	}

	return nil, false
}

// cleanExpiredSessions 清理过期会话
func (h *Auth115Handler) cleanExpiredSessions() {
	now := time.Now()
	authSessionsMu.Lock()
	defer authSessionsMu.Unlock()
	for sessionID, session := range authSessions {
		if now.Sub(session.CreatedAt) > authSessionTTL {
			delete(authSessions, sessionID)
		}
	}
}

// GetAuthSessions 获取当前用户的活跃授权会话（用于调试）
func (h *Auth115Handler) GetAuthSessions(c *gin.Context) {
	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	h.cleanExpiredSessions()

	var sessions []gin.H
	authSessionsMu.Lock()
	for sessionID, session := range authSessions {
		if session.UserID == userID {
			sessions = append(sessions, gin.H{
				"session_id": sessionID,
				"name":       session.Name,
				"client_id":  session.ClientID,
				"created_at": session.CreatedAt,
				"expires_at": session.CreatedAt.Add(authSessionTTL),
			})
		}
	}
	authSessionsMu.Unlock()

	h.success(c, gin.H{
		"sessions": sessions,
		"count":    len(sessions),
	}, "获取会话列表成功")
}
