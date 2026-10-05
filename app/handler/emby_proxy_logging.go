package handler

import (
	"net/url"
	"time"

	"film-fusion/app/service"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"

	"github.com/gin-gonic/gin"
)

// removeQueryParams 移除URL中的查询参数
func (h *EmbyProxyHandler) removeQueryParams(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.RawQuery = ""
	return u.String()
}

// log302 统一记录 302 重定向日志（同时写入内存环形缓冲，供前端查询）。
func (h *EmbyProxyHandler) log302(c *gin.Context, source, target string) {
	method := c.Request.Method
	uri := c.Request.RequestURI
	ua := c.Request.UserAgent()
	remote := c.ClientIP()

	h.logger.Infof("[EMBY PROXY] 302 重定向 source=%s method=%s uri=%s ua=%q remote=%s -> %s",
		source, method, uri, ua, remote, target,
	)

	h.log302Entry(embyproxylog.Entry{
		Source:    source,
		Method:    method,
		URI:       uri,
		UserAgent: ua,
		RemoteIP:  remote,
		Target:    target,
	})
}

func (h *EmbyProxyHandler) log302Entry(entry embyproxylog.Entry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	h.logger.Infof("[EMBY PROXY] 302 source=%s method=%s uri=%s status=%s assignment=%d actual_storage=%d fallback=%s -> %s",
		entry.Source,
		entry.Method,
		entry.URI,
		entry.BalanceStatus,
		entry.AssignmentID,
		entry.ActualStorageID,
		entry.FallbackReason,
		entry.Target,
	)
	embyproxylog.Default().Append(entry)
	embyplayback.Default().AttachRedirect(entry)
}

// logFallback 记录"播放请求未走 302、走了默认反代"的事件。
// 复用同一份内存环形缓冲，但 source=fallback、target 为空、reason 描述原因。
func (h *EmbyProxyHandler) logFallback(c *gin.Context, reason string) {
	method := c.Request.Method
	uri := c.Request.RequestURI
	ua := c.Request.UserAgent()
	remote := c.ClientIP()

	h.logger.Infof("[EMBY PROXY] 未走302 method=%s uri=%s ua=%q remote=%s reason=%s",
		method, uri, ua, remote, reason,
	)

	h.log302Entry(embyproxylog.Entry{
		Source:         "fallback",
		Method:         method,
		URI:            uri,
		UserAgent:      ua,
		RemoteIP:       remote,
		Target:         reason,
		FallbackReason: reason,
	})
}

func (h *EmbyProxyHandler) buildPlaybackLogEntry(base embyproxylog.Entry, target string, fromCache bool, itemID, mediaSourceID, mediaPath string, decision *service.BalancePlaybackDecision) embyproxylog.Entry {
	if fromCache {
		base.Source = "cache"
	} else {
		base.Source = "proxyPlay"
	}
	base.Target = target
	base.ItemID = itemID
	base.MediaSourceID = mediaSourceID
	base.MediaPath = mediaPath
	if decision == nil {
		return base
	}
	if decision.Match != nil {
		base.Match302ID = decision.Match.ID
	}
	if decision.Assignment != nil {
		base.AssignmentID = decision.Assignment.ID
		base.AssignedStorageID = decision.Assignment.PlaybackStorageID
		if decision.Assignment.PlaybackStorage != nil {
			base.AssignedStorageName = decision.Assignment.PlaybackStorage.StorageName
		}
	}
	if decision.PlaybackStorage != nil {
		if base.AssignedStorageID == 0 {
			base.AssignedStorageID = decision.PlaybackStorage.ID
		}
		if base.AssignedStorageName == "" {
			base.AssignedStorageName = decision.PlaybackStorage.StorageName
		}
	}
	if decision.PlaybackStorage != nil {
		base.ActualStorageID = decision.PlaybackStorage.ID
		base.ActualStorageName = decision.PlaybackStorage.StorageName
	}
	base.AccountType = decision.AccountType
	base.BalanceStatus = decision.Status
	base.FallbackReason = decision.FallbackReason
	return base
}
