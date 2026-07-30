package config

import (
	"fmt"
	"log"
	"net/netip"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server" json:"server"`
	Site       SiteConfig       `mapstructure:"site" json:"site"`
	Webhook    WebhookConfig    `mapstructure:"webhook" json:"webhook"`
	Log        LogConfig        `mapstructure:"log" json:"log"`
	JWT        JWTConfig        `mapstructure:"jwt" json:"jwt"`
	Emby       EmbyConfig       `mapstructure:"emby" json:"emby"`
	Telegram   TelegramConfig   `mapstructure:"telegram" json:"telegram"`
	MoviePilot MoviePilotConfig `mapstructure:"moviepilot" json:"moviepilot"`
	TMDB       TMDBConfig       `mapstructure:"tmdb" json:"tmdb"`
	HDHive     HDHiveConfig     `mapstructure:"hdhive" json:"hdhive"`
}

const (
	DefaultLoginTitle              = "Film Fusion"
	DefaultLoginSubtitle           = "Film Fusion 是简单的 Emby + 网盘的辅助工具"
	DefaultLoginFormTitle          = "欢迎回来"
	DefaultLoginFormSubtitle       = "使用管理员账户进入 Film Fusion 控制台"
	DefaultFooterText              = "Powered by Kumayi"
	DefaultLoginBackgroundSource   = "custom"
	DefaultLoginBackgroundMode     = "latest"
	DefaultLoginBackgroundInterval = 12
	DefaultLoginBackgroundLimit    = 10
)

// SiteConfig 保存可安全展示给未登录用户的站点外观配置。
type SiteConfig struct {
	LoginTitle              string `mapstructure:"login_title" json:"login_title"`
	LoginSubtitle           string `mapstructure:"login_subtitle" json:"login_subtitle"`
	LoginFormTitle          string `mapstructure:"login_form_title" json:"login_form_title"`
	LoginFormSubtitle       string `mapstructure:"login_form_subtitle" json:"login_form_subtitle"`
	LoginBackgroundURL      string `mapstructure:"login_background_url" json:"login_background_url"`
	LoginBackgroundSource   string `mapstructure:"login_background_source" json:"login_background_source"`
	LoginBackgroundMode     string `mapstructure:"login_background_mode" json:"login_background_mode"`
	LoginBackgroundInterval int    `mapstructure:"login_background_interval" json:"login_background_interval"`
	LoginBackgroundLimit    int    `mapstructure:"login_background_limit" json:"login_background_limit"`
	FooterText              string `mapstructure:"footer_text" json:"footer_text"`
	ICPNumber               string `mapstructure:"icp_number" json:"icp_number"`
	PoliceNumber            string `mapstructure:"police_number" json:"police_number"`
}

// WebhookConfig 保存外部 webhook 的独立鉴权配置。
// webhook 密钥不得复用管理后台密码或 JWT 密钥。
type WebhookConfig struct {
	CloudDrive2 CloudDrive2WebhookConfig `mapstructure:"clouddrive2" json:"clouddrive2"`
}

type CloudDrive2WebhookConfig struct {
	Enabled bool   `mapstructure:"enabled" json:"enabled"` // 是否启用 Bearer Token 鉴权；关闭时仍接收 Webhook
	Token   string `mapstructure:"token" json:"token"`
}

type ServerConfig struct {
	Port                   string              `mapstructure:"port" json:"port"`
	Username               string              `mapstructure:"username" json:"username"`
	Password               string              `mapstructure:"password" json:"password"`
	Download115Concurrency int                 `mapstructure:"download_115_concurrency" json:"download_115_concurrency"`
	ProcessNewMedia        bool                `mapstructure:"process_new_media" json:"process_new_media"` // 是否处理新增媒体事件
	Security               LoginSecurityConfig `mapstructure:"security" json:"security"`                   // FilmFusion 登录防爆破配置
}

type LogConfig struct {
	Level      string `mapstructure:"level" json:"level"`
	Format     string `mapstructure:"format" json:"format"`           // json 或 text
	Output     string `mapstructure:"output" json:"output"`           // stdout 或 file
	MaxSize    int    `mapstructure:"max_size" json:"max_size"`       // 兆字节
	MaxBackups int    `mapstructure:"max_backups" json:"max_backups"` // 备份数量
	MaxAge     int    `mapstructure:"max_age" json:"max_age"`         // 天数
	Compress   bool   `mapstructure:"compress" json:"compress"`       // 是否压缩旧文件
}

type JWTConfig struct {
	Secret     string `mapstructure:"secret" json:"secret"`           // JWT 密钥
	ExpireTime int    `mapstructure:"expire_time" json:"expire_time"` // 过期时间（小时）
	Issuer     string `mapstructure:"issuer" json:"issuer"`           // 签发者
}

const minJWTSecretBytes = 32

var insecureJWTSecrets = map[string]struct{}{
	"film-fusion-secret-key":               {},
	"your-jwt-secret-key":                  {},
	"your-secret-key-change-in-production": {},
}

