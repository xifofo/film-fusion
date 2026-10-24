package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

type cloudBrowserOperations interface {
	Browse(context.Context, uint, uint, string, string, int, int) (*service.CloudBrowserPage, error)
	GenerateSTRM(context.Context, uint, uint, service.CloudBrowserGenerateRequest) (*service.CloudBrowserOperationResult, error)
	ActiveJob(context.Context, uint, uint) (*service.CloudBrowserOperationResult, error)
	JobStatus(context.Context, uint, uint, string) (*service.CloudBrowserOperationResult, error)
	CancelJob(context.Context, uint, uint, string) (*service.CloudBrowserOperationResult, error)
}

type CloudBrowserHandler struct{ svc cloudBrowserOperations }

func NewCloudBrowserHandler(svc cloudBrowserOperations) *CloudBrowserHandler {
	return &CloudBrowserHandler{svc: svc}
}

func cloudBrowserIdentity(c *gin.Context) (uint, uint, bool) {
	value, exists := c.Get("user_id")
	userID, valid := value.(uint)
	if !exists || !valid || userID == 0 {
		c.JSON(http.StatusUnauthorized, NewErrorResponse("用户未认证", ""))
		return 0, 0, false
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, strconv.IntSize)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("目录配置 ID 无效", ""))
		return 0, 0, false
	}
	return userID, uint(id), true
}

func (h *CloudBrowserHandler) Browse(c *gin.Context) {
	userID, directoryID, ok := cloudBrowserIdentity(c)
	if !ok {
		return
	}
	method, cid := c.Query("access_method"), c.Query("cid")
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if !cloudFolderMoveMethodValid(method) || (cid != "" && !cloudFolderMoveIDValid(cid, true)) || offsetErr != nil || offset < 0 || offset > 10000000 || limitErr != nil || limit < 1 || limit > 200 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("目录参数无效，请指定 Cookie 或 OpenAPI，分页范围为 1 至 200 项", ""))
		return
	}
	result, err := h.svc.Browse(c.Request.Context(), userID, directoryID, method, cid, offset, limit)
	if err != nil {
		writeCloudBrowserError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func (h *CloudBrowserHandler) GenerateSTRM(c *gin.Context) {
	userID, directoryID, ok := cloudBrowserIdentity(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request service.CloudBrowserGenerateRequest
	err := decoder.Decode(&request)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			err = nil
		} else if err == nil {
			err = errors.New("multiple JSON values")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, NewErrorResponse("STRM 请求内容过大", ""))
		} else {
			c.JSON(http.StatusBadRequest, NewErrorResponse("STRM 请求格式无效", ""))
		}
		return
	}
	if request.SelectionToken == "" || len(request.SelectionToken) > 32<<10 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请先手动加载目录并选择文件或文件夹", ""))
		return
	}
	result, err := h.svc.GenerateSTRM(c.Request.Context(), userID, directoryID, request)
	if err != nil {
		writeCloudBrowserError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func (h *CloudBrowserHandler) JobStatus(c *gin.Context) { h.job(c, false) }
func (h *CloudBrowserHandler) CancelJob(c *gin.Context) { h.job(c, true) }

func (h *CloudBrowserHandler) ActiveJob(c *gin.Context) {
	userID, directoryID, ok := cloudBrowserIdentity(c)
	if !ok {
		return
	}
	result, err := h.svc.ActiveJob(c.Request.Context(), userID, directoryID)
	if err != nil {
		writeCloudBrowserError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func (h *CloudBrowserHandler) job(c *gin.Context, cancel bool) {
	userID, directoryID, ok := cloudBrowserIdentity(c)
	if !ok {
		return
	}
	jobID := c.Param("job_id")
	if len(jobID) != 32 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("STRM 任务 ID 无效", ""))
		return
	}
	for _, char := range jobID {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			c.JSON(http.StatusBadRequest, NewErrorResponse("STRM 任务 ID 无效", ""))
			return
		}
	}
	var result *service.CloudBrowserOperationResult
	var err error
	if cancel {
		result, err = h.svc.CancelJob(c.Request.Context(), userID, directoryID, jobID)
	} else {
		result, err = h.svc.JobStatus(c.Request.Context(), userID, directoryID, jobID)
	}
	if err != nil {
		writeCloudBrowserError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func writeCloudBrowserError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "网盘操作失败，请稍后重试"
	var serviceError *service.CloudFolderMoveError
	if errors.As(err, &serviceError) && serviceError.Status >= 400 && serviceError.Status <= 599 {
		status, message = serviceError.Status, serviceError.Message
	}
	c.JSON(status, NewErrorResponse(message, ""))
}
