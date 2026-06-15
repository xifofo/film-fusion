package handler

import (
	"net/http"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
)

// AppConfigHandler 提供 config.yaml 的在线读取/编辑与热重载。
// 共享同一 *config.Config 指针：保存后就地更新该结构体，使按需读取配置的逻辑立即生效；
// 同时重建 Emby 客户端、重排封面 cron；端口/JWT/日志等启动期绑定项标注「需重启」。
type AppConfigHandler struct {
	logger   *logger.Logger
	cfg      *config.Config
	emby     *embyhelper.EmbyClient
	coverSvc *service.EmbyCoverService
}

// NewAppConfigHandler 构造
func NewAppConfigHandler(log *logger.Logger, cfg *config.Config, emby *embyhelper.EmbyClient, coverSvc *service.EmbyCoverService) *AppConfigHandler {
	return &AppConfigHandler{logger: log, cfg: cfg, emby: emby, coverSvc: coverSvc}
}

func (h *AppConfigHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *AppConfigHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// Get GET /api/app-config 返回当前配置（密钥脱敏，仅返回是否已设置）。
func (h *AppConfigHandler) Get(c *gin.Context) {
	v := *h.cfg // 浅拷贝；仅清空字符串密钥，不影响原配置
	secrets := gin.H{
		"server.password":     h.cfg.Server.Password != "",
		"jwt.secret":          h.cfg.JWT.Secret != "",
		"emby.api_key":        h.cfg.Emby.APIKey != "",
		"moviepilot.password": h.cfg.MoviePilot.Password != "",
	}
	v.Server.Password = ""
	v.JWT.Secret = ""
	v.Emby.APIKey = ""
	v.MoviePilot.Password = ""

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

	// 密钥脱敏：前端留空表示沿用旧值
	if strings.TrimSpace(in.Server.Password) == "" {
		in.Server.Password = h.cfg.Server.Password
	}
	if strings.TrimSpace(in.JWT.Secret) == "" {
		in.JWT.Secret = h.cfg.JWT.Secret
	}
	if strings.TrimSpace(in.Emby.APIKey) == "" {
		in.Emby.APIKey = h.cfg.Emby.APIKey
	}
	if strings.TrimSpace(in.MoviePilot.Password) == "" {
		in.MoviePilot.Password = h.cfg.MoviePilot.Password
	}
	// 表单未管理 file_watcher，保留原值，避免被清空
	in.FileWatcher = h.cfg.FileWatcher

	// 基本校验
	if strings.TrimSpace(in.Server.Port) == "" {
		h.error(c, http.StatusBadRequest, 400, "服务器端口不能为空")
		return
	}
	if strings.TrimSpace(in.JWT.Secret) == "" {
		h.error(c, http.StatusBadRequest, 400, "JWT 密钥不能为空")
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

	// 写回 config.yaml
	if err := config.Save(&in); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "写入配置文件失败: "+err.Error())
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
