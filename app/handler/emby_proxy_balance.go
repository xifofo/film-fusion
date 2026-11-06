package handler

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"film-fusion/app/service"
	"film-fusion/app/store/embyproxylog"
	"film-fusion/app/utils/embydebug"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
)

// code115FileNotFound 是 115 开放平台「文件(夹)不存在或已删除」的错误码。
const code115FileNotFound = 430004

// proxyPlay 代理播放请求，返回重定向URL、日志条目、是否跳过和跳过原因。
func (h *EmbyProxyHandler) proxyPlay(c *gin.Context) (target string, entry embyproxylog.Entry, skip bool, reason string) {
	currentURI := c.Request.RequestURI
	baseEntry := embyproxylog.Entry{
		Method:    c.Request.Method,
		URI:       currentURI,
		UserAgent: c.Request.UserAgent(),
		RemoteIP:  c.ClientIP(),
	}

	h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求路径: %s", c.Request.URL.Path)
	re := regexp.MustCompile(`/[Vv]ideos/(\S+)/(stream|original|master)`)
	matches := re.FindStringSubmatch(currentURI)

	if len(matches) < 1 {
		h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求路径不匹配: %s", c.Request.URL.Path)
		return "", baseEntry, true, "不是 Emby 视频播放请求"
	}

	// 只为播放流创建编号；内部身份查询共享取消信号，账号决策仍保留原 Background 生命周期。
	ctx := embydebug.Start(c.Request.Context(), h.logger)
	c.Request = c.Request.WithContext(ctx)
	embydebug.Logf(ctx, "request", "method=%q path=%q", c.Request.Method, c.Request.URL.Path)
	defer func() {
		embydebug.Logf(ctx, "result", "redirect_ready=%t upstream_fallback=%t item_id=%q match=%d assignment=%d actual_storage=%d account_type=%q status=%q has_fallback_reason=%t", !skip, skip, entry.ItemID, entry.Match302ID, entry.AssignmentID, entry.ActualStorageID, entry.AccountType, entry.BalanceStatus, reason != "" || entry.FallbackReason != "")
	}()

	// 开始计时
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		h.logger.Infof("[EMBY PROXY] ProxyPlay 执行时间: %v", duration)
	}()

	itemInfoUri, itemId, etag, mediaSourceId, apiKey := embyhelper.GetItemPathInfo(c, h.config)
	embydebug.Logf(ctx, "media_lookup", "item_id=%q media_source_id=%q", itemId, mediaSourceId)
	embyRes, err := embyhelper.GetEmbyItemsContext(c.Request.Context(), itemInfoUri, itemId, etag, mediaSourceId, apiKey)

	if err != nil {
		embydebug.Logf(ctx, "media_lookup", "result=failed error_type=%T", err)
		h.logger.Errorf("获取 EmbyItems 错误: %v", err)
		return "", baseEntry, true, "获取 EmbyItems 失败: " + err.Error()
	}

	h.logger.Debugf("[EMBY PROXY] Request path: %s", c.Request.URL.Path)

	// EMBY 的播放地址, 兼容 Windows 的 Emby 路径
	embyPlayPath := pathhelper.EnsureLeadingSlash(embyRes.Path)

	h.logger.Infof("[EMBY PROXY] Emby 原地址: %s", embyPlayPath)

	if strings.HasPrefix(embyPlayPath, "http") {
		embydebug.Logf(ctx, "binding", "result=bypassed reason=external_media_url item_id=%q", itemId)
		h.logger.Infof("[EMBY PROXY] Emby 播放地址是完整的 URL: %s", embyPlayPath)
		baseEntry.Source = "proxyPlay"
		baseEntry.Target = embyPlayPath
		baseEntry.ItemID = itemId
		baseEntry.MediaSourceID = mediaSourceId
		baseEntry.MediaPath = embyPlayPath
		return embyPlayPath, baseEntry, false, ""
	}

	// Emby 账号绑定：命中则强制走指定 115 存储，跳过通用就绪缓存命中(避免被路由到非绑定账号)。
	forcedStorageID := h.resolveBoundStorageID(c, itemId)
	embydebug.Logf(ctx, "binding_route", "bound_storage=%d", forcedStorageID)

	if forcedStorageID == 0 {
		if redirectURL, entry, ok := h.proxyReadyBalanceCache(c, baseEntry, embyPlayPath, itemId, mediaSourceId); ok {
			embydebug.Logf(ctx, "ready_cache", "result=hit assignment=%d actual_storage=%d", entry.AssignmentID, entry.ActualStorageID)
			return redirectURL, entry, false, ""
		}
	}

	match, matchedPath, err := h.balanceSvc.FindMatch(embyPlayPath)
	if err != nil {
		embydebug.Logf(ctx, "match302", "result=not_matched error_type=%T", err)
		h.logger.Debugf("[EMBY PROXY] 路径 %s 未匹配任何 match302 规则", embyPlayPath)
		return "", baseEntry, true, "路径未匹配任何 match302 规则: " + embyPlayPath
	}
	embydebug.Logf(ctx, "match302", "result=matched match=%d balance_enabled=%t source_storage=%d bound_storage=%d", match.ID, match.BalanceEnabled, match.CloudStorageID, forcedStorageID)

	playbackReq := service.BalancePlaybackRequest{
		Match:           match,
		SourcePath:      embyPlayPath,
		MatchedPath:     matchedPath,
		EmbyItemID:      itemId,
		MediaSourceID:   mediaSourceId,
		UserAgent:       c.Request.UserAgent(),
		RemoteIP:        c.ClientIP(),
		ForcedStorageID: forcedStorageID,
	}
	decision, err := h.balanceSvc.ResolvePlayback(embydebug.Copy(context.Background(), ctx), playbackReq)
	if err != nil {
		embydebug.Logf(ctx, "decision", "result=failed error_type=%T", err)
		h.logger.Errorf("[EMBY PROXY] Match302 负载均衡决策失败: %v", err)
		return "", baseEntry, true, "Match302 负载均衡决策失败: " + err.Error()
	}

	embydebug.Logf(ctx, "download_url", "result=requesting actual_storage=%d account_type=%q", decision.PlaybackStorage.ID, decision.AccountType)
	redirectURL, fromCache, err := h.getDownloadURLForStorage(*decision.PlaybackStorage, decision.ActualPickCode, c.Request.UserAgent())
	if err != nil && decision.UseBalance && !decision.IsSourcePlayback {
		embydebug.Logf(ctx, "download_url", "result=failed actual_storage=%d file_not_found=%t error_type=%T next=source_fallback", decision.PlaybackStorage.ID, is115FileNotFound(err), err)
		if is115FileNotFound(err) && decision.Assignment != nil {
			h.logger.Warnf("[EMBY PROXY] 子账号目标文件已删除(430004)，失效并重新秒传 assignment=%d", decision.Assignment.ID)
			h.invalidateBalanceAssignment(decision.Assignment.Match302ID, decision.Assignment.ID)
		}
		if decision.SourceStorage == nil {
			embydebug.Logf(ctx, "source_fallback", "result=blocked reason=missing_source_storage")
			h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，源账号信息缺失，无法回退: %v", err)
			return "", baseEntry, true, "获取子账号直链失败，源账号信息缺失: " + err.Error()
		}
		if limitErr := h.balanceSvc.EnsureStrictStorageAllowed(decision.Match, decision.SourceStorage.ID, playbackReq); limitErr != nil {
			embydebug.Logf(ctx, "source_fallback", "result=blocked reason=source_concurrency_limit source_storage=%d", decision.SourceStorage.ID)
			h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，但严格模式不允许回退源账号: %v", limitErr)
			return "", baseEntry, true, "获取子账号直链失败，严格模式不允许回退源账号: " + limitErr.Error()
		}
		h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，回退源账号: %v", err)
		decision.PlaybackStorage = decision.SourceStorage
		decision.ActualPickCode = decision.SourceFile.PickCode
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.Status = "失败回退"
		decision.FallbackReason = "获取子账号直链失败: " + err.Error()
		embydebug.Logf(ctx, "source_fallback", "result=requesting source_storage=%d", decision.SourceStorage.ID)
		redirectURL, fromCache, err = h.getDownloadURLForStorage(*decision.SourceStorage, decision.SourceFile.PickCode, c.Request.UserAgent())
	}
	if err != nil {
		embydebug.Logf(ctx, "download_url", "result=failed actual_storage=%d error_type=%T", decision.PlaybackStorage.ID, err)
		h.logger.Errorf("[EMBY PROXY] 获取下载URL失败: %v", err)
		return "", baseEntry, true, "获取下载URL失败: " + err.Error()
	}

	embydebug.Logf(ctx, "download_url", "result=ready actual_storage=%d from_cache=%t", decision.PlaybackStorage.ID, fromCache)
	entry = h.buildPlaybackLogEntry(baseEntry, redirectURL, fromCache, itemId, mediaSourceId, embyPlayPath, decision)
	h.logger.Infof("[EMBY PROXY] Match302 匹配成功，重定向到: %s", redirectURL)
	return redirectURL, entry, false, ""
}

