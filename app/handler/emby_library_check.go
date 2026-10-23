package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

const embyLibraryCheckMaxBodyBytes = 256 << 10

type EmbyLibraryCheckHandler struct {
	svc *service.EmbyLibraryCheckService
}

func NewEmbyLibraryCheckHandler(svc *service.EmbyLibraryCheckService) *EmbyLibraryCheckHandler {
	return &EmbyLibraryCheckHandler{svc: svc}
}

func (h *EmbyLibraryCheckHandler) Check(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, embyLibraryCheckMaxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	var request struct {
		Names []string `json:"names"`
	}
	if err := decoder.Decode(&request); err != nil {
		writeEmbyLibraryCheckDecodeError(c, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeEmbyLibraryCheckDecodeError(c, err)
		return
	}
	result, err := h.svc.Check(c.Request.Context(), request.Names)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse(err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", result))
}

func writeEmbyLibraryCheckDecodeError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, NewErrorResponse("输入内容过大，请减少名称数量", ""))
		return
	}
	c.JSON(http.StatusBadRequest, NewErrorResponse("请求格式无效，请提供 names 字符串数组", ""))
}
