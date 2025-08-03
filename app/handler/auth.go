package handler

import (
	"film-fusion/app/auth"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// AuthHandler 认证处理器
type AuthHandler struct {
	config     *config.Config
	jwtService *auth.JWTService
}

// NewAuthHandler 创建认证处理器
func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		config:     cfg,
		jwtService: auth.NewJWTService(cfg),
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

// RegisterRequest 注册请求结构
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=20"`
	Password string `json:"password" binding:"required,min=6"`
	Email    string `json:"email" binding:"required,email"`
}

// Login 用户登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	// 查找用户
	var user model.User
	db := database.GetDB()
	result := db.Where("username = ?", req.Username).First(&user)
	if result.Error != nil {
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 验证密码
	if !utils.VerifyPassword(req.Password, user.Password) {
		h.error(c, http.StatusUnauthorized, 401, "用户名或密码错误")
		return
	}

	// 检查用户是否激活
	if !user.IsActive {
		h.error(c, http.StatusForbidden, 403, "用户账号已被禁用")
		return
	}

	// 生成JWT token
	token, err := h.jwtService.GenerateToken(user.ID, user.Username)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "生成令牌失败")
		return
	}

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

// Register 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	db := database.GetDB()

	// 检查用户名是否已存在
	var existingUser model.User
	if result := db.Where("username = ?", req.Username).First(&existingUser); result.Error == nil {
		h.error(c, http.StatusConflict, 409, "用户名已存在")
		return
	}

	// 检查邮箱是否已存在
	if result := db.Where("email = ?", req.Email).First(&existingUser); result.Error == nil {
		h.error(c, http.StatusConflict, 409, "邮箱已存在")
		return
	}

	// 哈希密码
	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "密码哈希失败")
		return
	}

	// 创建新用户
	user := model.User{
		Username: req.Username,
		Password: hashedPassword,
		Email:    req.Email,
		IsActive: true,
	}

	if err := db.Create(&user).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建用户失败")
		return
	}

	h.success(c, user, "注册成功")
}

// RefreshToken 刷新令牌
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		h.error(c, http.StatusUnauthorized, 401, "Authorization header is required")
		return
	}

	// 提取token
	token := authHeader[7:] // 去掉 "Bearer " 前缀

	newToken, err := h.jwtService.RefreshToken(token)
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
