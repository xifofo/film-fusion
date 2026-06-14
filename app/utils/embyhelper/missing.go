package embyhelper

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// MissingEpisodeItem /Shows/Missing 返回的单条缺失剧集(精简字段)。
type MissingEpisodeItem struct {
	ID                string `json:"Id"`
	Name              string `json:"Name"`
	IndexNumber       *int   `json:"IndexNumber"`       // 集号
	ParentIndexNumber *int   `json:"ParentIndexNumber"` // 季号
	SeriesID          string `json:"SeriesId"`
	SeriesName        string `json:"SeriesName"`
	SeasonName        string `json:"SeasonName"`
	PremiereDate      string `json:"PremiereDate"`
}

type missingEpisodesResp struct {
	Items            []MissingEpisodeItem `json:"Items"`
	TotalRecordCount int                  `json:"TotalRecordCount"`
}

// ListMissingEpisodes 调 Emby /Shows/Missing 获取缺失剧集。
// parentID 为空表示不限制媒体库(整服务器)；非空时按该库/剧集范围统计。
// 需要配置 admin_user_id。支持分页(startIndex/limit)。
func (e *EmbyClient) ListMissingEpisodes(parentID string, includeSpecials, includeUnaired bool, startIndex, limit int) ([]MissingEpisodeItem, int, error) {
	uid := strings.TrimSpace(e.config.Emby.AdminUserID)
	if uid == "" {
		return nil, 0, fmt.Errorf("未配置 Emby admin_user_id，无法扫描缺集")
	}
	if limit <= 0 {
		limit = 200
	}
	if startIndex < 0 {
		startIndex = 0
	}

	req := e.client.R().
		SetQueryParam("UserId", uid).
		SetQueryParam("IncludeSpecials", strconv.FormatBool(includeSpecials)).
		SetQueryParam("IncludeUnaired", strconv.FormatBool(includeUnaired)).
		SetQueryParam("Fields", "PremiereDate").
		SetQueryParam("StartIndex", strconv.Itoa(startIndex)).
		SetQueryParam("Limit", strconv.Itoa(limit)).
		SetQueryParam("EnableImages", "false").
		SetQueryParam("EnableUserData", "false")
	if strings.TrimSpace(parentID) != "" {
		req = req.SetQueryParam("ParentId", strings.TrimSpace(parentID))
	}

	var resp missingEpisodesResp
	r, err := req.SetResult(&resp).Get("/Shows/Missing")
	if err != nil {
		return nil, 0, fmt.Errorf("请求 Emby 缺集失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, 0, fmt.Errorf("Emby 缺集 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return resp.Items, resp.TotalRecordCount, nil
}