// TelegramConfig 控制 Telegram Bot 告警投递及各类安全事件开关。
type TelegramConfig struct {
	Enabled                bool   `mapstructure:"enabled" json:"enabled"`
	BotToken               string `mapstructure:"bot_token" json:"bot_token"`
	ChatID                 string `mapstructure:"chat_id" json:"chat_id"`
	MessageThreadID        int64  `mapstructure:"message_thread_id" json:"message_thread_id"`
	InstanceName           string `mapstructure:"instance_name" json:"instance_name"`
	APIBase                string `mapstructure:"api_base" json:"api_base"`
	TimeoutSeconds         int    `mapstructure:"timeout_seconds" json:"timeout_seconds"`
	Silent                 bool   `mapstructure:"silent" json:"silent"`
	NotifyEmbyBruteForce   bool   `mapstructure:"notify_emby_brute_force" json:"notify_emby_brute_force"`
	NotifySystemBruteForce bool   `mapstructure:"notify_system_brute_force" json:"notify_system_brute_force"`
}

func (c TelegramConfig) IsZero() bool {
	return !c.Enabled && c.BotToken == "" && c.ChatID == "" && c.MessageThreadID == 0 &&
		c.InstanceName == "" && c.APIBase == "" && c.TimeoutSeconds == 0 && !c.Silent &&
		!c.NotifyEmbyBruteForce && !c.NotifySystemBruteForce
}

type EmbyConfig struct {
	Enabled             bool                        `mapstructure:"enabled" json:"enabled"`                               // 是否启用 EMBY 服务
	URL                 string                      `mapstructure:"url" json:"url"`                                       // EMBY 服务器地址
	APIKey              string                      `mapstructure:"api_key" json:"api_key"`                               // EMBY API 密钥
	AdminUserID         string                      `mapstructure:"admin_user_id" json:"admin_user_id"`                   // EMBY 管理员用户 ID
	CacheTime           int                         `mapstructure:"cache_time" json:"cache_time"`                         // API 请求超时时间（秒）
	AddCurrentMediaInfo bool                        `mapstructure:"add_current_media_info" json:"add_current_media_info"` // 是否在开始播放时补充当前媒体信息
	AddNextMediaInfo    bool                        `mapstructure:"add_next_media_info" json:"add_next_media_info"`       // 是否添加下一部媒体信息
	RunProxyPort        int                         `mapstructure:"run_proxy_port" json:"run_proxy_port"`                 // 运行 Emby 代理端口
	Security            LoginSecurityConfig         `mapstructure:"security" json:"security"`                             // Emby 登录防爆破配置
	Cover               EmbyCoverConfig             `mapstructure:"cover" json:"cover"`                                   // 媒体库封面生成器配置
	ImageOptimization   EmbyImageOptimizationConfig `mapstructure:"image_optimization" json:"image_optimization"`         // Emby 图片尺寸与质量控制
}

// LoginSecurityConfig 控制登录接口的失败计数与临时封禁。
type LoginSecurityConfig struct {
	Enabled                 bool     `mapstructure:"enabled" json:"enabled"`
	WindowMinutes           int      `mapstructure:"window_minutes" json:"window_minutes"`
	MaxFailuresPerAccountIP int      `mapstructure:"max_failures_per_account_ip" json:"max_failures_per_account_ip"`
	MaxFailuresPerIP        int      `mapstructure:"max_failures_per_ip" json:"max_failures_per_ip"`
	BlockMinutes            int      `mapstructure:"block_minutes" json:"block_minutes"`
	TrustedProxyCIDRs       []string `mapstructure:"trusted_proxy_cidrs" json:"trusted_proxy_cidrs"`
}

func (c LoginSecurityConfig) IsZero() bool {
	return !c.Enabled && c.WindowMinutes == 0 && c.MaxFailuresPerAccountIP == 0 &&
		c.MaxFailuresPerIP == 0 && c.BlockMinutes == 0 && len(c.TrustedProxyCIDRs) == 0
}

// EmbySecurityConfig 保留旧名称，兼容现有调用。
type EmbySecurityConfig = LoginSecurityConfig

// EmbyImageRuleConfig 定义某类 Emby 图片的请求上限；0 表示保留客户端参数。
type EmbyImageRuleConfig struct {
	Enabled   bool `mapstructure:"enabled" json:"enabled"`
	MaxWidth  int  `mapstructure:"max_width" json:"max_width"`
	MaxHeight int  `mapstructure:"max_height" json:"max_height"`
	Quality   int  `mapstructure:"quality" json:"quality"`
}

// EmbyImageOptimizationConfig 按 Emby Web 的常见展示场景控制图片请求。
type EmbyImageOptimizationConfig struct {
	Enabled          bool                `mapstructure:"enabled" json:"enabled"`
	LibraryCover     EmbyImageRuleConfig `mapstructure:"library_cover" json:"library_cover"`
	Poster           EmbyImageRuleConfig `mapstructure:"poster" json:"poster"`
	ContinueBackdrop EmbyImageRuleConfig `mapstructure:"continue_backdrop" json:"continue_backdrop"`
	ListPoster       EmbyImageRuleConfig `mapstructure:"list_poster" json:"list_poster"`
	DetailLogo       EmbyImageRuleConfig `mapstructure:"detail_logo" json:"detail_logo"`
	DetailBackdrop   EmbyImageRuleConfig `mapstructure:"detail_backdrop" json:"detail_backdrop"`
	Other            EmbyImageRuleConfig `mapstructure:"other" json:"other"`
}

func (c EmbyImageOptimizationConfig) IsZero() bool {
	return c == (EmbyImageOptimizationConfig{})
}

