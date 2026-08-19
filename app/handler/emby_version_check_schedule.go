package handler

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const defaultEmbyVersionCheckCron = "0 4 * * *"

var embyVersionCheckCronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

type embyVersionCheckScheduler struct {
	db     *gorm.DB
	cronMu sync.Mutex
	cron   *cron.Cron
}

func newEmbyVersionCheckScheduler(db *gorm.DB) *embyVersionCheckScheduler {
	return &embyVersionCheckScheduler{db: db}
}

type embyVersionCheckSettingPayload struct {
	ScheduleEnabled *bool   `json:"schedule_enabled"`
	Cron            *string `json:"cron"`
	CloudPathIDs    *[]uint `json:"cloud_path_ids"`
	MediaType       *string `json:"media_type"`
}

// GetSetting GET /api/emby-version-check/setting
func (h *EmbyVersionCheckHandler) GetSetting(c *gin.Context) {
	userID, ok := h.embyVersionCheckUserID(c)
	if !ok {
		return
	}
	setting, err := h.getOrCreateEmbyVersionCheckSetting(userID)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取定时设置失败: "+err.Error())
		return
	}
	h.success(c, setting, "获取定时设置成功")
}

// UpdateSetting PUT /api/emby-version-check/setting
func (h *EmbyVersionCheckHandler) UpdateSetting(c *gin.Context) {
	userID, ok := h.embyVersionCheckUserID(c)
	if !ok {
		return
	}

	var payload embyVersionCheckSettingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	current, err := h.getOrCreateEmbyVersionCheckSetting(userID)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取定时设置失败: "+err.Error())
		return
	}
	if payload.ScheduleEnabled != nil {
		current.ScheduleEnabled = *payload.ScheduleEnabled
	}
	if payload.Cron != nil {
		current.Cron = *payload.Cron
	}
	if payload.CloudPathIDs != nil {
		current.CloudPathIDs = *payload.CloudPathIDs
	}
	if payload.MediaType != nil {
		current.MediaType = *payload.MediaType
	}

	updated, err := h.updateEmbyVersionCheckSetting(userID, *current)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "更新定时设置失败: "+err.Error())
		return
	}
	h.success(c, updated, "更新定时设置成功")
}

func (h *EmbyVersionCheckHandler) embyVersionCheckUserID(c *gin.Context) (uint, bool) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return 0, false
	}
	userID, ok := userIDVal.(uint)
	if !ok || userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return 0, false
	}
	return userID, true
}

func (h *EmbyVersionCheckHandler) getOrCreateEmbyVersionCheckSetting(userID uint) (*model.EmbyVersionCheckSetting, error) {
	if h.scheduler == nil || h.scheduler.db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var setting model.EmbyVersionCheckSetting
	err := h.scheduler.db.Where("user_id = ?", userID).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		setting = model.EmbyVersionCheckSetting{
			UserID:       userID,
			Cron:         defaultEmbyVersionCheckCron,
			CloudPathIDs: []uint{},
			MediaType:    "all",
		}
		if createErr := h.scheduler.db.Create(&setting).Error; createErr != nil {
			// A first page load and a scheduled callback can race to create the
			// per-user row. Re-read the winner instead of surfacing a unique-key error.
			if err := h.scheduler.db.Where("user_id = ?", userID).First(&setting).Error; err != nil {
				return nil, createErr
			}
		}
		return &setting, nil
	}
	if err != nil {
		return nil, err
	}
	if setting.CloudPathIDs == nil {
		setting.CloudPathIDs = []uint{}
	}
	if normalizeEmbyVersionMediaType(setting.MediaType) == "" {
		setting.MediaType = "all"
	}
	return &setting, nil
}