func (h *EmbyProxyHandler) proxyReadyBalanceCache(c *gin.Context, baseEntry embyproxylog.Entry, embyPlayPath, itemID, mediaSourceID string) (string, embyproxylog.Entry, bool) {
	assignment, err := h.balanceSvc.FindReadyPlaybackCacheByPath(embyPlayPath)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 查询 Match302 负载均衡缓存失败 path=%s err=%v", embyPlayPath, err)
		return "", baseEntry, false
	}
	if assignment == nil {
		return "", baseEntry, false
	}
	if assignment.Match302 == nil || assignment.PlaybackStorage == nil {
		h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存关联数据不完整 assignment=%d", assignment.ID)
		return "", baseEntry, false
	}

	playbackReq := service.BalancePlaybackRequest{
		Match:         assignment.Match302,
		SourcePath:    assignment.SourceFilePath,
		MatchedPath:   assignment.TargetPath,
		EmbyItemID:    itemID,
		MediaSourceID: mediaSourceID,
		UserAgent:     c.Request.UserAgent(),
		RemoteIP:      c.ClientIP(),
	}
	if err := h.balanceSvc.EnsureStrictStorageAllowed(assignment.Match302, assignment.PlaybackStorageID, playbackReq); err != nil {
		h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存命中但账号并发限制不允许播放 assignment=%d err=%v", assignment.ID, err)
		return "", baseEntry, false
	}

	redirectURL, _, err := h.getDownloadURLForStorage(*assignment.PlaybackStorage, assignment.TargetPickcode, c.Request.UserAgent())
	if err != nil {
		if is115FileNotFound(err) {
			h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存目标文件已删除(430004)，失效并重新秒传 assignment=%d err=%v", assignment.ID, err)
			h.invalidateBalanceAssignment(assignment.Match302ID, assignment.ID)
		} else {
			h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存命中但获取直链失败 assignment=%d err=%v", assignment.ID, err)
		}
		return "", baseEntry, false
	}

	h.balanceSvc.MarkAssignmentPlayed(assignment.ID)
	decision := &service.BalancePlaybackDecision{
		UseBalance:       true,
		Status:           "子账号播放",
		Match:            assignment.Match302,
		Assignment:       assignment,
		SourceStorage:    assignment.SourceStorage,
		PlaybackStorage:  assignment.PlaybackStorage,
		ActualPickCode:   assignment.TargetPickcode,
		IsSourcePlayback: false,
		AccountType:      "member",
	}
	if decision.SourceStorage == nil {
		decision.SourceStorage = assignment.Match302.CloudStorage
	}

	entry := h.buildPlaybackLogEntry(baseEntry, redirectURL, true, itemID, mediaSourceID, embyPlayPath, decision)
	h.logger.Infof("[EMBY PROXY] Match302 负载均衡缓存命中 assignment=%d storage=%d，重定向到: %s", assignment.ID, assignment.PlaybackStorageID, redirectURL)
	return redirectURL, entry, true
}

