package middleware

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"film-fusion/app/auth"
	"film-fusion/app/config"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// APIAuth accepts session JWTs or scoped API tokens. API tokens are header-only.
func APIAuth(cfg *config.Config, tokens *service.APITokenService) gin.HandlerFunc {
	jwtAuth := JWTAuth(cfg)
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], service.APITokenPrefix) {
			jwtAuth(c)
			return
		}
		token, user, err := tokens.Authenticate(c.Request.Context(), parts[1])
		if err != nil {
			if errors.Is(err, service.ErrInvalidAPIToken) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "API Token 无效或已失效"})
			} else {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": 503, "message": "暂时无法验证 API Token"})
			}
			return
		}
		scope := auth.APITokenScopeForRoute(c.FullPath())
		if scope == "" || !slices.Contains(token.Scopes, scope) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "message": "API Token 未获准访问此接口"})
			return
		}
		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set("api_token_id", token.ID)
		tokens.Touch(c.Request.Context(), token)
		c.Next()
	}
}
