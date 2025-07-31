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

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"data":    categories,
	})
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

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"data":    types,
	})
}
