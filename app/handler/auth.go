package handler

import (
	"film-fusion/app/auth"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// AuthHandler 认证处理器
type AuthHandler struct {
	config     *config.Config
	jwtService *auth.JWTService
	protection *service.EmbyLoginProtection
}

// NewAuthHandler 创建认证处理器
func NewAuthHandler(cfg *config.Config, protection *service.EmbyLoginProtection) *AuthHandler {
	return &AuthHandler{
		config:     cfg,
		jwtService: auth.NewJWTService(cfg),
		protection: protection,
	}
}

// 创建成功响应
func (h *AuthHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": message,
		"data":    data,
	})
}

// 创建错误响应
func (h *AuthHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, gin.H{
		"code":    errorCode,
		"message": message,
		"data":    nil,
	})
}

// LoginRequest 登录请求结构
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应结构
type LoginResponse struct {
	Token    string      `json:"token"`
	User     *model.User `json:"user"`
	ExpireAt int64       `json:"expire_at"`
}

// Login 用户登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	attempt, blocked := h.protection.InspectCredentials(c.Request, req.Username)
	if blocked {
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 查找用户
	var user model.User
	db := database.GetDB()
	result := db.Where("username = ?", req.Username).First(&user)
	if result.Error != nil {
		h.protection.ObserveResponse(attempt, http.StatusUnauthorized)
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 验证密码
	if !utils.VerifyPassword(req.Password, user.Password) {
		h.protection.ObserveResponse(attempt, http.StatusUnauthorized)
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 检查用户是否激活
	if !user.IsActive || !user.IsAdmin {
		h.protection.ObserveResponse(attempt, http.StatusUnauthorized)
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 生成JWT token
	token, err := h.jwtService.GenerateToken(user.ID, user.Username)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "生成令牌失败")
		return
	}
	h.protection.ObserveResponse(attempt, http.StatusOK)

	// 更新最后登录时间
	now := time.Now()
	user.LastLogin = &now
	db.Save(&user)

	// 计算过期时间
	expireAt := time.Now().Add(time.Duration(h.config.JWT.ExpireTime) * time.Hour).Unix()

	h.success(c, LoginResponse{
		Token:    token,
		User:     &user,
		ExpireAt: expireAt,
	}, "登录成功")
}

// RefreshToken 刷新令牌
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" || strings.TrimSpace(parts[1]) == "" {
		h.error(c, http.StatusUnauthorized, 401, "Authorization header is required")
		return
	}

	newToken, err := h.jwtService.RefreshToken(parts[1])
	if err != nil {
		h.error(c, http.StatusUnauthorized, 401, "刷新令牌失败: "+err.Error())
		return
	}

	expireAt := time.Now().Add(time.Duration(h.config.JWT.ExpireTime) * time.Hour).Unix()

	h.success(c, gin.H{
		"token":     newToken,
		"expire_at": expireAt,
	}, "刷新成功")
}

// Logout 用户退出登录。
// FilmFusion 使用无状态 JWT，服务端不保存会话；客户端收到成功响应后删除本地令牌。
// 接口保持幂等，不要求令牌仍然有效，确保过期会话也能正常退出。
func (h *AuthHandler) Logout(c *gin.Context) {
	h.success(c, nil, "退出登录成功")
}

// Me 获取当前用户信息
func (h *AuthHandler) Me(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "未认证")
		return
	}

	var user model.User
	db := database.GetDB()
	if err := db.First(&user, userID).Error; err != nil {
		h.error(c, http.StatusNotFound, 404, "用户不存在")
		return
	}

	h.success(c, user, "success")
}
