package embyhelper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/utils/embydebug"
)

type userDebugCapture struct {
	lines []string
}

func (capture *userDebugCapture) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

// 非认证 HTTP 错误不能在诊断里被写成 Token 过期，已有 nil 结果语义保持不变。
func TestUserTokenDebugReportsActualHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusBadGateway, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			capture := &userDebugCapture{}
			ctx := embydebug.Start(context.Background(), capture)
			user, err := GetUserByTokenContext(ctx, &config.Config{Emby: config.EmbyConfig{URL: server.URL}}, "private-token")
			if err != nil || user != nil {
				t.Fatalf("lookup behavior changed: user=%v err=%v", user, err)
			}
			output := strings.Join(capture.lines, "\n")
			if !strings.Contains(output, fmt.Sprintf("http_status=%d", status)) || !strings.Contains(output, "has_user=false") {
				t.Fatalf("missing actual identity result: %s", output)
			}
			if strings.Contains(output, "private-token") {
				t.Fatal("credential leaked in diagnostics")
			}
		})
	}
}
