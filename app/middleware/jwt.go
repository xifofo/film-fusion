package middleware

import (
	"film-fusion/app/auth"
	"film-fusion/app/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// JWTAuth JWT认证中间件
func JWTAuth(cfg *config.Config) gin.HandlerFunc {
	jwtService := auth.NewJWTService(cfg)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "Authorization header is required",
			})
			c.Abort()
			return
		}

		// 检查Bearer前缀
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "Authorization header format must be Bearer {token}",
			})
			c.Abort()
			return
		}

		token := parts[1]
		claims, err := jwtService.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "Invalid token: " + err.Error(),
			})
			c.Abort()
			return
		}

		// 将用户信息存储到上下文中
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// OptionalJWTAuth 可选的JWT认证中间件（不强制要求token）
func OptionalJWTAuth(cfg *config.Config) gin.HandlerFunc {
	jwtService := auth.NewJWTService(cfg)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				token := parts[1]
				if claims, err := jwtService.ValidateToken(token); err == nil {
					c.Set("user_id", claims.UserID)
					c.Set("username", claims.Username)
				}
			}
		}
		c.Next()
	}
}