func (h *EmbyVersionCheckHandler) updateEmbyVersionCheckSetting(userID uint, setting model.EmbyVersionCheckSetting) (*model.EmbyVersionCheckSetting, error) {
	cronExpr := strings.TrimSpace(setting.Cron)
	mediaType := normalizeEmbyVersionMediaType(setting.MediaType)
	if mediaType == "" {
		return nil, fmt.Errorf("媒体类型必须是 all / movie / tv")
	}
	if setting.ScheduleEnabled {
		if cronExpr == "" {
			return nil, fmt.Errorf("开启定时检查时 cron 表达式不能为空")
		}
		if _, err := embyVersionCheckCronParser.Parse(cronExpr); err != nil {
			return nil, fmt.Errorf("无效的 cron 表达式: %s", cronExpr)
		}
	}

	pathIDs := normalizeEmbyVersionCheckPathIDs(setting.CloudPathIDs)
	if setting.ScheduleEnabled {
		if _, err := h.configuredEmbyVersionCheckPaths(userID, pathIDs); err != nil {
			return nil, err
		}
	}

	current, err := h.getOrCreateEmbyVersionCheckSetting(userID)
	if err != nil {
		return nil, err
	}
	current.ScheduleEnabled = setting.ScheduleEnabled
	current.Cron = cronExpr
	current.CloudPathIDs = pathIDs
	current.MediaType = mediaType
	if err := h.scheduler.db.Model(current).
		Select("ScheduleEnabled", "Cron", "CloudPathIDs", "MediaType").
		Updates(current).Error; err != nil {
		return nil, err
	}

	h.Reschedule()
	return h.getOrCreateEmbyVersionCheckSetting(userID)
}

func normalizeEmbyVersionCheckPathIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (h *EmbyVersionCheckHandler) configuredEmbyVersionCheckPaths(userID uint, ids []uint) ([]model.CloudPath, error) {
	if h.scheduler == nil || h.scheduler.db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	query := h.scheduler.db.Preload("CloudStorage").
		Where("user_id = ? AND local_path <> ''", userID)
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	}
	var paths []model.CloudPath
	if err := query.Order("id asc").Find(&paths).Error; err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("没有可扫描的云路径映射，请先配置本地路径")
	}
	if len(ids) > 0 && len(paths) != len(ids) {
		return nil, fmt.Errorf("部分定时检查云路径映射已失效，请重新保存设置")
	}
	return paths, nil
}

// Start restores every enabled per-user schedule after the server starts.
func (h *EmbyVersionCheckHandler) Start() {
	h.Reschedule()
}

// Stop stops the version-check scheduler without interrupting an already running scan.
func (h *EmbyVersionCheckHandler) Stop() {
	if h.scheduler == nil {
		return
	}
	h.scheduler.cronMu.Lock()
	defer h.scheduler.cronMu.Unlock()
	h.stopEmbyVersionCheckCronLocked()
}

// Reschedule rebuilds the shared cron runner from all enabled user settings.
func (h *EmbyVersionCheckHandler) Reschedule() {
	if h.scheduler == nil || h.scheduler.db == nil {
		return
	}
	h.scheduler.cronMu.Lock()
	defer h.scheduler.cronMu.Unlock()
	h.stopEmbyVersionCheckCronLocked()

	var settings []model.EmbyVersionCheckSetting
	if err := h.scheduler.db.Where("schedule_enabled = ?", true).Order("user_id asc").Find(&settings).Error; err != nil {
		h.logEmbyVersionCheckWarn("读取定时设置失败，跳过调度: %v", err)
		return
	}
	if len(settings) == 0 {
		h.logEmbyVersionCheckInfo("定时检查未启用")
		return
	}

	runner := cron.New(cron.WithParser(embyVersionCheckCronParser))
	entryCount := 0
	for _, setting := range settings {
		userID := setting.UserID
		expr := strings.TrimSpace(setting.Cron)
		if _, err := runner.AddFunc(expr, func() { h.runScheduledEmbyVersionCheck(userID) }); err != nil {
			h.logEmbyVersionCheckWarn("用户 %d 的 cron 表达式无效 %q: %v", userID, expr, err)
			continue
		}
		entryCount++
	}
	if entryCount == 0 {
		return
	}
	runner.Start()
	h.scheduler.cron = runner
	h.logEmbyVersionCheckInfo("定时检查已启动: %d 个任务", entryCount)
}