// is115FileNotFound 判断错误是否为 115 开放平台「文件(夹)不存在或已删除」(code 430004)。
// SDK 在业务失败时返回结构化的 *sdk115.Error，可用 errors.As 可靠识别，无需脆弱的字符串匹配。
func is115FileNotFound(err error) bool {
	var sdkErr *sdk115.Error
	if errors.As(err, &sdkErr) {
		return sdkErr.Code == code115FileNotFound
	}
	return false
}

// invalidateBalanceAssignment 让失效的负载均衡 assignment 重新进入秒传流程。
// 场景：子账号上的秒传目标文件已被 115 删除(430004)，但缓存仍标记为 ready 并指向它，
// 导致每次播放都先命中失效缓存、调用 DownURL 失败再回退源账号。通过 RetryAssignment 将其
// 重置为 pending 并后台重新秒传：本次请求回退源账号播放，重传完成后自动恢复负载均衡。
func (h *EmbyProxyHandler) invalidateBalanceAssignment(matchID, assignmentID uint) {
	if matchID == 0 || assignmentID == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.logger.Errorf("[EMBY PROXY] 失效负载均衡 assignment=%d panic: %v", assignmentID, r)
			}
		}()
		if _, err := h.balanceSvc.RetryAssignment(context.Background(), matchID, assignmentID); err != nil {
			h.logger.Warnf("[EMBY PROXY] 失效负载均衡 assignment=%d 失败: %v", assignmentID, err)
			return
		}
		h.logger.Infof("[EMBY PROXY] 负载均衡目标文件已删除，已触发重新秒传 assignment=%d", assignmentID)
	}()
}
