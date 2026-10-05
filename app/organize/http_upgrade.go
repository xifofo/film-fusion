package organize

import (
	"errors"
	"net/http"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) UpgradePresets(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	h.success(c, service.MediaUpgradePresets(), "洗版预设")
}

func (h *Handler) TestUpgradePolicy(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	var req struct {
		Policy         service.MediaUpgradePolicy `json:"policy"`
		Incoming       string                     `json:"incoming"`
		Existing       string                     `json:"existing"`
		IncomingSizeMB int64                      `json:"incoming_size_mb"`
		ExistingSizeMB int64                      `json:"existing_size_mb"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	if len(req.Incoming) > 4096 || len(req.Existing) > 4096 || req.IncomingSizeMB < 0 || req.ExistingSizeMB < 0 || req.IncomingSizeMB > 100*1024*1024 || req.ExistingSizeMB > 100*1024*1024 {
		h.error(c, 400, 400, "测试输入超出范围")
		return
	}
	if err := service.ValidateMediaUpgradePolicy(req.Policy); err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	newFacts := service.ParseMediaUpgradeFacts(req.Incoming, req.IncomingSizeMB*1024*1024)
	oldFacts := service.ParseMediaUpgradeFacts(req.Existing, req.ExistingSizeMB*1024*1024)
	action, reason := service.CompareMediaUpgrade(req.Policy, newFacts, oldFacts)
	h.success(c, gin.H{"action": action, "reason": reason, "incoming": service.RankMediaUpgrade(req.Policy, newFacts), "existing": service.RankMediaUpgrade(req.Policy, oldFacts), "incoming_facts": newFacts, "existing_facts": oldFacts}, "质量规则测试；未核对媒体身份")
}

func (h *Handler) ListUpgradeJobs(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var jobs []model.OrganizeUpgradeJob
	query := h.store().Where("user_id = ?", uid)
	if dirID := c.Query("cloud_directory_id"); dirID != "" {
		query = query.Where("cloud_directory_id = ?", dirID)
	}
	if err := query.Order("created_at DESC").Limit(50).Find(&jobs).Error; err != nil {
		h.error(c, 500, 500, err.Error())
		return
	}
	h.success(c, jobs, "洗版记录")
}

func (h *Handler) RefreshUpgradeLibrary(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var job model.OrganizeUpgradeJob
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", c.Param("id"), uid).First(&job).Error; err != nil {
		h.error(c, 404, 404, "洗版任务不存在或无权限")
		return
	}
	if job.Status != "completed" {
		h.error(c, 409, 409, "请先完成洗版任务")
		return
	}
	if h.embyClient == nil {
		h.error(c, 400, 400, "Emby 尚未配置")
		return
	}
	job.RefreshError = ""
	status, _, err := h.embyClient.RefreshLibrary()
	if err != nil {
		job.RefreshError = err.Error()
	} else if status < 200 || status >= 300 {
		job.RefreshError = "Emby 刷新请求失败"
	}
	if err := h.store().Model(&job).Update("refresh_error", job.RefreshError).Error; err != nil {
		h.error(c, 500, 500, err.Error())
		return
	}
	h.success(c, job, "已尝试刷新媒体库")
}

func (h *Handler) RetryUpgradeJob(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var job model.OrganizeUpgradeJob
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", c.Param("id"), uid).First(&job).Error; err != nil {
		h.error(c, 404, 404, "洗版任务不存在或无权限")
		return
	}
	var req struct {
		Restore bool `json:"restore"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	if job.Status == "completed" || job.Status == "rolled_back" {
		h.error(c, 409, 409, "此任务已经结束")
		return
	}
	if job.LeaseUntil != nil && job.LeaseUntil.After(time.Now()) {
		h.error(c, 409, 409, "任务仍在执行，或中断任务的租约尚未到期")
		return
	}
	payload, err := decodeUpgradePayload(job)
	if err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", job.CloudStorageID, uid).First(&storage).Error; err != nil {
		h.error(c, 400, 400, "云存储不可用")
		return
	}
	var lock model.OrganizeStorageLock
	if err := h.store().WithContext(h.requestContext()).Where("cloud_storage_id = ? AND plan_id = ?", storage.ID, job.ID).First(&lock).Error; err != nil {
		h.error(c, 409, 409, "任务的恢复锁缺失，已停止执行")
		return
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	if req.Restore {
		payload.Rollback = true
	}
	// Save the direction with the lease claim inside runUpgradeJob. A failed
	// compensation stays a compensation on every subsequent retry.
	result, err := h.runUpgradeJob(&job, &payload, &storage, client)
	if err != nil {
		c.JSON(http.StatusOK, apiResponse{Code: 0, Message: "洗版任务仍需处理", Data: gin.H{"result": result, "error": err.Error()}})
		return
	}
	if result.UpgradeJob == nil {
		h.error(c, 500, 500, errors.New("洗版任务结果缺失").Error())
		return
	}
	h.success(c, gin.H{"result": result}, "洗版任务已处理")
}
