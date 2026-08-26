package handler

import (
	"film-fusion/app/config"
	"film-fusion/app/database"
)

func (h *AppConfigHandler) currentMediaRecognitionSettings() config.MediaRecognitionConfig {
	settings := h.cfg.MediaRecognition
	if normalized, err := config.NormalizeMediaRecognitionSource(settings.Source); err == nil {
		settings.Source = normalized
	} else {
		settings.Source = config.DefaultMediaRecognitionSource
	}
	if h.db == nil {
		return settings
	}

	loaded, err := database.LoadMediaRecognitionSettings(h.db)
	if err != nil {
		if h.logger != nil {
			h.logger.Warnf("[media-recognition-settings] 读取数据库配置失败，使用内存值: %v", err)
		}
		return settings
	}
	return loaded
}
