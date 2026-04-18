package embyhelper

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// EmbyLibrary 表示一个 Emby 媒体库（CollectionFolder）
type EmbyLibrary struct {
	ID                string   `json:"Id"`
	Name              string   `json:"Name"`
	CollectionType    string   `json:"CollectionType"` // movies / tvshows / music / mixed / homevideos ...
	ServerID          string   `json:"ServerId"`
	PrimaryImageTag   string   `json:"-"`
	IsFolder          bool     `json:"IsFolder"`
	ChildCount        int      `json:"ChildCount"`
	Path              string   `json:"Path"`
	BackdropImageTags []string `json:"BackdropImageTags,omitempty"`
}

// EmbyItem 表示一个媒体项（Movie/Series 等），用于取最新海报
type EmbyItem struct {
	ID           string            `json:"Id"`
	Name         string            `json:"Name"`
	Type         string            `json:"Type"`
	DateCreated  string            `json:"DateCreated"`
	ImageTags    map[string]string `json:"ImageTags,omitempty"`
	BackdropTags []string          `json:"BackdropImageTags,omitempty"`
	ProviderIDs  map[string]string `json:"ProviderIds,omitempty"`
}

// listMediaFoldersResp /Library/MediaFolders 响应
type listMediaFoldersResp struct {
	Items            []EmbyLibrary `json:"Items"`
	TotalRecordCount int           `json:"TotalRecordCount"`
}

// listItemsResp /Users/{UserId}/Items 响应
type listItemsResp struct {
	Items            []EmbyItem `json:"Items"`
	TotalRecordCount int        `json:"TotalRecordCount"`
}

// ListLibraries 列出所有媒体库（CollectionFolder）
// Emby /Library/MediaFolders 返回当前 api_key 可见的所有库
func (e *EmbyClient) ListLibraries() ([]EmbyLibrary, error) {
	var resp listMediaFoldersResp
	r, err := e.client.R().
		SetResult(&resp).
		Get("/Library/MediaFolders")
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 媒体库列表失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby 媒体库列表 HTTP %d: %s", r.StatusCode(), r.String())
	}
	// 过滤掉非 CollectionFolder（playlists / boxsets 等也会出现）
	out := make([]EmbyLibrary, 0, len(resp.Items))
	for _, it := range resp.Items {
		if it.ID == "" {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// ListLatestItems 取某个库下最新入库的 N 个 Movie / Series（按 DateCreated 降序）
// 需要 admin_user_id 才能正确过滤；如未配置则用 /Items 顶层接口（要求 api_key 是管理员密钥）
func (e *EmbyClient) ListLatestItems(libraryID string, limit int, includeTypes []string) ([]EmbyItem, error) {
	if libraryID == "" {
		return nil, fmt.Errorf("libraryID 不能为空")
	}
	if limit <= 0 {
		limit = 9
	}
	if len(includeTypes) == 0 {
		includeTypes = []string{"Movie", "Series"}
	}

	req := e.client.R().
		SetQueryParam("ParentId", libraryID).
		SetQueryParam("Recursive", "true").
		SetQueryParam("IncludeItemTypes", strings.Join(includeTypes, ",")).
		SetQueryParam("SortBy", "DateCreated,SortName").
		SetQueryParam("SortOrder", "Descending").
		SetQueryParam("Limit", strconv.Itoa(limit)).
		SetQueryParam("Fields", "DateCreated,PrimaryImageAspectRatio,ImageTags,BackdropImageTags").
		SetQueryParam("ImageTypeLimit", "1").
		SetQueryParam("EnableImageTypes", "Primary,Backdrop,Thumb")

	var resp listItemsResp
	req.SetResult(&resp)

	endpoint := "/Items"
	if uid := strings.TrimSpace(e.config.Emby.AdminUserID); uid != "" {
		endpoint = "/Users/" + uid + "/Items"
	}

	r, err := req.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 最新项失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby 最新项 HTTP %d: %s", r.StatusCode(), r.String())
	}
	return resp.Items, nil
}

// DownloadImage 拉取某个 Item 的图片字节
// imageType: Primary / Backdrop / Thumb / Logo
// maxWidth=0 表示不缩放（取原图）
func (e *EmbyClient) DownloadImage(itemID, imageType string, maxWidth int) ([]byte, string, error) {
	if itemID == "" {
		return nil, "", fmt.Errorf("itemID 不能为空")
	}
	if imageType == "" {
		imageType = "Primary"
	}

	req := e.client.R()
	if maxWidth > 0 {
		req = req.SetQueryParam("maxWidth", strconv.Itoa(maxWidth)).
			SetQueryParam("quality", "90")
	}
	r, err := req.Get(fmt.Sprintf("/Items/%s/Images/%s", itemID, imageType))
	if err != nil {
		return nil, "", fmt.Errorf("下载 Emby 图片失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, "", fmt.Errorf("Emby 图片 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	data := r.Bytes()
	if len(data) == 0 {
		return nil, "", fmt.Errorf("Emby 图片为空: itemID=%s type=%s", itemID, imageType)
	}
	return append([]byte(nil), data...), r.Header().Get("Content-Type"), nil
}

// truncate 截断字符串辅助函数
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// UploadPrimaryImage 上传 Primary 类型图片到指定 Item
// Emby 要求 body 必须是 base64 字符串（不是原始字节），Content-Type 是图片 MIME
// itemID 既可以是普通 Item，也可以是 CollectionFolder（媒体库本身）的 ID
func (e *EmbyClient) UploadPrimaryImage(itemID string, imageBytes []byte, contentType string) error {
	if itemID == "" {
		return fmt.Errorf("itemID 不能为空")
	}
	if len(imageBytes) == 0 {
		return fmt.Errorf("图片字节为空")
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}

	encoded := base64.StdEncoding.EncodeToString(imageBytes)

	r, err := e.client.R().
		SetHeader("Content-Type", contentType).
		SetBody(encoded).
		Post(fmt.Sprintf("/Items/%s/Images/Primary", itemID))
	if err != nil {
		return fmt.Errorf("上传 Emby 封面失败: %w", err)
	}
	if r.StatusCode() != http.StatusNoContent && r.StatusCode() != http.StatusOK {
		return fmt.Errorf("上传 Emby 封面 HTTP %d: %s", r.StatusCode(), r.String())
	}
	return nil
}

// DeleteImage 删除指定 Item 的某种类型图片（用于清除旧封面）
func (e *EmbyClient) DeleteImage(itemID, imageType string) error {
	if itemID == "" {
		return fmt.Errorf("itemID 不能为空")
	}
	if imageType == "" {
		imageType = "Primary"
	}
	r, err := e.client.R().Delete(fmt.Sprintf("/Items/%s/Images/%s", itemID, imageType))
	if err != nil {
		return fmt.Errorf("删除 Emby 图片失败: %w", err)
	}
	if r.StatusCode() != http.StatusNoContent && r.StatusCode() != http.StatusOK && r.StatusCode() != http.StatusNotFound {
		return fmt.Errorf("删除 Emby 图片 HTTP %d: %s", r.StatusCode(), r.String())
	}
	return nil
}
