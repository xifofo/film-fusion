package organize

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

func bindUpgradeJSON(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("请求必须包含单个 JSON 对象")
	}
	return nil
}

func (h *Handler) upgradeRuleGroupError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, service.ErrUpgradeRuleGroupNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, service.ErrUpgradeRuleGroupConflict) || errors.Is(err, service.ErrUpgradeRuleGroupInUse) {
		status = http.StatusConflict
	}
	h.error(c, status, status, err.Error())
}

// ListUpgradeRuleGroups 列出当前用户可复用的洗版配置。
func (h *Handler) ListUpgradeRuleGroups(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	groups, err := service.ListMediaUpgradeRuleGroups(h.store().WithContext(h.requestContext()), uid)
	if err != nil {
		h.error(c, 500, 500, err.Error())
		return
	}
	h.success(c, groups, "洗版规则组")
}

// SaveUpgradeRuleGroup 新建或按版本保存规则组，不执行任何文件操作。
func (h *Handler) SaveUpgradeRuleGroup(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var id uint64
	var err error
	if c.Param("id") != "" {
		id, err = strconv.ParseUint(c.Param("id"), 10, 32)
		if err != nil || id == 0 {
			h.error(c, 400, 400, "无效的规则组 ID")
			return
		}
	}
	var input service.MediaUpgradeRuleGroupInput
	if err := bindUpgradeJSON(c, &input); err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	group, err := service.SaveMediaUpgradeRuleGroup(h.store().WithContext(h.requestContext()), uid, uint(id), input)
	if err != nil {
		h.upgradeRuleGroupError(c, err)
		return
	}
	h.success(c, group, "洗版规则组已保存；相关目录下次预览使用新版本")
}

// DeleteUpgradeRuleGroup 仅删除无目录引用且版本未变化的规则组。
func (h *Handler) DeleteUpgradeRuleGroup(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		h.error(c, 400, 400, "无效的规则组 ID")
		return
	}
	revision, err := strconv.ParseUint(c.Query("expected_revision"), 10, 32)
	if err != nil || revision == 0 {
		h.error(c, 400, 400, "缺少规则组版本，请刷新后重试")
		return
	}
	if err := service.DeleteMediaUpgradeRuleGroup(h.store().WithContext(h.requestContext()), uid, uint(id), uint(revision)); err != nil {
		h.upgradeRuleGroupError(c, err)
		return
	}
	h.success(c, nil, "洗版规则组已删除")
}