// EmbyCoverConfig 媒体库封面生成器配置
type EmbyCoverConfig struct {
	Enabled     bool   `mapstructure:"enabled" json:"enabled"`           // 是否启用封面生成功能
	Cron        string `mapstructure:"cron" json:"cron"`                 // cron 表达式，例如 "0 3 * * *" 每天 3 点；为空则禁用定时任务
	Width       int    `mapstructure:"width" json:"width"`               // 输出图宽（默认 1920）
	Height      int    `mapstructure:"height" json:"height"`             // 输出图高（默认 1080）
	JpegQuality int    `mapstructure:"jpeg_quality" json:"jpeg_quality"` // JPEG 输出质量 1-100（默认 88）
	FontCN      string `mapstructure:"font_cn" json:"font_cn"`           // 中文字体绝对路径或相对项目根目录路径
	FontEN      string `mapstructure:"font_en" json:"font_en"`           // 英文字体路径
	PosterCount int    `mapstructure:"poster_count" json:"poster_count"` // 拼接海报数量（默认 9，对应九宫格）
}

type MoviePilotConfig struct {
	API      string `mapstructure:"api" json:"api"`           // MoviePilot API 地址
	Username string `mapstructure:"username" json:"username"` // MoviePilot 用户名
	Password string `mapstructure:"password" json:"password"` // MoviePilot 密码
}

type TMDBConfig struct {
	Enabled        bool   `mapstructure:"enabled" json:"enabled"`                 // 是否启用 TMDB API
	BaseURL        string `mapstructure:"base_url" json:"base_url"`               // TMDB API 地址
	APIKey         string `mapstructure:"api_key" json:"api_key"`                 // TMDB v3 API Key
	AccessToken    string `mapstructure:"access_token" json:"access_token"`       // TMDB Read Access Token
	TimeoutSeconds int    `mapstructure:"timeout_seconds" json:"timeout_seconds"` // 请求超时时间（秒）
	CacheMinutes   int    `mapstructure:"cache_minutes" json:"cache_minutes"`     // 元数据缓存时间（分钟）
}

type HDHiveConfig struct {
	Enabled               bool   `mapstructure:"enabled" json:"enabled"`                                   // 是否启用 HDHive OpenAPI
	BaseURL               string `mapstructure:"base_url" json:"base_url"`                                 // HDHive 服务地址
	ClientID              string `mapstructure:"client_id" json:"client_id"`                               // OpenAPI 应用公开 ID
	RedirectURI           string `mapstructure:"redirect_uri" json:"redirect_uri"`                         // OAuth 回调地址
	Scope                 string `mapstructure:"scope" json:"scope"`                                       // OAuth 授权 scope
	APIKey                string `mapstructure:"api_key" json:"api_key"`                                   // 应用 Secret，用于 X-API-Key
	AccessToken           string `mapstructure:"access_token" json:"access_token"`                         // 用户 Access Token
	RefreshToken          string `mapstructure:"refresh_token" json:"refresh_token"`                       // 用户 Refresh Token
	AccessTokenExpiresAt  string `mapstructure:"access_token_expires_at" json:"access_token_expires_at"`   // Access Token 过期时间（RFC3339）
	RefreshTokenExpiresAt string `mapstructure:"refresh_token_expires_at" json:"refresh_token_expires_at"` // Refresh Token 过期时间（RFC3339）
	AutoRefresh           bool   `mapstructure:"auto_refresh" json:"auto_refresh"`                         // 是否自动刷新 Access Token
	RefreshBeforeMinutes  int    `mapstructure:"refresh_before_minutes" json:"refresh_before_minutes"`     // 过期前多少分钟刷新
	RefreshCheckMinutes   int    `mapstructure:"refresh_check_minutes" json:"refresh_check_minutes"`       // 自动刷新检查间隔
	TimeoutSeconds        int    `mapstructure:"timeout_seconds" json:"timeout_seconds"`                   // 请求超时时间（秒）
}

func Load() *Config {
	setDefaults()

	// 读取配置
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Println("未找到配置文件，使用默认配置")
		} else {
			log.Fatalf("读取配置文件出错: %v", err)
		}
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatalf("无法解码配置: %v", err)
	}
	applySiteDefaults(&config.Site)
	applyEmbyImageOptimizationDefaults(&config.Emby.ImageOptimization)
	applyLoginSecurityDefaults("server.security", &config.Server.Security)
	applyLoginSecurityDefaults("emby.security", &config.Emby.Security)
	applyTelegramDefaults(&config.Telegram)

	// 验证配置
	if err := validateConfig(&config); err != nil {
		log.Fatalf("配置验证失败: %v", err)
	}

	return &config
}

