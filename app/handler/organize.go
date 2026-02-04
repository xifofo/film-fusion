package handler

import (
	"context"
	"encoding/json"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"net/http"
	"path/filepath"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
)

// OrganizeHandler 处理整理文件的接口
type OrganizeHandler struct {
	logger        *logger.Logger
	sdk115Open    *sdk115.Client
	moviePilotSvc *service.MoviePilotService
}

func NewOrganizeHandler(log *logger.Logger, moviePilotSvc *service.MoviePilotService) *OrganizeHandler {
	return &OrganizeHandler{
		logger:        log,
		sdk115Open:    sdk115.New(),
		moviePilotSvc: moviePilotSvc,
	}
}

func (h *OrganizeHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

func (h *OrganizeHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

type Organize115Request struct {
	CloudStorageID uint   `json:"cloud_storage_id" binding:"required"`
	FolderID       string `json:"folder_id" binding:"required"`
}

type Organize115ItemResult struct {
	FileID       string `json:"file_id"`
	FileName     string `json:"file_name"`
	PickCode     string `json:"pickcode"`
	MediaType    string `json:"media_type"`
	Category     string `json:"category"`
	Title        string `json:"title"`
	Year         string `json:"year"`
	TransferName string `json:"transfer_name"`
	TargetPath   string `json:"target_path"`
	Error        string `json:"error,omitempty"`
}

func (h *OrganizeHandler) Organize115(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req Organize115Request
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, userID).First(&storage).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		return
	}

	if storage.StorageType != model.StorageType115Open {
		h.error(c, http.StatusBadRequest, 400, "当前接口仅支持 115open 存储类型")
		return
	}
	if !storage.IsAvailable() {
		h.error(c, http.StatusBadRequest, 400, "云存储不可用或令牌已过期")
		return
	}

	categoryCfg, err := h.moviePilotSvc.GetCategoryConfig()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "获取 MoviePilot 分类配置失败")
		return
	}

	h.sdk115Open.SetAccessToken(storage.AccessToken)

	req115 := &sdk115.GetFilesReq{
		CID:     req.FolderID,
		ShowDir: true,
		Stdir:   1,
		Limit:   1150,
		Offset:  0,
	}

	results := make([]Organize115ItemResult, 0)
	totalFiles := 0

	for {
		resp, err := h.sdk115Open.GetFiles(context.Background(), req115)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "获取115文件列表失败")
			return
		}

		if debugJSON, err := json.MarshalIndent(resp.Data, "", "  "); err == nil {
			h.logger.Infof("115目录分页数据 (offset=%d): %s", req115.Offset, string(debugJSON))
		}

		for _, file := range resp.Data {
			if file.Fc != "1" {
				continue
			}

			totalFiles++
			item := Organize115ItemResult{
				FileID:   file.Fid,
				FileName: file.Fn,
				PickCode: file.Pc,
			}

			ext := strings.TrimPrefix(filepath.Ext(file.Fn), ".")

			info, _, recErr := h.moviePilotSvc.RecognizeFile(file.Fn)
			if recErr != nil {
				item.Error = recErr.Error()
			}

			transferName, _, transErr := h.moviePilotSvc.TransferName(file.Fn, ext)
			if transErr != nil {
				if item.Error == "" {
					item.Error = transErr.Error()
				} else {
					item.Error = item.Error + "; " + transErr.Error()
				}
			}

			item.MediaType = info.MediaType
			item.Title = info.Title
			item.Year = info.Year
			item.TransferName = transferName
			item.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
			item.TargetPath = service.BuildMoviePilotTargetPath(item.Category, info, transferName, file.Fn)

			results = append(results, item)
		}

		if req115.Offset+req115.Limit >= resp.Count {
			break
		}
		req115.Offset += req115.Limit
	}

	h.success(c, gin.H{
		"cloud_storage_id": req.CloudStorageID,
		"folder_id":        req.FolderID,
		"total":            totalFiles,
		"items":            results,
	}, "整理完成")
}
