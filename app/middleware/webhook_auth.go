package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"film-fusion/app/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CloudDrive2WebhookAuth 验证 CloudDrive2 发来的独立 Bearer Token。
// 中间件按请求读取共享配置指针，因此系统设置保存后可立即生效。
func CloudDrive2WebhookAuth(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings := cfg.Webhook.CloudDrive2
		if !settings.Enabled {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "CloudDrive2 webhook is disabled"})
			return
		}

		expected := strings.TrimSpace(settings.Token)
		if len(expected) < 32 {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "CloudDrive2 webhook authentication is not configured"})
			return
		}

		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !secureTokenEqual(parts[1], expected) {
			c.Header("WWW-Authenticate", `Bearer realm="clouddrive2-webhook"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Unauthorized"})
			return
		}

		c.Next()
	}
}

func secureTokenEqual(actual, expected string) bool {
	actualHash := sha256.Sum256([]byte(actual))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(actualHash[:], expectedHash[:]) == 1
}
