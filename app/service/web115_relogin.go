package service

import (
	"fmt"
	"strings"
	"time"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

// 115 扫码登录相关接口（用于 login_another_app 式的 cookie 自动续期）。
// 完整流程：token(匿名) -> scan(旧cookie) -> confirm(旧cookie) -> status(确认) -> result(换发新cookie)。
const (
	// web115QrcodePromptAPI 扫码：相当于「已登录设备扫了这个二维码」
	web115QrcodePromptAPI = "https://qrcodeapi.115.com/api/2.0/prompt.php"
	// web115QrcodeSloginAPI 确认：相当于「已登录设备点了确认登录」
	web115QrcodeSloginAPI = "https://qrcodeapi.115.com/api/2.0/slogin.php"
	// web115ReloginTimeout 单次续期中每个 HTTP 请求的超时，防止网络卡死阻塞保活协程
	web115ReloginTimeout = 30 * time.Second
)

// DefaultReloginApp 默认换端登录的目标 app。
// 选用 alipaymini(ssoent=R2)这类独立设备端，避免踢掉用户日常使用的 web(A1)/手机端。
const DefaultReloginApp = driver.LoginAppAlipayMini

// ParseLoginApp 把字符串解析为合法的 LoginApp，非法值回退到默认端。
func ParseLoginApp(s string) driver.LoginApp {
	app := driver.LoginApp(strings.TrimSpace(s))
	switch app {
	case driver.LoginAppWeb,
		driver.LoginAppAndroid,
		driver.LoginAppIOS,
		driver.LoginAppTV,
		driver.LoginAppAlipayMini,
		driver.LoginAppWechatMini,
		driver.LoginQAppAndroid:
		return app
	default:
		return DefaultReloginApp
	}
}

// RefreshCookieByApp 用一个仍然在线的旧 cookie，免扫码地为同一账号换取一份新的、
// 绑定到指定 app 端的 cookie。等价于 p115client 的 login_another_app。
//
// 注意：旧 cookie 必须仍然有效（在线），否则无法完成自动扫码确认；
// 自动确认环节可能触发 115「老乡验证」风控（异地/机房 IP 上尤甚），失败时返回错误由上层兜底。
func (s *Web115Service) RefreshCookieByApp(oldCookie string, app driver.LoginApp) (string, error) {
	oldCookie = normalizeCookie(oldCookie)
	if oldCookie == "" {
		return "", fmt.Errorf("旧 cookie 为空")
	}
	if app == "" {
		app = DefaultReloginApp
	}

	// 旧 cookie 的 client，用于自动 scan / confirm（NewClient 内部已做 CookieCheck，确认在线）
	oldClient, err := s.NewClient(oldCookie)
	if err != nil {
		return "", fmt.Errorf("旧 cookie 无效，无法续期: %w", err)
	}
	oldClient.Client.SetTimeout(web115ReloginTimeout)

	// 干净 client（无 cookie），用于匿名拿 token、查 status、换发新 cookie
	freshClient := driver.New(driver.UA(web115BrowserUA))
	freshClient.Client.SetTimeout(web115ReloginTimeout)

	// 1. 获取二维码 token（匿名）
	session, err := freshClient.QRCodeStart()
	if err != nil {
		return "", fmt.Errorf("获取二维码 token 失败: %w", err)
	}

	// 2. 用旧 cookie 扫码
	if err := s.qrcodeScan(oldClient, session.UID); err != nil {
		return "", fmt.Errorf("自动扫码失败: %w", err)
	}

	// 3. 用旧 cookie 确认
	if err := s.qrcodeScanConfirm(oldClient, session.UID); err != nil {
		return "", fmt.Errorf("自动确认失败: %w", err)
	}

	// 4. 确认二维码状态已变为「已确认登录」
	status, err := freshClient.QRCodeStatus(session)
	if err != nil {
		return "", fmt.Errorf("查询二维码状态失败: %w", err)
	}
	if !status.IsAllowed() {
		return "", fmt.Errorf("二维码状态异常(status=%d, msg=%s)，自动确认未生效（可能触发风控）", status.Status, status.Msg)
	}

	// 5. 换发指定 app 端的新 cookie
	cred, err := freshClient.QRCodeLoginWithApp(session, app)
	if err != nil {
		return "", fmt.Errorf("换发新 cookie 失败: %w", err)
	}

	newCookie := strings.TrimSpace(cred.Cookie())
	if newCookie == "" || strings.TrimSpace(cred.UID) == "" {
		return "", fmt.Errorf("换发的新 cookie 为空")
	}
	return newCookie, nil
}

// qrcodeScan 用旧 cookie 扫描二维码：GET /api/2.0/prompt.php?uid={uid}
// oldClient 已在 NewClient 中设置了 Cookie 请求头，这里的请求会自动携带旧 cookie。
func (s *Web115Service) qrcodeScan(oldClient *driver.Pan115Client, uid string) error {
	resp, err := oldClient.NewRequest().
		SetQueryParam("uid", uid).
		Get(web115QrcodePromptAPI)
	if err != nil {
		return err
	}
	if resp != nil && resp.IsError() {
		return fmt.Errorf("prompt.php 返回 HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

// qrcodeScanConfirm 用旧 cookie 确认登录：GET /api/2.0/slogin.php?key={uid}&uid={uid}&client=0
func (s *Web115Service) qrcodeScanConfirm(oldClient *driver.Pan115Client, uid string) error {
	resp, err := oldClient.NewRequest().
		SetQueryParams(map[string]string{
			"key":    uid,
			"uid":    uid,
			"client": "0",
		}).
		Get(web115QrcodeSloginAPI)
	if err != nil {
		return err
	}
	if resp != nil && resp.IsError() {
		return fmt.Errorf("slogin.php 返回 HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}
