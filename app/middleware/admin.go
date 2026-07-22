package middleware

import (
	"net/http"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
)

// RequireAdmin 限制 FilmFusion 管理接口仅允许启用中的管理员账号访问。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := c.Get("user_id")
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "未认证"})
			return
		}

		var user model.User
		if err := database.GetDB().Select("id", "is_active", "is_admin").First(&user, userID).Error; err != nil || !user.IsActive || !user.IsAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "message": "无权访问管理接口"})
			return
		}
		c.Next()
	}
}
