// 通用字典管理接口沿用 FilmFusion 管理员 JWT 与响应信封，输入拒绝未知字段和 null。
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// DictionaryHandler 只负责协议转换，字段规则与版本校验由 Service 执行。
type DictionaryHandler struct{ service *service.DictionaryService }

// NewDictionaryHandler 复用应用装配的数据库，不在请求中创建独立连接。
func NewDictionaryHandler(s *service.DictionaryService) *DictionaryHandler {
	return &DictionaryHandler{service: s}
}

func dictionaryResponse(c *gin.Context, data any, err error) {
	if err == nil {
		c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "操作成功", Data: data})
		return
	}
	var failure *service.DictionaryError
	if errors.As(err, &failure) {
		c.JSON(failure.Status, ApiResponse{Code: failure.Status, Message: failure.Message, Data: gin.H{"reason": failure.Reason}})
		return
	}
	c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "数据字典操作失败"})
}

func dictionaryInput(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return service.ErrDictionaryInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return service.ErrDictionaryInvalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return service.ErrDictionaryInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return service.ErrDictionaryInvalid
	}
	return nil
}

func dictionaryQuery(c *gin.Context) (service.DictionaryQuery, error) {
	page, e1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e2 := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	query := service.DictionaryQuery{Page: page, PageSize: size, Search: strings.TrimSpace(c.Query("search")), Sort: c.Query("sort"), Order: c.Query("order"), IDs: c.QueryArray("ids")}
	if e1 != nil || e2 != nil {
		return query, service.ErrDictionaryInvalid
	}
	if value, exists := c.GetQuery("enabled"); exists {
		if value != "true" && value != "false" {
			return query, service.ErrDictionaryInvalid
		}
		enabled := value == "true"
		query.Enabled = &enabled
	}
	return query, nil
}

// List 查询管理员可维护的全部类型，包括停用类型。
func (h *DictionaryHandler) List(c *gin.Context) {
	q, err := dictionaryQuery(c)
	if err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.List(c.Request.Context(), q)
	dictionaryResponse(c, data, err)
}

// Get 读取当前版本的类型详情。
func (h *DictionaryHandler) Get(c *gin.Context) {
	data, err := h.service.Get(c.Request.Context(), c.Param("code"))
	dictionaryResponse(c, data, err)
}

// Create 创建空类型，业务选项由项接口独立维护。
func (h *DictionaryHandler) Create(c *gin.Context) {
	var input service.DictionaryInput
	if err := dictionaryInput(c, &input); err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.Create(c.Request.Context(), input)
	dictionaryResponse(c, data, err)
}

// Update 更新类型的可变元数据，客户端须携带 expectedVersion。
func (h *DictionaryHandler) Update(c *gin.Context) {
	var input service.DictionaryPatch
	if err := dictionaryInput(c, &input); err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.Update(c.Request.Context(), c.Param("code"), input)
	dictionaryResponse(c, data, err)
}

// Items 返回当前类型的分页管理项，包含停用项。
func (h *DictionaryHandler) Items(c *gin.Context) {
	q, err := dictionaryQuery(c)
	if err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.Items(c.Request.Context(), c.Param("code"), q)
	dictionaryResponse(c, data, err)
}

// GetItem 精确读取所属类型中的项及其当前版本。
func (h *DictionaryHandler) GetItem(c *gin.Context) {
	data, err := h.service.GetItem(c.Request.Context(), c.Param("code"), c.Param("id"))
	dictionaryResponse(c, data, err)
}

// CreateItem 创建稳定字符串值；115 应用目录的 value 同时执行 Client ID 校验。
func (h *DictionaryHandler) CreateItem(c *gin.Context) {
	var input service.DictionaryItemInput
	if err := dictionaryInput(c, &input); err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.CreateItem(c.Request.Context(), c.Param("code"), input)
	dictionaryResponse(c, data, err)
}

// UpdateItem 只更新标签、说明、排序及启停，不接收 value 或 dictionaryId。
func (h *DictionaryHandler) UpdateItem(c *gin.Context) {
	var input service.DictionaryItemPatch
	if err := dictionaryInput(c, &input); err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.UpdateItem(c.Request.Context(), c.Param("code"), c.Param("id"), input)
	dictionaryResponse(c, data, err)
}

// DeleteItem 按已查看的版本删除候选，失败不会将旧版本自动提升后重试。
func (h *DictionaryHandler) DeleteItem(c *gin.Context) {
	version, err := strconv.ParseInt(c.Query("expectedVersion"), 10, 64)
	if err != nil {
		dictionaryResponse(c, nil, service.ErrDictionaryInvalid)
		return
	}
	err = h.service.DeleteItem(c.Request.Context(), c.Param("code"), c.Param("id"), version)
	dictionaryResponse(c, nil, err)
}

// Options 是通用业务候选入口，只返回启用类型中的启用项。
func (h *DictionaryHandler) Options(c *gin.Context) {
	q, err := dictionaryQuery(c)
	if err != nil {
		dictionaryResponse(c, nil, err)
		return
	}
	data, err := h.service.Options(c.Request.Context(), c.Param("code"), q)
	dictionaryResponse(c, data, err)
}
