package embyhelper

import (
	"fmt"
	"net/http"
	"strings"
)

// countItemsResp 仅取 TotalRecordCount 字段的精简响应结构
type countItemsResp struct {
	TotalRecordCount int `json:"TotalRecordCount"`
}

// CountItems 统计某个媒体库下指定类型的 Item 数量。
//
// 参数：
//   - libraryID  为空表示跨全库统计；非空时通过 ParentId 限定
//   - typesCSV   英文逗号分隔的类型，例如 "Movie" / "Series" / "Movie,Series"
//
// 实现要点：
//   - Recursive=true 必须开启，否则只算顶层（不会进入电影目录子层）
//   - Limit=1 拿一条数据 + EnableTotalRecordCount=true 取总数；
//     Limit=0 在部分 Emby 版本上会被视为"不限制"，反而拉满结果，故避开
func (e *EmbyClient) CountItems(libraryID, typesCSV string) (int, error) {
	return e.CountItemsFiltered(libraryID, typesCSV, nil)
}

// CountItemsFiltered 在 CountItems 基础上附加 Emby /Items 原生过滤参数，
// 例如 IsHD、Is4K、MinDateCreated、Genres。filters 的键值原样作为查询参数发送，
// 调用方负责只传 Emby 支持的参数；不支持的参数会被 Emby 忽略并返回未过滤总数。
func (e *EmbyClient) CountItemsFiltered(libraryID, typesCSV string, filters map[string]string) (int, error) {
	if strings.TrimSpace(typesCSV) == "" {
		return 0, fmt.Errorf("typesCSV 不能为空")
	}

	req := e.client.R().SetContext(e.requestContext()).
		SetQueryParam("Recursive", "true").
		SetQueryParam("IncludeItemTypes", typesCSV).
		SetQueryParam("Limit", "1").
		SetQueryParam("EnableTotalRecordCount", "true").
		SetQueryParam("Fields", "")
	if libraryID != "" {
		req = req.SetQueryParam("ParentId", libraryID)
	}
	for key, value := range filters {
		req = req.SetQueryParam(key, value)
	}

	endpoint := "/Items"
	if uid := strings.TrimSpace(e.config.Emby.AdminUserID); uid != "" {
		endpoint = "/Users/" + uid + "/Items"
	}

	var resp countItemsResp
	r, err := req.SetResult(&resp).Get(endpoint)
	if err != nil {
		return 0, fmt.Errorf("请求 Emby Items 数量失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return 0, fmt.Errorf("Emby Items 数量 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return resp.TotalRecordCount, nil
}

// GenreCount 是 /Genres 返回的类型及其电影、剧集数量。
// 部分 Emby 版本不填充 MovieCount/SeriesCount，此时两者为 0，调用方需回退逐类型计数。
type GenreCount struct {
	Name        string `json:"Name"`
	MovieCount  int    `json:"MovieCount"`
	SeriesCount int    `json:"SeriesCount"`
}

// ListGenres 读取跨全库的电影 / 剧集类型列表，并请求 ItemCounts 字段。
func (e *EmbyClient) ListGenres(typesCSV string) ([]GenreCount, error) {
	req := e.client.R().SetContext(e.requestContext()).
		SetQueryParam("Recursive", "true").
		SetQueryParam("IncludeItemTypes", typesCSV).
		SetQueryParam("Fields", "ItemCounts").
		SetQueryParam("SortBy", "SortName")
	if uid := strings.TrimSpace(e.config.Emby.AdminUserID); uid != "" {
		req = req.SetQueryParam("UserId", uid)
	}

	var resp struct {
		Items []GenreCount `json:"Items"`
	}
	r, err := req.SetResult(&resp).Get("/Genres")
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 类型列表失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby 类型列表 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return resp.Items, nil
}
