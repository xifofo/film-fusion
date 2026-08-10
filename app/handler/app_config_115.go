package handler

import "film-fusion/app/database"

func (h *AppConfigHandler) current115Settings() (string, string) {
	defaultApp := h.cfg.Server.Cookie115DefaultApp
	userAgent := h.cfg.Server.Web115UserAgent
	if h.db == nil {
		return defaultApp, userAgent
	}

	settings, err := database.Load115Settings(h.db)
	if err != nil {
		if h.logger != nil {
			h.logger.Warnf("[115-settings] 读取数据库配置失败，使用启动时内存值: %v", err)
		}
		return defaultApp, userAgent
	}
	return settings.CookieDefaultApp, settings.WebUserAgent
}
