package config

import (
	"fmt"
	"log"
	"net/netip"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server" json:"server"`
	Log        LogConfig        `mapstructure:"log" json:"log"`
	JWT        JWTConfig        `mapstructure:"jwt" json:"jwt"`
	Emby       EmbyConfig       `mapstructure:"emby" json:"emby"`
	MoviePilot MoviePilotConfig `mapstructure:"moviepilot" json:"moviepilot"`
	TMDB       TMDBConfig       `mapstructure:"tmdb" json:"tmdb"`
	HDHive     HDHiveConfig     `mapstructure:"hdhive" json:"hdhive"`
}

type ServerConfig struct {
	Port                   string `mapstructure:"port" json:"port"`
	Username               string `mapstructure:"username" json:"username"`
	Password               string `mapstructure:"password" json:"password"`
	Download115Concurrency int    `mapstructure:"download_115_concurrency" json:"download_115_concurrency"`
	ProcessNewMedia        bool   `mapstructure:"process_new_media" json:"process_new_media"` // 是否处理新增媒体事件
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

type EmbyConfig struct {
	Enabled             bool                        `mapstructure:"enabled" json:"enabled"`                               // 是否启用 EMBY 服务
	URL                 string                      `mapstructure:"url" json:"url"`                                       // EMBY 服务器地址
	APIKey              string                      `mapstructure:"api_key" json:"api_key"`                               // EMBY API 密钥
	AdminUserID         string                      `mapstructure:"admin_user_id" json:"admin_user_id"`                   // EMBY 管理员用户 ID
	CacheTime           int                         `mapstructure:"cache_time" json:"cache_time"`                         // API 请求超时时间（秒）
	AddCurrentMediaInfo bool                        `mapstructure:"add_current_media_info" json:"add_current_media_info"` // 是否在开始播放时补充当前媒体信息
	AddNextMediaInfo    bool                        `mapstructure:"add_next_media_info" json:"add_next_media_info"`       // 是否添加下一部媒体信息
	RunProxyPort        int                         `mapstructure:"run_proxy_port" json:"run_proxy_port"`                 // 运行 Emby 代理端口
	Security            EmbySecurityConfig          `mapstructure:"security" json:"security"`                             // Emby 登录防爆破配置
	Cover               EmbyCoverConfig             `mapstructure:"cover" json:"cover"`                                   // 媒体库封面生成器配置
	ImageOptimization   EmbyImageOptimizationConfig `mapstructure:"image_optimization" json:"image_optimization"`         // Emby 图片尺寸与质量控制
}

// EmbySecurityConfig 控制 Emby 代理登录接口的失败计数与临时封禁。
type EmbySecurityConfig struct {
	Enabled                 bool     `mapstructure:"enabled" json:"enabled"`
	WindowMinutes           int      `mapstructure:"window_minutes" json:"window_minutes"`
	MaxFailuresPerAccountIP int      `mapstructure:"max_failures_per_account_ip" json:"max_failures_per_account_ip"`
	MaxFailuresPerIP        int      `mapstructure:"max_failures_per_ip" json:"max_failures_per_ip"`
	BlockMinutes            int      `mapstructure:"block_minutes" json:"block_minutes"`
	TrustedProxyCIDRs       []string `mapstructure:"trusted_proxy_cidrs" json:"trusted_proxy_cidrs"`
}

func (c EmbySecurityConfig) IsZero() bool {
	return !c.Enabled && c.WindowMinutes == 0 && c.MaxFailuresPerAccountIP == 0 &&
		c.MaxFailuresPerIP == 0 && c.BlockMinutes == 0 && len(c.TrustedProxyCIDRs) == 0
}

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
	applyEmbyImageOptimizationDefaults(&config.Emby.ImageOptimization)
	applyEmbySecurityDefaults(&config.Emby.Security)

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

	viper.Set("emby.enabled", c.Emby.Enabled)
	viper.Set("emby.url", c.Emby.URL)
	viper.Set("emby.api_key", c.Emby.APIKey)
	viper.Set("emby.admin_user_id", c.Emby.AdminUserID)
	viper.Set("emby.cache_time", c.Emby.CacheTime)
	viper.Set("emby.add_current_media_info", c.Emby.AddCurrentMediaInfo)
	viper.Set("emby.add_next_media_info", c.Emby.AddNextMediaInfo)
	viper.Set("emby.run_proxy_port", c.Emby.RunProxyPort)
	viper.Set("emby.security.enabled", c.Emby.Security.Enabled)
	viper.Set("emby.security.window_minutes", c.Emby.Security.WindowMinutes)
	viper.Set("emby.security.max_failures_per_account_ip", c.Emby.Security.MaxFailuresPerAccountIP)
	viper.Set("emby.security.max_failures_per_ip", c.Emby.Security.MaxFailuresPerIP)
	viper.Set("emby.security.block_minutes", c.Emby.Security.BlockMinutes)
	viper.Set("emby.security.trusted_proxy_cidrs", c.Emby.Security.TrustedProxyCIDRs)
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

// setDefaults 设置默认配置
func setDefaults() {
	viper.SetDefault("server.port", "5000")
	viper.SetDefault("server.process_new_media", true) // 默认启用新媒体处理

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

	// JWT默认配置
	viper.SetDefault("jwt.secret", "your-secret-key-change-in-production")
	viper.SetDefault("jwt.expire_time", 24) // 24小时
	viper.SetDefault("jwt.issuer", "film-fusion")

	// Emby 默认配置
	viper.SetDefault("emby.add_current_media_info", true)
	viper.SetDefault("emby.security.enabled", true)
	viper.SetDefault("emby.security.window_minutes", 10)
	viper.SetDefault("emby.security.max_failures_per_account_ip", 5)
	viper.SetDefault("emby.security.max_failures_per_ip", 20)
	viper.SetDefault("emby.security.block_minutes", 30)
	viper.SetDefault("emby.security.trusted_proxy_cidrs", []string{})
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

func defaultEmbySecurityConfig() EmbySecurityConfig {
	return EmbySecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 5,
		MaxFailuresPerIP: 20, BlockMinutes: 30, TrustedProxyCIDRs: []string{},
	}
}

func applyEmbySecurityDefaults(settings *EmbySecurityConfig) {
	defaults := defaultEmbySecurityConfig()
	if settings.IsZero() && !viper.InConfig("emby.security") {
		*settings = defaults
		return
	}
	if !viper.InConfig("emby.security.enabled") {
		settings.Enabled = defaults.Enabled
	}
	if !viper.InConfig("emby.security.window_minutes") {
		settings.WindowMinutes = defaults.WindowMinutes
	}
	if !viper.InConfig("emby.security.max_failures_per_account_ip") {
		settings.MaxFailuresPerAccountIP = defaults.MaxFailuresPerAccountIP
	}
	if !viper.InConfig("emby.security.max_failures_per_ip") {
		settings.MaxFailuresPerIP = defaults.MaxFailuresPerIP
	}
	if !viper.InConfig("emby.security.block_minutes") {
		settings.BlockMinutes = defaults.BlockMinutes
	}
	if settings.TrustedProxyCIDRs == nil {
		settings.TrustedProxyCIDRs = []string{}
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

// validateConfig 验证配置的有效性
func validateConfig(config *Config) error {
	if config.Server.Port == "" {
		return fmt.Errorf("服务器端口未设置")
	}
	if config.JWT.Secret == "" {
		return fmt.Errorf("JWT密钥未设置")
	}
	if err := ValidateEmbySecurity(config.Emby.Security); err != nil {
		return err
	}
	return nil
}

func ValidateEmbySecurity(settings EmbySecurityConfig) error {
	if settings.Enabled && (settings.WindowMinutes <= 0 || settings.MaxFailuresPerAccountIP <= 0 || settings.MaxFailuresPerIP <= 0 || settings.BlockMinutes <= 0) {
		return fmt.Errorf("Emby 登录保护的统计窗口、失败阈值和封禁时长必须大于 0")
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
