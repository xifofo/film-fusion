package handler

import (
	"film-fusion/app/model"
	"net/http"

	"github.com/gin-gonic/gin"
)

// SystemConfigHandler 系统配置处理器
type SystemConfigHandler struct{}

// NewSystemConfigHandler 创建系统配置处理器
func NewSystemConfigHandler() *SystemConfigHandler {
	return &SystemConfigHandler{}
}

// 创建成功响应
func (h *SystemConfigHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *SystemConfigHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// ConfigCategoryResponse 配置分类响应结构
type ConfigCategoryResponse struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// ConfigTypeResponse 配置类型响应结构
type ConfigTypeResponse struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// GetConfigCategories 获取配置分类常量
func (h *SystemConfigHandler) GetConfigCategories(c *gin.Context) {
	categories := []ConfigCategoryResponse{
		{
			Key:         model.CategorySystem,
			Description: "系统配置",
		},
		{
			Key:         model.CategorySecurity,
			Description: "安全配置",
		},
		{
			Key:         model.Category115Open,
			Description: "115开放平台配置",
		},
	}

	h.success(c, categories, "获取配置分类成功")
}

// GetConfigTypes 获取配置类型常量
func (h *SystemConfigHandler) GetConfigTypes(c *gin.Context) {
	types := []ConfigTypeResponse{
		{
			Key:         model.TypeString,
			Description: "字符串类型",
		},
		{
			Key:         model.TypeInt,
			Description: "整数类型",
		},
		{
			Key:         model.TypeBool,
			Description: "布尔类型",
		},
		{
			Key:         model.TypeJSON,
			Description: "JSON类型",
		},
		{
			Key:         model.TypeFloat,
			Description: "浮点数类型",
		},
	}

	h.success(c, types, "获取配置类型成功")
}
