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
	if strings.TrimSpace(typesCSV) == "" {
		return 0, fmt.Errorf("typesCSV 不能为空")
	}

	req := e.client.R().
		SetQueryParam("Recursive", "true").
		SetQueryParam("IncludeItemTypes", typesCSV).
		SetQueryParam("Limit", "1").
		SetQueryParam("EnableTotalRecordCount", "true").
		SetQueryParam("Fields", "")
	if libraryID != "" {
		req = req.SetQueryParam("ParentId", libraryID)
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
