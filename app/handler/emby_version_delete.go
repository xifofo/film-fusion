package handler

// 本地版本删除只接受当前账号最近扫描中的文件，逐项报告结果；Emby 扫描与文件删除分开反馈。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
)

type embyVersionDeleteTarget struct {
	CloudPathID uint   `json:"cloud_path_id"`
	Path        string `json:"path"`
}

type embyVersionDeleteRequest struct {
	JobID       string                    `json:"job_id"`
	Files       []embyVersionDeleteTarget `json:"files"`
	RefreshEmby bool                      `json:"refresh_emby"`
}

type embyVersionDeleteFileResult struct {
	embyVersionDeleteTarget
	Status string `json:"status"` // deleted / missing / failed
	Error  string `json:"error,omitempty"`
}

type embyVersionRefreshResult struct {
	Status  string `json:"status"` // requested / failed / skipped
	Message string `json:"message"`
}

type embyVersionDeleteResponse struct {
	Files       []embyVersionDeleteFileResult `json:"files"`
	EmbyRefresh embyVersionRefreshResult      `json:"emby_refresh"`
	Status      EmbyVersionCheckStatus        `json:"status"`
}

// DeleteFiles 删除明确勾选的本地文件。路由要求管理员或对应模块 Token；文件范围再次按账号校验。
func (h *EmbyVersionCheckHandler) DeleteFiles(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	var req embyVersionDeleteRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || req.JobID == "" || len(req.Files) == 0 || len(req.Files) > 200 {
		h.error(c, http.StatusBadRequest, 400, "请提供扫描任务及 1–200 个待删除文件")
		return
	}

	owner := userID.(uint)
	targets, err := h.beginVersionDelete(owner, req)
	if err != nil {
		h.error(c, http.StatusConflict, 409, err.Error())
		return
	}
	defer func() {
		h.mu.Lock()
		delete(h.deleting, owner)
		h.mu.Unlock()
	}()

	ids := make([]uint, 0, len(targets))
	for _, file := range targets {
		ids = append(ids, file.CloudPathID)
	}
	var mappings []model.MediaDirectory
	if err := database.GetDB().WithContext(c.Request.Context()).Where("user_id = ? AND id IN ?", owner, ids).Scopes(model.MappingDirectories).Find(&mappings).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取当前媒体目录失败，未删除文件")
		return
	}
	byID := make(map[uint]model.MediaDirectory, len(mappings))
	for _, mapping := range mappings {
		byID[mapping.ID] = mapping
	}

	response := embyVersionDeleteResponse{Files: make([]embyVersionDeleteFileResult, 0, len(targets))}
	removed := make(map[string]bool)
	for _, file := range targets {
		entry := embyVersionDeleteFileResult{embyVersionDeleteTarget: embyVersionDeleteTarget{file.CloudPathID, file.Path}, Status: "failed"}
		if err := c.Request.Context().Err(); err != nil {
			entry.Error = "请求已取消，未继续删除该文件"
		} else if mapping, exists := byID[file.CloudPathID]; !exists {
			entry.Error = "媒体目录已删除、停用或不属于当前账号，请重新检查"
		} else {
			entry.Status, err = removeScannedVersionFile(file, mapping)
			if err != nil {
				entry.Status, entry.Error = "failed", err.Error()
			} else {
				removed[file.Path] = true
			}
		}
		response.Files = append(response.Files, entry)
		if h.logger != nil {
			h.logger.Infof("本地版本删除 user=%d job=%s mapping=%d path=%q status=%s", owner, req.JobID, file.CloudPathID, file.Path, entry.Status)
		}
	}

	// 即使部分失败或客户端断开，已发生的删除也必须反映到后续状态查询中。
	h.applyVersionDeletes(owner, req.JobID, removed)
	response.EmbyRefresh = embyVersionRefreshResult{Status: "skipped", Message: "未请求 Emby 扫描"}
	if req.RefreshEmby && len(removed) > 0 {
		// 删除已完成后继续一次有界通知，不因浏览器断开而遗漏，也不自动重试未知请求。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		response.EmbyRefresh = h.refreshAfterVersionDelete(ctx)
		cancel()
	}
	response.Status = EmbyVersionCheckStatus{Job: h.jobSnapshot(owner)}
	h.success(c, response, "本地文件处理完成，请核对逐项结果")
}

// beginVersionDelete 冻结本批扫描身份，阻止同账号新扫描和重复删除同时开始。
func (h *EmbyVersionCheckHandler) beginVersionDelete(owner uint, req embyVersionDeleteRequest) ([]EmbyVersionFile, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	job := h.jobs[owner]
	if job == nil || job.ID != req.JobID || job.Result == nil || job.Running || h.deleting[owner] {
		return nil, errors.New("扫描结果已失效或已有任务正在执行，请刷新后重新选择")
	}
	available := make(map[embyVersionDeleteTarget]EmbyVersionFile)
	for _, item := range job.Result.Items {
		for _, file := range item.Files {
			available[embyVersionDeleteTarget{file.CloudPathID, file.Path}] = file
		}
	}
	targets := make([]EmbyVersionFile, 0, len(req.Files))
	seen := make(map[string]bool)
	for _, target := range req.Files {
		file, exists := available[target]
		if !exists || seen[file.Path] {
			return nil, errors.New("存在不属于当前扫描结果或重复选择的文件，请刷新后重新选择")
		}
		seen[file.Path] = true
		targets = append(targets, file)
	}
	if h.deleting == nil {
		h.deleting = make(map[uint]bool)
	}
	h.deleting[owner] = true
	return targets, nil
}

func (h *EmbyVersionCheckHandler) refreshAfterVersionDelete(ctx context.Context) embyVersionRefreshResult {
	if h.embyClient == nil {
		return embyVersionRefreshResult{Status: "failed", Message: "Emby 客户端未配置，请在 Emby 中手动扫描媒体库"}
	}
	status, _, err := h.embyClient.WithContext(ctx).RefreshLibrary()
	if err != nil {
		// 客户端错误可能包含 API Key 或上游内容，返回可操作的状态而不回显原始错误。
		return embyVersionRefreshResult{Status: "failed", Message: fmt.Sprintf("Emby 扫描请求未确认（HTTP %d），请检查连接与权限或在 Emby 中手动扫描", status)}
	}
	return embyVersionRefreshResult{Status: "requested", Message: "Emby 已受理全库扫描请求；媒体库更新需等待扫描完成"}
}
