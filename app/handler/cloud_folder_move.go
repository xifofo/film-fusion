package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

const cloudFolderMoveMaxBodyBytes = 16 << 10

type cloudFolderMoveOperations interface {
	Browse(context.Context, uint, uint, string, string, int, int) (*service.CloudBrowseResult, error)
	Move(context.Context, uint, uint, service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error)
}

type CloudFolderMoveHandler struct {
	svc cloudFolderMoveOperations
}

func NewCloudFolderMoveHandler(svc cloudFolderMoveOperations) *CloudFolderMoveHandler {
	return &CloudFolderMoveHandler{svc: svc}
}

// Browse lists exactly the selected account and access method. The service
// checks same-user ownership before constructing a provider client.
func (h *CloudFolderMoveHandler) Browse(c *gin.Context) {
	userID, storageID, ok := cloudFolderMoveIdentity(c)
	if !ok {
		return
	}
	method := c.Query("access_method")
	cid := c.DefaultQuery("cid", "0")
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if !cloudFolderMoveMethodValid(method) || !cloudFolderMoveIDValid(cid, true) || offsetErr != nil || offset < 0 || limitErr != nil || limit < 1 || limit > 200 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("目录参数无效，请指定访问方式、目录 ID 和有效分页范围", ""))
		return
	}
	result, err := h.svc.Browse(c.Request.Context(), userID, storageID, method, cid, offset, limit)
	if err != nil {
		writeCloudFolderMoveError(c, err, nil)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func (h *CloudFolderMoveHandler) Move(c *gin.Context) {
	userID, storageID, ok := cloudFolderMoveIdentity(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cloudFolderMoveMaxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request service.CloudFolderMoveRequest
	if err := decoder.Decode(&request); err != nil {
		writeCloudFolderMoveDecodeError(c, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeCloudFolderMoveDecodeError(c, err)
		return
	}
	if !cloudFolderMoveMethodValid(request.AccessMethod) || !cloudFolderMoveIDValid(request.SourceParentID, true) || !cloudFolderMoveIDValid(request.FileID, false) || !cloudFolderMoveIDValid(request.TargetParentID, true) || (request.FolderID != "" && !cloudFolderMoveIDValid(request.FolderID, false)) || strings.TrimSpace(request.FileName) == "" || strings.TrimSpace(request.FolderName) == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse("移动参数无效，请提供源文件、目标目录及文件夹名称", ""))
		return
	}
	result, err := h.svc.Move(c.Request.Context(), userID, storageID, request)
	if err != nil {
		writeCloudFolderMoveError(c, err, result)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func cloudFolderMoveIdentity(c *gin.Context) (uint, uint, bool) {
	value, exists := c.Get("user_id")
	userID, valid := value.(uint)
	if !exists || !valid || userID == 0 {
		c.JSON(http.StatusUnauthorized, NewErrorResponse("用户未认证", ""))
		return 0, 0, false
	}
	storageID, err := strconv.ParseUint(c.Param("id"), 10, strconv.IntSize)
	if err != nil || storageID == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("存储账号 ID 无效", ""))
		return 0, 0, false
	}
	return userID, uint(storageID), true
}

func cloudFolderMoveMethodValid(method string) bool {
	return method == "cookie" || method == "openapi"
}

func cloudFolderMoveIDValid(value string, allowRoot bool) bool {
	if value == "" || len(value) > 20 || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && (allowRoot || id != 0)
}

func writeCloudFolderMoveDecodeError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, NewErrorResponse("移动请求内容过大", ""))
		return
	}
	c.JSON(http.StatusBadRequest, NewErrorResponse("移动请求格式无效", ""))
}

func writeCloudFolderMoveError(c *gin.Context, err error, partial *service.CloudFolderMoveResult) {
	status, message := http.StatusInternalServerError, "云盘操作失败，请稍后重试"
	var serviceError *service.CloudFolderMoveError
	if errors.As(err, &serviceError) && serviceError.Status >= 400 && serviceError.Status <= 599 {
		status, message = serviceError.Status, serviceError.Message
	}
	c.JSON(status, ApiResponse{Code: -1, Message: message, Data: partial})
}