// Save 把内存配置写回 config.yaml（仅覆盖已暴露的键，未暴露键会被保留）。
// 通过全局 viper 设置各键后 WriteConfig，保持原有 yaml 键名与未管理项。
func Save(c *Config) error {
	viper.Set("server.port", c.Server.Port)
	viper.Set("server.username", c.Server.Username)
	viper.Set("server.password", c.Server.Password)
	viper.Set("server.download_115_concurrency", c.Server.Download115Concurrency)
	viper.Set("server.process_new_media", c.Server.ProcessNewMedia)
	setLoginSecurity("server.security", c.Server.Security)

	viper.Set("site.login_title", c.Site.LoginTitle)
	viper.Set("site.login_subtitle", c.Site.LoginSubtitle)
	viper.Set("site.login_form_title", c.Site.LoginFormTitle)
	viper.Set("site.login_form_subtitle", c.Site.LoginFormSubtitle)
	viper.Set("site.login_background_url", c.Site.LoginBackgroundURL)
	viper.Set("site.login_background_source", c.Site.LoginBackgroundSource)
	viper.Set("site.login_background_mode", c.Site.LoginBackgroundMode)
	viper.Set("site.login_background_interval", c.Site.LoginBackgroundInterval)
	viper.Set("site.login_background_limit", c.Site.LoginBackgroundLimit)
	viper.Set("site.footer_text", c.Site.FooterText)
	viper.Set("site.icp_number", c.Site.ICPNumber)
	viper.Set("site.police_number", c.Site.PoliceNumber)

	viper.Set("webhook.clouddrive2.enabled", c.Webhook.CloudDrive2.Enabled)
	viper.Set("webhook.clouddrive2.token", c.Webhook.CloudDrive2.Token)

	viper.Set("log.level", c.Log.Level)
	viper.Set("log.format", c.Log.Format)
	viper.Set("log.output", c.Log.Output)
	viper.Set("log.max_size", c.Log.MaxSize)
	viper.Set("log.max_backups", c.Log.MaxBackups)
	viper.Set("log.max_age", c.Log.MaxAge)
	viper.Set("log.compress", c.Log.Compress)

	viper.Set("jwt.secret", c.JWT.Secret)
	viper.Set("jwt.expire_time", c.JWT.ExpireTime)
	viper.Set("jwt.issuer", c.JWT.Issuer)

	viper.Set("telegram.enabled", c.Telegram.Enabled)
	viper.Set("telegram.bot_token", c.Telegram.BotToken)
	viper.Set("telegram.chat_id", c.Telegram.ChatID)
	viper.Set("telegram.message_thread_id", c.Telegram.MessageThreadID)
	viper.Set("telegram.instance_name", c.Telegram.InstanceName)
	viper.Set("telegram.api_base", c.Telegram.APIBase)
	viper.Set("telegram.timeout_seconds", c.Telegram.TimeoutSeconds)
	viper.Set("telegram.silent", c.Telegram.Silent)
	viper.Set("telegram.notify_emby_brute_force", c.Telegram.NotifyEmbyBruteForce)
	viper.Set("telegram.notify_system_brute_force", c.Telegram.NotifySystemBruteForce)

	viper.Set("emby.enabled", c.Emby.Enabled)
	viper.Set("emby.url", c.Emby.URL)
	viper.Set("emby.api_key", c.Emby.APIKey)
	viper.Set("emby.admin_user_id", c.Emby.AdminUserID)
	viper.Set("emby.cache_time", c.Emby.CacheTime)
	viper.Set("emby.add_current_media_info", c.Emby.AddCurrentMediaInfo)
	viper.Set("emby.add_next_media_info", c.Emby.AddNextMediaInfo)
	viper.Set("emby.run_proxy_port", c.Emby.RunProxyPort)
	setLoginSecurity("emby.security", c.Emby.Security)
	viper.Set("emby.image_optimization.enabled", c.Emby.ImageOptimization.Enabled)
	setEmbyImageRule("emby.image_optimization.library_cover", c.Emby.ImageOptimization.LibraryCover)
	setEmbyImageRule("emby.image_optimization.poster", c.Emby.ImageOptimization.Poster)
	setEmbyImageRule("emby.image_optimization.continue_backdrop", c.Emby.ImageOptimization.ContinueBackdrop)
	setEmbyImageRule("emby.image_optimization.list_poster", c.Emby.ImageOptimization.ListPoster)
	setEmbyImageRule("emby.image_optimization.detail_logo", c.Emby.ImageOptimization.DetailLogo)
	setEmbyImageRule("emby.image_optimization.detail_backdrop", c.Emby.ImageOptimization.DetailBackdrop)
	setEmbyImageRule("emby.image_optimization.other", c.Emby.ImageOptimization.Other)

	viper.Set("emby.cover.enabled", c.Emby.Cover.Enabled)
	viper.Set("emby.cover.cron", c.Emby.Cover.Cron)
	viper.Set("emby.cover.width", c.Emby.Cover.Width)
	viper.Set("emby.cover.height", c.Emby.Cover.Height)
	viper.Set("emby.cover.jpeg_quality", c.Emby.Cover.JpegQuality)
	viper.Set("emby.cover.font_cn", c.Emby.Cover.FontCN)
	viper.Set("emby.cover.font_en", c.Emby.Cover.FontEN)
	viper.Set("emby.cover.poster_count", c.Emby.Cover.PosterCount)

	viper.Set("moviepilot.api", c.MoviePilot.API)
	viper.Set("moviepilot.username", c.MoviePilot.Username)
	viper.Set("moviepilot.password", c.MoviePilot.Password)

	viper.Set("tmdb.enabled", c.TMDB.Enabled)
	viper.Set("tmdb.base_url", c.TMDB.BaseURL)
	viper.Set("tmdb.api_key", c.TMDB.APIKey)
	viper.Set("tmdb.access_token", c.TMDB.AccessToken)
	viper.Set("tmdb.timeout_seconds", c.TMDB.TimeoutSeconds)
	viper.Set("tmdb.cache_minutes", c.TMDB.CacheMinutes)

	viper.Set("hdhive.enabled", c.HDHive.Enabled)
	viper.Set("hdhive.base_url", c.HDHive.BaseURL)
	viper.Set("hdhive.client_id", c.HDHive.ClientID)
	viper.Set("hdhive.redirect_uri", c.HDHive.RedirectURI)
	viper.Set("hdhive.scope", c.HDHive.Scope)
	viper.Set("hdhive.api_key", c.HDHive.APIKey)
	viper.Set("hdhive.access_token", c.HDHive.AccessToken)
	viper.Set("hdhive.refresh_token", c.HDHive.RefreshToken)
	viper.Set("hdhive.access_token_expires_at", c.HDHive.AccessTokenExpiresAt)
	viper.Set("hdhive.refresh_token_expires_at", c.HDHive.RefreshTokenExpiresAt)
	viper.Set("hdhive.auto_refresh", c.HDHive.AutoRefresh)
	viper.Set("hdhive.refresh_before_minutes", c.HDHive.RefreshBeforeMinutes)
	viper.Set("hdhive.refresh_check_minutes", c.HDHive.RefreshCheckMinutes)
	viper.Set("hdhive.timeout_seconds", c.HDHive.TimeoutSeconds)

	if err := viper.WriteConfig(); err != nil {
		// 配置文件不存在时回退到显式路径写入
		path := viper.ConfigFileUsed()
		if path == "" {
			path = "data/config.yaml"
		}
		return viper.WriteConfigAs(path)
	}
	return nil
}

