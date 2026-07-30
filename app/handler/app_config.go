package handler

import (
	"net/http"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AppConfigHandler 提供 config.yaml 的在线读取/编辑与热重载。
// 共享同一 *config.Config 指针：保存后就地更新该结构体，使按需读取配置的逻辑立即生效；
// 同时重建 Emby 客户端、重排封面 cron；端口/日志等启动期绑定项标注「需重启」。
type AppConfigHandler struct {
	logger        *logger.Logger
	cfg           *config.Config
	emby          *embyhelper.EmbyClient
	coverSvc      *service.EmbyCoverService
	tmdbSvc       *service.TMDBService
	siteAssetsDir string
	db            *gorm.DB
	backgrounds   *loginBackgroundCache
}

// NewAppConfigHandler 构造
func NewAppConfigHandler(log *logger.Logger, cfg *config.Config, emby *embyhelper.EmbyClient, coverSvc *service.EmbyCoverService, tmdbServices ...*service.TMDBService) *AppConfigHandler {
	handler := &AppConfigHandler{
		logger:        log,
		cfg:           cfg,
		emby:          emby,
		coverSvc:      coverSvc,
		siteAssetsDir: defaultSiteAssetsDir,
		db:            database.GetDB(),
		backgrounds:   &loginBackgroundCache{},
	}
	if len(tmdbServices) > 0 {
		handler.tmdbSvc = tmdbServices[0]
	}
	return handler
}

func (h *AppConfigHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *AppConfigHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// GetPublic GET /api/public-config 仅返回登录前可安全展示的站点配置。
func (h *AppConfigHandler) GetPublic(c *gin.Context) {
	site := h.currentSiteConfig()
	title := strings.TrimSpace(site.LoginTitle)
	if title == "" {
		title = config.DefaultLoginTitle
	}
	subtitle := strings.TrimSpace(site.LoginSubtitle)
	if subtitle == "" {
		subtitle = config.DefaultLoginSubtitle
	}
	formTitle := strings.TrimSpace(site.LoginFormTitle)
	if formTitle == "" {
		formTitle = config.DefaultLoginFormTitle
	}
	formSubtitle := strings.TrimSpace(site.LoginFormSubtitle)
	if formSubtitle == "" {
		formSubtitle = config.DefaultLoginFormSubtitle
	}
	backgrounds := h.resolveLoginBackgrounds(c.Request.Context(), site)
	h.success(c, gin.H{
		"login_title":               title,
		"login_subtitle":            subtitle,
		"login_form_title":          formTitle,
		"login_form_subtitle":       formSubtitle,
		"login_background_url":      strings.TrimSpace(site.LoginBackgroundURL),
		"login_background_source":   normalizeLoginBackgroundSource(site.LoginBackgroundSource),
		"login_background_mode":     normalizeLoginBackgroundMode(site.LoginBackgroundMode),
		"login_background_interval": normalizeLoginBackgroundInterval(site.LoginBackgroundInterval),
		"login_backgrounds":         backgrounds,
		"footer_text":               strings.TrimSpace(site.FooterText),
		"icp_number":                strings.TrimSpace(site.ICPNumber),
		"police_number":             strings.TrimSpace(site.PoliceNumber),
	}, "获取公开配置成功")
}

// Get GET /api/app-config 返回当前配置（密钥脱敏，仅返回是否已设置）。
func (h *AppConfigHandler) Get(c *gin.Context) {
	v := *h.cfg // 浅拷贝；仅清空字符串密钥，不影响原配置
	v.Site = h.currentSiteConfig()
	secrets := gin.H{
		"server.password":           h.cfg.Server.Password != "",
		"webhook.clouddrive2.token": h.cfg.Webhook.CloudDrive2.Token != "",
		"emby.api_key":              h.cfg.Emby.APIKey != "",
		"moviepilot.password":       h.cfg.MoviePilot.Password != "",
		"tmdb.api_key":              h.cfg.TMDB.APIKey != "",
		"tmdb.access_token":         h.cfg.TMDB.AccessToken != "",
		"telegram.bot_token":        h.cfg.Telegram.BotToken != "",
		"hdhive.api_key":            h.cfg.HDHive.APIKey != "",
		"hdhive.access_token":       h.cfg.HDHive.AccessToken != "",
		"hdhive.refresh_token":      h.cfg.HDHive.RefreshToken != "",
	}
	v.Server.Password = ""
	v.Webhook.CloudDrive2.Token = ""
	v.Emby.APIKey = ""
	v.MoviePilot.Password = ""
	v.TMDB.APIKey = ""
	v.TMDB.AccessToken = ""
	v.Telegram.BotToken = ""
	v.HDHive.APIKey = ""
	v.HDHive.AccessToken = ""
	v.HDHive.RefreshToken = ""

	h.success(c, gin.H{"config": v, "secrets": secrets}, "获取配置成功")
}

type appConfigUpdatePayload struct {
	Config config.Config `json:"config"`
}

// Update PUT /api/app-config 保存配置到 config.yaml 并尽量热重载。
func (h *AppConfigHandler) Update(c *gin.Context) {
	var payload appConfigUpdatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	in := payload.Config
	// 系统设置页不编辑图片优化子配置，缺省时沿用专用页面保存的值。
	if in.Emby.ImageOptimization.IsZero() {
		in.Emby.ImageOptimization = h.cfg.Emby.ImageOptimization
	}
	if in.Emby.Security.IsZero() {
		in.Emby.Security = h.cfg.Emby.Security
	}
	if in.Server.Security.IsZero() {
		in.Server.Security = h.cfg.Server.Security
	}
	if in.Telegram.IsZero() {
		in.Telegram = h.cfg.Telegram
	}

	// 密钥脱敏：前端留空表示沿用旧值
	if strings.TrimSpace(in.Server.Password) == "" {
		in.Server.Password = h.cfg.Server.Password
	}
	if strings.TrimSpace(in.Webhook.CloudDrive2.Token) == "" {
		in.Webhook.CloudDrive2.Token = h.cfg.Webhook.CloudDrive2.Token
	}
	// JWT 签名由服务内部管理，不接受 API 输入。
	in.JWT.Secret = h.cfg.JWT.Secret
	if strings.TrimSpace(in.Emby.APIKey) == "" {
		in.Emby.APIKey = h.cfg.Emby.APIKey
	}
	if strings.TrimSpace(in.MoviePilot.Password) == "" {
		in.MoviePilot.Password = h.cfg.MoviePilot.Password
	}
	if strings.TrimSpace(in.TMDB.APIKey) == "" {
		in.TMDB.APIKey = h.cfg.TMDB.APIKey
	}
	if strings.TrimSpace(in.TMDB.AccessToken) == "" {
		in.TMDB.AccessToken = h.cfg.TMDB.AccessToken
	}
	if strings.TrimSpace(in.Telegram.BotToken) == "" {
		in.Telegram.BotToken = h.cfg.Telegram.BotToken
	}
	if strings.TrimSpace(in.HDHive.APIKey) == "" {
		in.HDHive.APIKey = h.cfg.HDHive.APIKey
	}
	if strings.TrimSpace(in.HDHive.AccessToken) == "" {
		in.HDHive.AccessToken = h.cfg.HDHive.AccessToken
	}
	if strings.TrimSpace(in.HDHive.RefreshToken) == "" {
		in.HDHive.RefreshToken = h.cfg.HDHive.RefreshToken
	}
	if strings.TrimSpace(in.HDHive.AccessTokenExpiresAt) == "" {
		in.HDHive.AccessTokenExpiresAt = h.cfg.HDHive.AccessTokenExpiresAt
	}
	if strings.TrimSpace(in.HDHive.RefreshTokenExpiresAt) == "" {
		in.HDHive.RefreshTokenExpiresAt = h.cfg.HDHive.RefreshTokenExpiresAt
	}
	if in.HDHive.RefreshBeforeMinutes <= 0 {
		in.HDHive.RefreshBeforeMinutes = h.cfg.HDHive.RefreshBeforeMinutes
		if in.HDHive.RefreshBeforeMinutes <= 0 {
			in.HDHive.RefreshBeforeMinutes = 15
		}
	}
	if in.HDHive.RefreshCheckMinutes <= 0 {
		in.HDHive.RefreshCheckMinutes = h.cfg.HDHive.RefreshCheckMinutes
		if in.HDHive.RefreshCheckMinutes <= 0 {
			in.HDHive.RefreshCheckMinutes = 10
		}
	}
	// 基本校验
	if strings.TrimSpace(in.Server.Port) == "" {
		h.error(c, http.StatusBadRequest, 400, "服务器端口不能为空")
		return
	}
	// 兼容未携带 site 字段的旧版前端。
	currentSite := h.currentSiteConfig()
	if siteConfigIsEmpty(in.Site) {
		in.Site = currentSite
	}
	if strings.TrimSpace(in.Site.LoginTitle) == "" {
		in.Site.LoginTitle = currentSite.LoginTitle
		if strings.TrimSpace(in.Site.LoginTitle) == "" {
			in.Site.LoginTitle = config.DefaultLoginTitle
		}
	}
	if strings.TrimSpace(in.Site.LoginSubtitle) == "" {
		in.Site.LoginSubtitle = currentSite.LoginSubtitle
		if strings.TrimSpace(in.Site.LoginSubtitle) == "" {
			in.Site.LoginSubtitle = config.DefaultLoginSubtitle
		}
	}
	if strings.TrimSpace(in.Site.LoginFormTitle) == "" {
		in.Site.LoginFormTitle = currentSite.LoginFormTitle
		if strings.TrimSpace(in.Site.LoginFormTitle) == "" {
			in.Site.LoginFormTitle = config.DefaultLoginFormTitle
		}
	}
	if strings.TrimSpace(in.Site.LoginFormSubtitle) == "" {
		in.Site.LoginFormSubtitle = currentSite.LoginFormSubtitle
		if strings.TrimSpace(in.Site.LoginFormSubtitle) == "" {
			in.Site.LoginFormSubtitle = config.DefaultLoginFormSubtitle
		}
	}
	in.Site.LoginTitle = strings.TrimSpace(in.Site.LoginTitle)
	in.Site.LoginSubtitle = strings.TrimSpace(in.Site.LoginSubtitle)
	in.Site.LoginFormTitle = strings.TrimSpace(in.Site.LoginFormTitle)
	in.Site.LoginFormSubtitle = strings.TrimSpace(in.Site.LoginFormSubtitle)
	in.Site.LoginBackgroundURL = strings.TrimSpace(in.Site.LoginBackgroundURL)
	if strings.TrimSpace(in.Site.LoginBackgroundSource) == "" {
		in.Site.LoginBackgroundSource = currentSite.LoginBackgroundSource
	}
	if strings.TrimSpace(in.Site.LoginBackgroundSource) == "" {
		in.Site.LoginBackgroundSource = config.DefaultLoginBackgroundSource
	}
	if strings.TrimSpace(in.Site.LoginBackgroundMode) == "" {
		in.Site.LoginBackgroundMode = currentSite.LoginBackgroundMode
	}
	if strings.TrimSpace(in.Site.LoginBackgroundMode) == "" {
		in.Site.LoginBackgroundMode = config.DefaultLoginBackgroundMode
	}
	if in.Site.LoginBackgroundInterval <= 0 {
		in.Site.LoginBackgroundInterval = currentSite.LoginBackgroundInterval
	}
	if in.Site.LoginBackgroundInterval <= 0 {
		in.Site.LoginBackgroundInterval = config.DefaultLoginBackgroundInterval
	}
	if in.Site.LoginBackgroundLimit <= 0 {
		in.Site.LoginBackgroundLimit = currentSite.LoginBackgroundLimit
	}
	if in.Site.LoginBackgroundLimit <= 0 {
		in.Site.LoginBackgroundLimit = config.DefaultLoginBackgroundLimit
	}
	in.Site.LoginBackgroundSource = strings.ToLower(strings.TrimSpace(in.Site.LoginBackgroundSource))
	in.Site.LoginBackgroundMode = strings.ToLower(strings.TrimSpace(in.Site.LoginBackgroundMode))
	in.Site.FooterText = strings.TrimSpace(in.Site.FooterText)
	in.Site.ICPNumber = strings.TrimSpace(in.Site.ICPNumber)
	in.Site.PoliceNumber = strings.TrimSpace(in.Site.PoliceNumber)
	if err := config.ValidateSite(in.Site); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := config.ValidateEmbySecurity(in.Emby.Security); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := config.ValidateLoginSecurity("FilmFusion", in.Server.Security); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := config.ValidateTelegram(in.Telegram); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := config.ValidateWebhook(in.Webhook); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 计算需重启才能生效的变更项
	restart := make([]string, 0, 4)
	if in.Server.Port != h.cfg.Server.Port {
		restart = append(restart, "HTTP 端口")
	}
	if in.Emby.RunProxyPort != h.cfg.Emby.RunProxyPort {
		restart = append(restart, "Emby 代理端口")
	}
	if in.Emby.Enabled != h.cfg.Emby.Enabled {
		restart = append(restart, "Emby 代理启用开关")
	}
	// 日志级别可热生效；格式/输出/轮转属于启动期结构，需重启
	if in.Log.Format != h.cfg.Log.Format ||
		in.Log.Output != h.cfg.Log.Output ||
		in.Log.MaxSize != h.cfg.Log.MaxSize ||
		in.Log.MaxBackups != h.cfg.Log.MaxBackups ||
		in.Log.MaxAge != h.cfg.Log.MaxAge ||
		in.Log.Compress != h.cfg.Log.Compress {
		restart = append(restart, "日志格式/输出/轮转")
	}
	if in.Server.Download115Concurrency != h.cfg.Server.Download115Concurrency {
		restart = append(restart, "115 下载并发数")
	}

	// 外观配置写入 system_configs，同时镜像到 config.yaml 作为兼容备份。
	if err := h.saveConfigAndSiteSettings(&in); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "写入配置失败: "+err.Error())
		return
	}
	// 就地更新共享配置，使按需读取 cfg 的逻辑立即生效
	*h.cfg = in
	// 热重载关键组件：Emby 连接、封面 cron、日志级别
	h.emby.Reload()
	h.coverSvc.Restart()
	h.logger.SetLevel(in.Log.Level)

	h.logger.Infof("[app-config] 配置已更新并热重载，需重启项: %v", restart)
	h.success(c, gin.H{"restart_fields": restart}, "保存成功")
}
