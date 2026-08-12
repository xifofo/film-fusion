package handler

import "film-fusion/app/database"

func (h *AppConfigHandler) currentRSSAutomationUserAgent() string {
	if h.db == nil {
		return database.DefaultRSSAutomationUserAgent
	}

	settings, err := database.LoadRSSAutomationSettings(h.db)
	if err != nil {
		if h.logger != nil {
			h.logger.Warnf("[rss-automation-settings] 读取数据库配置失败，使用默认值: %v", err)
		}
		return database.DefaultRSSAutomationUserAgent
	}
	return settings.UserAgent
}