func setEmbyImageRule(prefix string, rule EmbyImageRuleConfig) {
	viper.Set(prefix+".enabled", rule.Enabled)
	viper.Set(prefix+".max_width", rule.MaxWidth)
	viper.Set(prefix+".max_height", rule.MaxHeight)
	viper.Set(prefix+".quality", rule.Quality)
}

func setLoginSecurity(prefix string, settings LoginSecurityConfig) {
	viper.Set(prefix+".enabled", settings.Enabled)
	viper.Set(prefix+".window_minutes", settings.WindowMinutes)
	viper.Set(prefix+".max_failures_per_account_ip", settings.MaxFailuresPerAccountIP)
	viper.Set(prefix+".max_failures_per_ip", settings.MaxFailuresPerIP)
	viper.Set(prefix+".block_minutes", settings.BlockMinutes)
	viper.Set(prefix+".trusted_proxy_cidrs", settings.TrustedProxyCIDRs)
}

// setDefaults 设置默认配置
func setDefaults() {
	viper.SetDefault("server.port", "5000")
	viper.SetDefault("server.process_new_media", true) // 默认启用新媒体处理
	setDefaultLoginSecurity("server.security")

	// 登录页公开展示配置
	viper.SetDefault("site.login_title", DefaultLoginTitle)
	viper.SetDefault("site.login_subtitle", DefaultLoginSubtitle)
	viper.SetDefault("site.login_form_title", DefaultLoginFormTitle)
	viper.SetDefault("site.login_form_subtitle", DefaultLoginFormSubtitle)
	viper.SetDefault("site.login_background_url", "")
	viper.SetDefault("site.login_background_source", DefaultLoginBackgroundSource)
	viper.SetDefault("site.login_background_mode", DefaultLoginBackgroundMode)
	viper.SetDefault("site.login_background_interval", DefaultLoginBackgroundInterval)
	viper.SetDefault("site.login_background_limit", DefaultLoginBackgroundLimit)
	viper.SetDefault("site.footer_text", DefaultFooterText)
	viper.SetDefault("site.icp_number", "")
	viper.SetDefault("site.police_number", "")

	// CloudDrive2 webhook 默认关闭，必须显式设置独立 Token 后开启。
	viper.SetDefault("webhook.clouddrive2.enabled", false)
	viper.SetDefault("webhook.clouddrive2.token", "")

	// Telegram 告警默认配置
	viper.SetDefault("telegram.enabled", false)
	viper.SetDefault("telegram.bot_token", "")
	viper.SetDefault("telegram.chat_id", "")
	viper.SetDefault("telegram.message_thread_id", 0)
	viper.SetDefault("telegram.instance_name", "FilmFusion")
	viper.SetDefault("telegram.api_base", "https://api.telegram.org")
	viper.SetDefault("telegram.timeout_seconds", 10)
	viper.SetDefault("telegram.silent", false)
	viper.SetDefault("telegram.notify_emby_brute_force", true)
	viper.SetDefault("telegram.notify_system_brute_force", true)

	// MoviePilot 默认配置
	viper.SetDefault("moviepilot.api", "http://127.0.0.1:3001")
	viper.SetDefault("moviepilot.username", "")
	viper.SetDefault("moviepilot.password", "")

	// TMDB 默认配置
	viper.SetDefault("tmdb.enabled", false)
	viper.SetDefault("tmdb.base_url", "https://api.themoviedb.org")
	viper.SetDefault("tmdb.api_key", "")
	viper.SetDefault("tmdb.access_token", "")
	viper.SetDefault("tmdb.timeout_seconds", 10)
	viper.SetDefault("tmdb.cache_minutes", 60)

	// HDHive 默认配置
	viper.SetDefault("hdhive.enabled", false)
	viper.SetDefault("hdhive.base_url", "https://hdhive.com")
	viper.SetDefault("hdhive.client_id", "")
	viper.SetDefault("hdhive.redirect_uri", "")
	viper.SetDefault("hdhive.scope", "query unlock")
	viper.SetDefault("hdhive.api_key", "")
	viper.SetDefault("hdhive.access_token", "")
	viper.SetDefault("hdhive.refresh_token", "")
	viper.SetDefault("hdhive.access_token_expires_at", "")
	viper.SetDefault("hdhive.refresh_token_expires_at", "")
	viper.SetDefault("hdhive.auto_refresh", true)
	viper.SetDefault("hdhive.refresh_before_minutes", 15)
	viper.SetDefault("hdhive.refresh_check_minutes", 10)
	viper.SetDefault("hdhive.timeout_seconds", 30)

	// 日志默认配置
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "text")
	viper.SetDefault("log.output", "stdout")
	viper.SetDefault("log.max_size", 100)
	viper.SetDefault("log.max_backups", 3)
	viper.SetDefault("log.max_age", 28)
	viper.SetDefault("log.compress", true)

	// JWT 密钥没有可用默认值。环境变量优先于 config.yaml，适合容器密钥注入。
	viper.SetDefault("jwt.secret", "")
	if err := viper.BindEnv("jwt.secret", "FILM_FUSION_JWT_SECRET"); err != nil {
		panic(fmt.Sprintf("绑定 JWT 密钥环境变量失败: %v", err))
	}
	viper.SetDefault("jwt.expire_time", 24) // 24小时
	viper.SetDefault("jwt.issuer", "film-fusion")

	// Emby 默认配置
	viper.SetDefault("emby.add_current_media_info", true)
	setDefaultLoginSecurity("emby.security")
	viper.SetDefault("emby.image_optimization.enabled", false)
	setDefaultEmbyImageRule("emby.image_optimization.library_cover", true, 676, 380, 80)
	setDefaultEmbyImageRule("emby.image_optimization.poster", true, 356, 534, 80)
	setDefaultEmbyImageRule("emby.image_optimization.continue_backdrop", true, 674, 380, 70)
	setDefaultEmbyImageRule("emby.image_optimization.list_poster", true, 160, 240, 80)
	setDefaultEmbyImageRule("emby.image_optimization.detail_logo", true, 600, 152, 85)
	setDefaultEmbyImageRule("emby.image_optimization.detail_backdrop", true, 1920, 1080, 70)
	setDefaultEmbyImageRule("emby.image_optimization.other", false, 0, 0, 80)

	// Emby Cover 默认配置
	viper.SetDefault("emby.cover.enabled", false)
	viper.SetDefault("emby.cover.cron", "")
	viper.SetDefault("emby.cover.width", 1920)
	viper.SetDefault("emby.cover.height", 1080)
	viper.SetDefault("emby.cover.jpeg_quality", 88)
	viper.SetDefault("emby.cover.font_cn", "data/assets/fonts/SourceHanSansCN-Bold.otf")
	viper.SetDefault("emby.cover.font_en", "data/assets/fonts/Inter-Bold.ttf")
	viper.SetDefault("emby.cover.poster_count", 9)
}

