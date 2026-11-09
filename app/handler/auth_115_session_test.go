// 授权会话的所有入口必须在访问 115 前完成用户范围与有效期校验。
package handler

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func Test115PKCEVerifierContract(t *testing.T) {
	allowed := regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)
	previous := ""
	for _, length := range []int{-1, 43, 44, 128, 200} {
		value, err := generateLoginCodeVerifier(length)
		if err != nil || !allowed.MatchString(value) || value == previous {
			t.Fatal("verifier must use independent randomness and PKCE-safe characters")
		}
		previous = value
	}
}

func Test115SessionGuardsStatusAndCompletion(t *testing.T) {
	for _, endpoint := range []string{"status", "complete"} {
		for _, tc := range []struct {
			name    string
			user    uint
			age     time.Duration
			missing bool
			status  int
		}{
			{"unauthenticated", 0, 0, false, 401},
			{"foreign", 8, 0, false, 403},
			{"expired", 7, authSessionTTL + time.Second, false, 410},
			{"missing", 7, 0, true, 404},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				const id = "test-session-guard"
				authSessionsMu.Lock()
				delete(authSessions, id)
				if !tc.missing {
					authSessions[id] = &authSession{UserID: 7, CreatedAt: time.Now().Add(-tc.age)}
				}
				authSessionsMu.Unlock()
				t.Cleanup(func() {
					authSessionsMu.Lock()
					delete(authSessions, id)
					authSessionsMu.Unlock()
				})

				h := &Auth115Handler{}
				router := gin.New()
				router.Use(func(c *gin.Context) { c.Set("user_id", tc.user) })
				router.POST("/status", h.CheckStatus)
				router.POST("/complete", h.CompleteAuth)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/"+endpoint, strings.NewReader(`{"session_id":"`+id+`"}`)))
				if response.Code != tc.status {
					t.Fatalf("status=%d, want %d", response.Code, tc.status)
				}
			})
		}
	}
}
