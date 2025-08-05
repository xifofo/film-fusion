package handler

import (
	"encoding/json"
	"film-fusion/app/logger"
	"net/http"

	"github.com/gin-gonic/gin"
)

// WebhookHandler 处理 webhook 相关请求
type WebhookHandler struct {
	logger *logger.Logger
}

// NewWebhookHandler 创建新的 WebhookHandler
func NewWebhookHandler(log *logger.Logger) *WebhookHandler {
	return &WebhookHandler{
		logger: log,
	}
}

// CloudDrive2FileNotify 处理 clouddrive2 文件系统监听器的 webhook
func (h *WebhookHandler) CloudDrive2FileNotify(c *gin.Context) {
	// 获取查询参数
	deviceName := c.Query("device_name")
	userName := c.Query("user_name")

	h.logger.Debugf("收到 clouddrive2 文件通知 webhook - 设备: %s, 用户: %s", deviceName, userName)

	// 解析请求体
	var requestBody map[string]interface{}
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		h.logger.Errorf("解析 clouddrive2 文件通知请求体失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid JSON format",
			"message": err.Error(),
		})
		return
	}

	// 打印完整的请求数据用于调试
	requestBodyJSON, _ := json.MarshalIndent(requestBody, "", "  ")
	h.logger.Debugf("clouddrive2 文件通知完整数据: %s", string(requestBodyJSON))

	// 提取关键信息
	if deviceNameBody, ok := requestBody["device_name"].(string); ok {
		h.logger.Debugf("请求体中的设备名: %s", deviceNameBody)
	}

	if userNameBody, ok := requestBody["user_name"].(string); ok {
		h.logger.Debugf("请求体中的用户名: %s", userNameBody)
	}

	if version, ok := requestBody["version"].(string); ok {
		h.logger.Debugf("clouddrive2 版本: %s", version)
	}

	if eventCategory, ok := requestBody["event_category"].(string); ok {
		h.logger.Debugf("事件类别: %s", eventCategory)
	}

	if eventName, ok := requestBody["event_name"].(string); ok {
		h.logger.Debugf("事件名称: %s", eventName)
	}

	if eventTime, ok := requestBody["event_time"].(string); ok {
		h.logger.Debugf("事件时间: %s", eventTime)
	}

	if sendTime, ok := requestBody["send_time"].(string); ok {
		h.logger.Debugf("发送时间: %s", sendTime)
	}

	// 解析文件变更数据
	if data, ok := requestBody["data"].([]interface{}); ok {
		for i, item := range data {
			if fileChange, ok := item.(map[string]interface{}); ok {
				h.logger.Debugf("文件变更 %d:", i+1)
				if action, ok := fileChange["action"].(string); ok {
					h.logger.Debugf("  动作: %s", action)
				}
				if isDir, ok := fileChange["is_dir"].(string); ok {
					h.logger.Debugf("  是否为目录: %s", isDir)
				}
				if sourceFile, ok := fileChange["source_file"].(string); ok {
					h.logger.Debugf("  源文件: %s", sourceFile)
				}
				if destinationFile, ok := fileChange["destination_file"].(string); ok {
					h.logger.Debugf("  目标文件: %s", destinationFile)
				}
			}
		}
	}

	// 返回成功响应
	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"message":     "File notification received",
		"device_name": deviceName,
		"user_name":   userName,
		"received_at": requestBody,
	})
}

// CloudDrive2MountNotify 处理 clouddrive2 挂载点监听器的 webhook
func (h *WebhookHandler) CloudDrive2MountNotify(c *gin.Context) {
	// 获取查询参数
	deviceName := c.Query("device_name")
	userName := c.Query("user_name")
	eventType := c.Query("type")

	h.logger.Debugf("收到 clouddrive2 挂载通知 webhook - 设备: %s, 用户: %s, 类型: %s", deviceName, userName, eventType)

	// 解析请求体
	var requestBody map[string]interface{}
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		h.logger.Errorf("解析 clouddrive2 挂载通知请求体失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid JSON format",
			"message": err.Error(),
		})
		return
	}

	// 打印完整的请求数据用于调试
	requestBodyJSON, _ := json.MarshalIndent(requestBody, "", "  ")
	h.logger.Debugf("clouddrive2 挂载通知完整数据: %s", string(requestBodyJSON))

	// 提取关键信息
	if deviceNameBody, ok := requestBody["device_name"].(string); ok {
		h.logger.Debugf("请求体中的设备名: %s", deviceNameBody)
	}

	if userNameBody, ok := requestBody["user_name"].(string); ok {
		h.logger.Debugf("请求体中的用户名: %s", userNameBody)
	}

	if version, ok := requestBody["version"].(string); ok {
		h.logger.Debugf("clouddrive2 版本: %s", version)
	}

	if eventCategory, ok := requestBody["event_category"].(string); ok {
		h.logger.Debugf("事件类别: %s", eventCategory)
	}

	if eventName, ok := requestBody["event_name"].(string); ok {
		h.logger.Debugf("事件名称: %s", eventName)
	}

	if eventTime, ok := requestBody["event_time"].(string); ok {
		h.logger.Debugf("事件时间: %s", eventTime)
	}

	if sendTime, ok := requestBody["send_time"].(string); ok {
		h.logger.Debugf("发送时间: %s", sendTime)
	}

	// 解析挂载点变更数据
	if data, ok := requestBody["data"].([]interface{}); ok {
		for i, item := range data {
			if mountChange, ok := item.(map[string]interface{}); ok {
				h.logger.Debugf("挂载点变更 %d:", i+1)
				if action, ok := mountChange["action"].(string); ok {
					h.logger.Debugf("  动作: %s", action)
				}
				if mountPoint, ok := mountChange["mount_point"].(string); ok {
					h.logger.Debugf("  挂载点: %s", mountPoint)
				}
				if status, ok := mountChange["status"].(string); ok {
					h.logger.Debugf("  状态: %s", status)
				}
				if reason, ok := mountChange["reason"].(string); ok {
					h.logger.Debugf("  原因: %s", reason)
				}
			}
		}
	}

	// 返回成功响应
	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"message":     "Mount notification received",
		"device_name": deviceName,
		"user_name":   userName,
		"type":        eventType,
		"received_at": requestBody,
	})
}
