package embyhelper

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ItemBrief Emby Item 列表项的精简结构（用于批量扫描时少拉字段）。
type ItemBrief struct {
	ID             string   `json:"Id"`
	Name           string   `json:"Name"`
	Type           string   `json:"Type"`
	SortName       string   `json:"SortName"`
	ForcedSortName string   `json:"ForcedSortName"`
	LockedFields   []string `json:"LockedFields"`
}

type listItemBriefResp struct {
	Items            []ItemBrief `json:"Items"`
	TotalRecordCount int         `json:"TotalRecordCount"`
}

// ListItemsForSort 分页拉取某个媒体库下指定类型的 Item，返回供 SortName 处理用的精简字段。
// 当 libraryID 为空时，扫描全库。
func (e *EmbyClient) ListItemsForSort(libraryID string, includeTypes []string, startIndex, limit int) ([]ItemBrief, int, error) {
	if limit <= 0 {
		limit = 200
	}
	if len(includeTypes) == 0 {
		// 默认覆盖到 Folder：strm/刮削未识别的电影目录在 Emby Web 字母索引里也会出现，
		// 不回填它们的 SortName 就会让 /Items/Prefixes 出现汉字 prefix。
		includeTypes = []string{"Movie", "Series", "BoxSet", "Folder"}
	}

	req := e.client.R().
		SetQueryParam("Recursive", "true").
		SetQueryParam("IncludeItemTypes", strings.Join(includeTypes, ",")).
		SetQueryParam("Fields", "SortName,ForcedSortName,LockedFields").
		// 用 DateCreated 排序保证分页稳定：写入会改变 SortName，
		// 用 SortName 分页会出现 item 漂移导致漏处理；DateCreated 不会变。
		SetQueryParam("SortBy", "DateCreated").
		SetQueryParam("SortOrder", "Ascending").
		SetQueryParam("StartIndex", strconv.Itoa(startIndex)).
		SetQueryParam("Limit", strconv.Itoa(limit))
	if libraryID != "" {
		req = req.SetQueryParam("ParentId", libraryID)
	}

	endpoint := "/Items"
	if uid := strings.TrimSpace(e.config.Emby.AdminUserID); uid != "" {
		endpoint = "/Users/" + uid + "/Items"
	}

	var resp listItemBriefResp
	r, err := req.SetResult(&resp).Get(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 Emby Items 列表失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, 0, fmt.Errorf("Emby Items 列表 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return resp.Items, resp.TotalRecordCount, nil
}

// GetItemDetail 拉取单个 Item 的完整 ItemDto。Emby 改元数据要求把整个对象 POST 回去。
func (e *EmbyClient) GetItemDetail(itemID string) (map[string]any, error) {
	if itemID == "" {
		return nil, fmt.Errorf("itemID 不能为空")
	}
	uid := strings.TrimSpace(e.config.Emby.AdminUserID)
	if uid == "" {
		return nil, fmt.Errorf("emby.admin_user_id 未配置，无法读取 Item 详情")
	}

	var detail map[string]any
	r, err := e.client.R().
		SetResult(&detail).
		Get(fmt.Sprintf("/Users/%s/Items/%s", uid, itemID))
	if err != nil {
		return nil, fmt.Errorf("请求 Emby Item 详情失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby Item 详情 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	return detail, nil
}

// RefreshLibrary 触发 Emby 全库元数据扫描，让 Emby 重建派生字段和索引（含 /Items/Prefixes 字母索引）。
// 我们写元数据的 SortName/ForcedSortName 已加锁，刮削器不会覆盖，安全。
// 该接口异步：Emby 立即返回，扫描在后端线程跑。
func (e *EmbyClient) RefreshLibrary() (int, string, error) {
	r, err := e.client.R().Post("/Library/Refresh")
	if err != nil {
		return 0, "", fmt.Errorf("触发 Emby 库刷新失败: %w", err)
	}
	status := r.StatusCode()
	respSnippet := truncate(r.String(), 256)
	if status != http.StatusNoContent && status != http.StatusOK {
		return status, respSnippet, fmt.Errorf("触发 Emby 库刷新 HTTP %d: %s", status, respSnippet)
	}
	return status, respSnippet, nil
}

// UpdateItem 把完整的 ItemDto POST 回 Emby，用于更新元数据（如 ForcedSortName / LockedFields）。
// 返回 (statusCode, responseBodySnippet, error)，便于上层定位问题。
func (e *EmbyClient) UpdateItem(itemID string, body map[string]any) (int, string, error) {
	if itemID == "" {
		return 0, "", fmt.Errorf("itemID 不能为空")
	}
	if len(body) == 0 {
		return 0, "", fmt.Errorf("更新 Item 的 body 不能为空")
	}

	r, err := e.client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post(fmt.Sprintf("/Items/%s", itemID))
	if err != nil {
		return 0, "", fmt.Errorf("更新 Emby Item 失败: %w", err)
	}
	status := r.StatusCode()
	respSnippet := truncate(r.String(), 256)
	if status != http.StatusNoContent && status != http.StatusOK {
		return status, respSnippet, fmt.Errorf("更新 Emby Item HTTP %d: %s", status, respSnippet)
	}
	return status, respSnippet, nil
}
