package embyhelper

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// PlayedUserData /Users/{id}/Items 返回项里的 UserData（观看态）。
type PlayedUserData struct {
	Played         bool   `json:"Played"`
	PlayCount      int    `json:"PlayCount"`
	LastPlayedDate string `json:"LastPlayedDate"`
}

// PlayedItem 已观看的条目（Movie/Episode），用于历史回填。
type PlayedItem struct {
	ID                string         `json:"Id"`
	Name              string         `json:"Name"`
	Type              string         `json:"Type"`
	SeriesID          string         `json:"SeriesId"`
	SeriesName        string         `json:"SeriesName"`
	ParentIndexNumber *int           `json:"ParentIndexNumber"` // 季号
	IndexNumber       *int           `json:"IndexNumber"`       // 集号
	ProductionYear    int            `json:"ProductionYear"`
	RunTimeTicks      int64          `json:"RunTimeTicks"`
	DateCreated       string         `json:"DateCreated"` // 入库时间，作为缺少 LastPlayedDate 时的回退
	UserData          PlayedUserData `json:"UserData"`
}

type playedItemsResp struct {
	Items            []PlayedItem `json:"Items"`
	TotalRecordCount int          `json:"TotalRecordCount"`
}

// ListPlayedItems 取指定 Emby 用户「已观看」的 Movie/Episode（含 UserData），用于历史回填。
// 需要管理员 APIKey；支持分页(startIndex/limit)。
func (e *EmbyClient) ListPlayedItems(userID string, startIndex, limit int) ([]PlayedItem, int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, 0, fmt.Errorf("userID 不能为空")
	}
	if limit <= 0 {
		limit = 200
	}
	if startIndex < 0 {
		startIndex = 0
	}

	var resp playedItemsResp
	r, err := e.client.R().
		SetQueryParam("Recursive", "true").
		SetQueryParam("IsPlayed", "true").
		SetQueryParam("IncludeItemTypes", "Movie,Episode").
		SetQueryParam("Fields", "DateCreated,SeriesName,ParentIndexNumber,IndexNumber,ProductionYear,RunTimeTicks").
		// 必须显式开启 UserData，否则部分 Emby 版本在指定 Fields 时不返回
		// UserData(含 LastPlayedDate)，会导致回填因「无有效观看时间」全部跳过。
		SetQueryParam("EnableUserData", "true").
		SetQueryParam("EnableImages", "false").
		SetQueryParam("SortBy", "DatePlayed").
		SetQueryParam("SortOrder", "Descending").
		SetQueryParam("StartIndex", strconv.Itoa(startIndex)).
		SetQueryParam("Limit", strconv.Itoa(limit)).
		SetResult(&resp).
		Get("/Users/" + userID + "/Items")
	if err != nil {
		return nil, 0, fmt.Errorf("请求 Emby 已观看列表失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, 0, fmt.Errorf("Emby 已观看列表 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return resp.Items, resp.TotalRecordCount, nil
}

// RuntimeMinutesFromTicks 把 Emby 的 RunTimeTicks 转成分钟（1 tick = 100ns）。
func RuntimeMinutesFromTicks(ticks int64) int {
	if ticks <= 0 {
		return 0
	}
	return int(ticks / 10000000 / 60)
}