func setDefaultEmbyImageRule(prefix string, enabled bool, maxWidth, maxHeight, quality int) {
	viper.SetDefault(prefix+".enabled", enabled)
	viper.SetDefault(prefix+".max_width", maxWidth)
	viper.SetDefault(prefix+".max_height", maxHeight)
	viper.SetDefault(prefix+".quality", quality)
}

func defaultEmbyImageOptimizationConfig() EmbyImageOptimizationConfig {
	return EmbyImageOptimizationConfig{
		LibraryCover:     EmbyImageRuleConfig{Enabled: true, MaxWidth: 676, MaxHeight: 380, Quality: 80},
		Poster:           EmbyImageRuleConfig{Enabled: true, MaxWidth: 356, MaxHeight: 534, Quality: 80},
		ContinueBackdrop: EmbyImageRuleConfig{Enabled: true, MaxWidth: 674, MaxHeight: 380, Quality: 70},
		ListPoster:       EmbyImageRuleConfig{Enabled: true, MaxWidth: 160, MaxHeight: 240, Quality: 80},
		DetailLogo:       EmbyImageRuleConfig{Enabled: true, MaxWidth: 600, MaxHeight: 152, Quality: 85},
		DetailBackdrop:   EmbyImageRuleConfig{Enabled: true, MaxWidth: 1920, MaxHeight: 1080, Quality: 70},
		Other:            EmbyImageRuleConfig{Enabled: false, Quality: 80},
	}
}

func defaultLoginSecurityConfig() LoginSecurityConfig {
	return LoginSecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 5,
		MaxFailuresPerIP: 20, BlockMinutes: 30, TrustedProxyCIDRs: []string{},
	}
}

func setDefaultLoginSecurity(prefix string) {
	viper.SetDefault(prefix+".enabled", true)
	viper.SetDefault(prefix+".window_minutes", 10)
	viper.SetDefault(prefix+".max_failures_per_account_ip", 5)
	viper.SetDefault(prefix+".max_failures_per_ip", 20)
	viper.SetDefault(prefix+".block_minutes", 30)
	viper.SetDefault(prefix+".trusted_proxy_cidrs", []string{})
}

func applyLoginSecurityDefaults(prefix string, settings *LoginSecurityConfig) {
	defaults := defaultLoginSecurityConfig()
	if settings.IsZero() && !viper.InConfig(prefix) {
		*settings = defaults
		return
	}
	if !viper.InConfig(prefix + ".enabled") {
		settings.Enabled = defaults.Enabled
	}
	if !viper.InConfig(prefix + ".window_minutes") {
		settings.WindowMinutes = defaults.WindowMinutes
	}
	if !viper.InConfig(prefix + ".max_failures_per_account_ip") {
		settings.MaxFailuresPerAccountIP = defaults.MaxFailuresPerAccountIP
	}
	if !viper.InConfig(prefix + ".max_failures_per_ip") {
		settings.MaxFailuresPerIP = defaults.MaxFailuresPerIP
	}
	if !viper.InConfig(prefix + ".block_minutes") {
		settings.BlockMinutes = defaults.BlockMinutes
	}
	if settings.TrustedProxyCIDRs == nil {
		settings.TrustedProxyCIDRs = []string{}
	}
}

