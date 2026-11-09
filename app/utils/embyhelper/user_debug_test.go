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

type userDebugCapture struct{ lines []string }

func (capture *userDebugCapture) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

func TestDeviceSessionIdentityAndDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"idle_device", `[{"DeviceId":"device-1","UserId":"user-1","UserName":"private-name"}]`, "user-1", 200},
		{"other_device_same_film_ip", `[{"DeviceId":"other","UserId":"wrong-user","RemoteEndPoint":"192.0.2.1","NowPlayingItem":{"Id":"same-film"}}]`, "", 200},
		{"ambiguous", `[{"DeviceId":"device-1","UserId":"user-1"},{"DeviceId":"device-1","UserId":"user-2"}]`, "", 200},
		{"same_user_duplicates", `[{"DeviceId":"device-1","UserId":"user-1"},{"DeviceId":"device-1","UserId":"user-1"}]`, "user-1", 200},
		{"empty", `[]`, "", 200},
		{"unauthorized", `{"message":"private-body"}`, "", 401},
		{"server_failure", `{"message":"private-body"}`, "", 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Sessions" || r.URL.Query().Get("DeviceId") != "device-1" {
					t.Errorf("wrong identity endpoint")
				}
				if r.Header.Get("X-Emby-Token") != "private-token" || r.URL.Query().Get("api_key") != "" {
					t.Error("identity must use the playback token, not admin credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			capture := &userDebugCapture{}
			ctx := embydebug.Start(context.Background(), capture)
			user, err := GetUserByDeviceContext(ctx, &config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-admin"}}, "private-token", "device-1")
			if (err != nil) != (test.status != 200) {
				t.Fatalf("unexpected error: %v", err)
			}
			got := ""
			if user != nil {
				got = user.ID
			}
			if got != test.want {
				t.Fatalf("user=%q want=%q", got, test.want)
			}
			output := strings.Join(capture.lines, "\n")
			if !strings.Contains(output, fmt.Sprintf("http_status=%d", test.status)) {
				t.Fatalf("missing HTTP diagnostic: %s", output)
			}
			for _, secret := range []string{"private-token", "private-admin", "private-body", "private-name", "192.0.2.1", "device-1"} {
				if strings.Contains(output, secret) {
					t.Errorf("diagnostic leaked %q", secret)
				}
			}
		})
	}
}