func (h *EmbyVersionCheckHandler) stopEmbyVersionCheckCronLocked() {
	if h.scheduler.cron == nil {
		return
	}
	ctx := h.scheduler.cron.Stop()
	<-ctx.Done()
	h.scheduler.cron = nil
	h.logEmbyVersionCheckInfo("定时调度已停止")
}

func (h *EmbyVersionCheckHandler) runScheduledEmbyVersionCheck(userID uint) {
	setting, err := h.getOrCreateEmbyVersionCheckSetting(userID)
	if err != nil {
		h.logEmbyVersionCheckWarn("用户 %d 定时任务读取设置失败: %v", userID, err)
		return
	}
	if !setting.ScheduleEnabled {
		return
	}
	mediaType := normalizeEmbyVersionMediaType(setting.MediaType)
	paths, err := h.configuredEmbyVersionCheckPaths(userID, setting.CloudPathIDs)
	if err != nil {
		h.recordScheduledEmbyVersionCheckFailure(userID, err)
		h.logEmbyVersionCheckWarn("用户 %d 定时任务触发失败: %v", userID, err)
		return
	}
	selectedIDs := append([]uint(nil), setting.CloudPathIDs...)
	if len(selectedIDs) == 0 {
		for _, path := range paths {
			selectedIDs = append(selectedIDs, path.ID)
		}
	}
	request := EmbyVersionCheckRequest{CloudPathIDs: selectedIDs, MediaType: mediaType}
	if _, started := h.startJob(userID, request, mediaType, paths); !started {
		h.logEmbyVersionCheckWarn("用户 %d 定时任务未启动: 已有检查正在运行", userID)
	}
}

func (h *EmbyVersionCheckHandler) persistEmbyVersionCheckResult(userID uint, finishedAt time.Time, result *EmbyVersionCheckResult, errorMessage string) {
	if h.scheduler == nil || h.scheduler.db == nil {
		return
	}
	if _, err := h.getOrCreateEmbyVersionCheckSetting(userID); err != nil {
		h.logEmbyVersionCheckWarn("用户 %d 保存检查状态失败: %v", userID, err)
		return
	}
	updates := map[string]any{
		"last_scan_at": &finishedAt,
		"last_status":  "success",
		"last_error":   "",
	}
	if errorMessage != "" {
		updates["last_status"] = "failed"
		updates["last_error"] = errorMessage
	} else if result != nil {
		updates["last_total_files"] = result.TotalFiles
		updates["last_duplicate_movies"] = result.DuplicateMovieCount
		updates["last_duplicate_episodes"] = result.DuplicateEpisodeCount
	}
	if err := h.scheduler.db.Model(&model.EmbyVersionCheckSetting{}).
		Where("user_id = ?", userID).
		Updates(updates).Error; err != nil {
		h.logEmbyVersionCheckWarn("用户 %d 保存检查状态失败: %v", userID, err)
	}
}

func (h *EmbyVersionCheckHandler) recordScheduledEmbyVersionCheckFailure(userID uint, checkErr error) {
	h.persistEmbyVersionCheckResult(userID, time.Now(), nil, checkErr.Error())
}

func (h *EmbyVersionCheckHandler) logEmbyVersionCheckInfo(format string, args ...any) {
	if h.logger != nil {
		h.logger.Infof("[emby-version-check] "+format, args...)
	}
}

func (h *EmbyVersionCheckHandler) logEmbyVersionCheckWarn(format string, args ...any) {
	if h.logger != nil {
		h.logger.Warnf("[emby-version-check] "+format, args...)
	}
}