func defaultTelegramConfig() TelegramConfig {
	return TelegramConfig{
		InstanceName: "FilmFusion", APIBase: "https://api.telegram.org", TimeoutSeconds: 10,
		NotifyEmbyBruteForce: true, NotifySystemBruteForce: true,
	}
}

func applyTelegramDefaults(settings *TelegramConfig) {
	defaults := defaultTelegramConfig()
	if settings.IsZero() && !viper.InConfig("telegram") {
		*settings = defaults
		return
	}
	if !viper.InConfig("telegram.instance_name") {
		settings.InstanceName = defaults.InstanceName
	}
	if !viper.InConfig("telegram.api_base") {
		settings.APIBase = defaults.APIBase
	}
	if !viper.InConfig("telegram.timeout_seconds") {
		settings.TimeoutSeconds = defaults.TimeoutSeconds
	}
	if !viper.InConfig("telegram.notify_emby_brute_force") {
		settings.NotifyEmbyBruteForce = defaults.NotifyEmbyBruteForce
	}
	if !viper.InConfig("telegram.notify_system_brute_force") {
		settings.NotifySystemBruteForce = defaults.NotifySystemBruteForce
	}
}

// Viper does not reliably unmarshal nested defaults into a struct when the
// parent map is absent from an existing config file. Fill only keys that were
// not explicitly provided so existing zero values keep their meaning.
func applyEmbyImageOptimizationDefaults(settings *EmbyImageOptimizationConfig) {
	defaults := defaultEmbyImageOptimizationConfig()
	if settings.IsZero() {
		*settings = defaults
		return
	}
	if !viper.InConfig("emby.image_optimization.enabled") {
		settings.Enabled = defaults.Enabled
	}
	applyEmbyImageRuleDefaults("emby.image_optimization.library_cover", &settings.LibraryCover, defaults.LibraryCover)
	applyEmbyImageRuleDefaults("emby.image_optimization.poster", &settings.Poster, defaults.Poster)
	applyEmbyImageRuleDefaults("emby.image_optimization.continue_backdrop", &settings.ContinueBackdrop, defaults.ContinueBackdrop)
	applyEmbyImageRuleDefaults("emby.image_optimization.list_poster", &settings.ListPoster, defaults.ListPoster)
	applyEmbyImageRuleDefaults("emby.image_optimization.detail_logo", &settings.DetailLogo, defaults.DetailLogo)
	applyEmbyImageRuleDefaults("emby.image_optimization.detail_backdrop", &settings.DetailBackdrop, defaults.DetailBackdrop)
	applyEmbyImageRuleDefaults("emby.image_optimization.other", &settings.Other, defaults.Other)
}

func applyEmbyImageRuleDefaults(prefix string, rule *EmbyImageRuleConfig, defaults EmbyImageRuleConfig) {
	if !viper.InConfig(prefix + ".enabled") {
		rule.Enabled = defaults.Enabled
	}
	if !viper.InConfig(prefix + ".max_width") {
		rule.MaxWidth = defaults.MaxWidth
	}
	if !viper.InConfig(prefix + ".max_height") {
		rule.MaxHeight = defaults.MaxHeight
	}
	if !viper.InConfig(prefix + ".quality") {
		rule.Quality = defaults.Quality
	}
}

func applySiteDefaults(settings *SiteConfig) {
	if strings.TrimSpace(settings.LoginTitle) == "" {
		settings.LoginTitle = DefaultLoginTitle
	}
	if strings.TrimSpace(settings.LoginSubtitle) == "" {
		settings.LoginSubtitle = DefaultLoginSubtitle
	}
	if strings.TrimSpace(settings.LoginFormTitle) == "" {
		settings.LoginFormTitle = DefaultLoginFormTitle
	}
	if strings.TrimSpace(settings.LoginFormSubtitle) == "" {
		settings.LoginFormSubtitle = DefaultLoginFormSubtitle
	}
	if strings.TrimSpace(settings.LoginBackgroundSource) == "" {
		settings.LoginBackgroundSource = DefaultLoginBackgroundSource
	}
	if strings.TrimSpace(settings.LoginBackgroundMode) == "" {
		settings.LoginBackgroundMode = DefaultLoginBackgroundMode
	}
	if settings.LoginBackgroundInterval <= 0 {
		settings.LoginBackgroundInterval = DefaultLoginBackgroundInterval
	}
	if settings.LoginBackgroundLimit <= 0 {
		settings.LoginBackgroundLimit = DefaultLoginBackgroundLimit
	}
}

// validateConfig 验证配置的有效性
func validateConfig(config *Config) error {
	if config.Server.Port == "" {
		return fmt.Errorf("服务器端口未设置")
	}
	if err := ValidateSite(config.Site); err != nil {
		return err
	}
	if err := ValidateJWTSecret(config.JWT.Secret); err != nil {
		return err
	}
	if err := ValidateLoginSecurity("FilmFusion", config.Server.Security); err != nil {
		return err
	}
	if err := ValidateEmbySecurity(config.Emby.Security); err != nil {
		return err
	}
	if err := ValidateTelegram(config.Telegram); err != nil {
		return err
	}
	if err := ValidateWebhook(config.Webhook); err != nil {
		return err
	}
	return nil
}

func ValidateSite(settings SiteConfig) error {
	if strings.TrimSpace(settings.LoginTitle) == "" {
		return fmt.Errorf("登录页内容标题不能为空")
	}
	if strings.TrimSpace(settings.LoginSubtitle) == "" {
		return fmt.Errorf("登录页内容副标题不能为空")
	}
	if strings.TrimSpace(settings.LoginFormTitle) == "" {
		return fmt.Errorf("登录表单主标题不能为空")
	}
	if strings.TrimSpace(settings.LoginFormSubtitle) == "" {
		return fmt.Errorf("登录表单说明不能为空")
	}
	backgroundURL := strings.TrimSpace(settings.LoginBackgroundURL)
	if backgroundURL != "" {
		parsed, err := url.ParseRequestURI(backgroundURL)
		if err != nil {
			return fmt.Errorf("登录页背景图片 URL 无效")
		}
		if parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("登录页背景图片 URL 仅支持 HTTP 或 HTTPS")
		}
	}
	backgroundSource := strings.ToLower(strings.TrimSpace(settings.LoginBackgroundSource))
	if backgroundSource == "" {
		backgroundSource = DefaultLoginBackgroundSource
	}
	switch backgroundSource {
	case "custom", "emby", "tmdb":
	default:
		return fmt.Errorf("登录页背景来源仅支持 custom、emby 或 tmdb")
	}
	backgroundMode := strings.ToLower(strings.TrimSpace(settings.LoginBackgroundMode))
	if backgroundMode == "" {
		backgroundMode = DefaultLoginBackgroundMode
	}
	switch backgroundMode {
	case "latest", "popular":
	default:
		return fmt.Errorf("登录页背景内容仅支持 latest 或 popular")
	}
	backgroundInterval := settings.LoginBackgroundInterval
	if backgroundInterval == 0 {
		backgroundInterval = DefaultLoginBackgroundInterval
	}
	if backgroundInterval < 5 || backgroundInterval > 300 {
		return fmt.Errorf("登录页背景轮播间隔需在 5 到 300 秒之间")
	}
	backgroundLimit := settings.LoginBackgroundLimit
	if backgroundLimit == 0 {
		backgroundLimit = DefaultLoginBackgroundLimit
	}
	if backgroundLimit < 1 || backgroundLimit > 20 {
		return fmt.Errorf("登录页背景数量需在 1 到 20 张之间")
	}
	return nil
}

// ValidateWebhook 验证 webhook 鉴权配置。32 字符下限用于避免误填短口令。
func ValidateWebhook(settings WebhookConfig) error {
	if settings.CloudDrive2.Enabled && len(strings.TrimSpace(settings.CloudDrive2.Token)) < 32 {
		return fmt.Errorf("CloudDrive2 Webhook 鉴权启用时 Token 至少需要 32 个字符")
	}
	return nil
}

// ValidateJWTSecret rejects missing, publicly known, and undersized HMAC keys.
// HS256 requires at least 256 bits of key material, represented here as 32 bytes.
func ValidateJWTSecret(secret string) error {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return fmt.Errorf("JWT 密钥未设置；请配置 jwt.secret 或 FILM_FUSION_JWT_SECRET")
	}
	if _, known := insecureJWTSecrets[trimmed]; known {
		return fmt.Errorf("JWT 密钥使用了公开示例值；请更换为随机生成的独立密钥")
	}
	if len([]byte(trimmed)) < minJWTSecretBytes {
		return fmt.Errorf("JWT 密钥过短：至少需要 %d 字节的随机密钥", minJWTSecretBytes)
	}
	return nil
}

func ValidateEmbySecurity(settings EmbySecurityConfig) error {
	return ValidateLoginSecurity("Emby", settings)
}

func ValidateLoginSecurity(name string, settings LoginSecurityConfig) error {
	if settings.Enabled && (settings.WindowMinutes <= 0 || settings.MaxFailuresPerAccountIP <= 0 || settings.MaxFailuresPerIP <= 0 || settings.BlockMinutes <= 0) {
		return fmt.Errorf("%s 登录保护的统计窗口、失败阈值和封禁时长必须大于 0", name)
	}
	for _, raw := range settings.TrustedProxyCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, err := netip.ParsePrefix(raw); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(raw); err == nil {
			continue
		}
		return fmt.Errorf("可信代理 IP/CIDR 无效: %s", raw)
	}
	return nil
}

func ValidateTelegram(settings TelegramConfig) error {
	if settings.MessageThreadID < 0 {
		return fmt.Errorf("Telegram 话题 ID 不能小于 0")
	}
	if !settings.Enabled {
		return nil
	}
	if strings.TrimSpace(settings.BotToken) == "" || strings.ContainsAny(settings.BotToken, "/\\\r\n\t ") {
		return fmt.Errorf("Telegram Bot Token 未设置或格式无效")
	}
	if strings.TrimSpace(settings.ChatID) == "" {
		return fmt.Errorf("Telegram Chat ID 不能为空")
	}
	if settings.TimeoutSeconds <= 0 {
		return fmt.Errorf("Telegram 请求超时必须大于 0")
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(settings.APIBase))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("Telegram API 地址无效")
	}
	return nil
}
