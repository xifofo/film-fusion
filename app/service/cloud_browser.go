package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

// Browser requests are deliberately independent of the background token manager:
// no authentication probe, refresh, retry, fallback, or pagination is implicit.
type CloudBrowserBreadcrumb struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

type CloudBrowserEntry struct {
	FileID                string `json:"file_id"`
	ParentID              string `json:"parent_id"`
	Name                  string `json:"name"`
	IsFile                bool   `json:"is_file"`
	Size                  int64  `json:"size"`
	Complete              bool   `json:"complete"`
	SelectionToken        string `json:"selection_token,omitempty"`
	MatchedDirectoryID    uint   `json:"matched_directory_id,omitempty"`
	MatchedDirectoryName  string `json:"matched_directory_name,omitempty"`
	STRMUnavailableReason string `json:"strm_unavailable_reason,omitempty"`
}

type CloudBrowserDirectoryMatch struct {
	ID            uint   `json:"id"`
	DirectoryName string `json:"directory_name"`
}

type CloudBrowserPage struct {
	CID                   string                      `json:"cid"`
	Offset                int                         `json:"offset"`
	Limit                 int                         `json:"limit"`
	Total                 int64                       `json:"total"`
	HasMore               bool                        `json:"has_more"`
	AccessMethod          string                      `json:"access_method"`
	AvailableMethods      []string                    `json:"available_methods"`
	Breadcrumbs           []CloudBrowserBreadcrumb    `json:"breadcrumbs"`
	Items                 []CloudBrowserEntry         `json:"items"`
	MatchedDirectory      *CloudBrowserDirectoryMatch `json:"matched_directory"`
	CurrentDirectoryToken string                      `json:"current_directory_token"`
	STRMUnavailableReason string                      `json:"strm_unavailable_reason,omitempty"`
}

type CloudBrowserGenerateRequest struct {
	SelectionToken string `json:"selection_token"`
}

type cloudBrowserSelection struct {
	UserID           uint                     `json:"u"`
	EntryDirectoryID uint                     `json:"d"`
	StorageID        uint                     `json:"s"`
	Method           string                   `json:"m"`
	Expires          int64                    `json:"e"`
	Ancestors        []CloudBrowserBreadcrumb `json:"a"`
	Item             CloudBrowserEntry        `json:"i"`
}

type cloudBrowserProvider interface {
	List(context.Context, string, int, int) (*CloudBrowserPage, error)
}

type CloudBrowserService struct {
	db          *gorm.DB
	web         *Web115Service
	key         []byte
	newProvider func(model.CloudStorage, string) (cloudBrowserProvider, error)
	mu          sync.Mutex
	jobs        map[string]*cloudBrowserJob
	stopped     bool
	wg          sync.WaitGroup
}

func NewCloudBrowserService(db *gorm.DB, web *Web115Service) *CloudBrowserService {
	s := &CloudBrowserService{db: db, web: web, jobs: make(map[string]*cloudBrowserJob)}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err == nil {
		s.key = key
	}
	s.newProvider = s.makeBrowserProvider
	return s
}

func (s *CloudBrowserService) directory(ctx context.Context, userID, directoryID uint) (model.CloudDirectory, error) {
	var directory model.CloudDirectory
	if userID == 0 || directoryID == 0 {
		return directory, cloudMoveError(400, "目录配置参数无效")
	}
	if s.db == nil {
		return directory, cloudMoveError(500, "数据库不可用")
	}
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", directoryID, userID).First(&directory).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return directory, cloudMoveError(404, "未找到当前用户的目录配置")
	}
	if err != nil {
		return directory, cloudMoveError(500, "读取目录配置失败")
	}
	return directory, nil
}

func (s *CloudBrowserService) directories(ctx context.Context, userID, storageID uint) ([]model.CloudDirectory, error) {
	var directories []model.CloudDirectory
	if err := s.db.WithContext(ctx).Where("user_id = ? AND cloud_storage_id = ?", userID, storageID).Find(&directories).Error; err != nil {
		return nil, cloudMoveError(500, "读取目录配置失败")
	}
	return directories, nil
}

func (s *CloudBrowserService) storage(ctx context.Context, userID, storageID uint, method string) (model.CloudStorage, error) {
	// This existing validator is local-only and never refreshes credentials.
	return (&CloudFolderMoveService{db: s.db}).storage(ctx, userID, storageID, method)
}

func (s *CloudBrowserService) Browse(ctx context.Context, userID, directoryID uint, method, cid string, offset, limit int) (*CloudBrowserPage, error) {
	if (method != "cookie" && method != "openapi") || offset < 0 || offset > 10000000 || limit < 1 || limit > 200 || (cid != "" && !cloudMoveID(cid, true)) {
		return nil, cloudMoveError(400, "请选择访问方式、有效目录和分页范围（每页最多 200 项）")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	directory, err := s.directory(ctx, userID, directoryID)
	if err != nil {
		return nil, err
	}
	if cid == "" {
		cid = directory.DirectoryID
	}
	if !cloudMoveID(cid, true) {
		return nil, cloudMoveError(400, "目录配置中的 115 目录 ID 无效")
	}
	storage, err := s.storage(ctx, userID, directory.CloudStorageID, method)
	if err != nil {
		return nil, err
	}
	configs, err := s.directories(ctx, userID, storage.ID)
	if err != nil {
		return nil, err
	}
	p, err := s.newProvider(storage, method)
	if err != nil {
		return nil, cloudBrowserProviderError(ctx, err)
	}
	page, err := p.List(ctx, cid, offset, limit)
	if err != nil {
		return nil, cloudBrowserProviderError(ctx, err)
	}
	if page == nil || page.CID != cid || page.Offset != offset || !cloudBrowserValidAncestry(page.Breadcrumbs, cid) {
		return nil, cloudMoveError(502, "115 返回的目录路径或分页无效")
	}
	page.AccessMethod = method
	page.AvailableMethods = make([]string, 0, 2)
	if strings.TrimSpace(storage.Cookie) != "" {
		page.AvailableMethods = append(page.AvailableMethods, "cookie")
	}
	if strings.TrimSpace(storage.AccessToken) != "" {
		page.AvailableMethods = append(page.AvailableMethods, "openapi")
	}
	selected := cloudBrowserSelection{UserID: userID, EntryDirectoryID: directoryID, StorageID: storage.ID, Method: method, Expires: time.Now().Add(15 * time.Minute).Unix()}
	current := page.Breadcrumbs[len(page.Breadcrumbs)-1]
	selected.Ancestors = page.Breadcrumbs
	selected.Item = CloudBrowserEntry{FileID: current.ID, ParentID: current.ParentID, Name: current.Name, Complete: true}
	page.CurrentDirectoryToken, err = s.signSelection(selected)
	if err != nil {
		return nil, err
	}
	matched, _, matchErr := cloudBrowserMatchDirectory(configs, page.Breadcrumbs)
	if matchErr != nil {
		page.STRMUnavailableReason = "当前路径匹配到重复的目录配置，请先修正配置"
	} else if matched == nil {
		page.STRMUnavailableReason = "当前目录未匹配目录配置"
	} else {
		page.MatchedDirectory = &CloudBrowserDirectoryMatch{ID: matched.ID, DirectoryName: matched.DirectoryName}
		page.STRMUnavailableReason = cloudBrowserSTRMUnavailable(matched, selected.Item)
	}
	for i := range page.Items {
		item := &page.Items[i]
		ancestry := page.Breadcrumbs
		if !item.IsFile {
			ancestry = append(append([]CloudBrowserBreadcrumb(nil), ancestry...), CloudBrowserBreadcrumb{ID: item.FileID, ParentID: item.ParentID, Name: item.Name})
		}
		match, _, matchErr := cloudBrowserMatchDirectory(configs, ancestry)
		if matchErr != nil {
			item.STRMUnavailableReason = "当前路径匹配到重复的目录配置，请先修正配置"
		} else if match == nil {
			item.STRMUnavailableReason = "未匹配当前账号的目录配置"
		} else {
			item.MatchedDirectoryID, item.MatchedDirectoryName = match.ID, match.DirectoryName
			item.STRMUnavailableReason = cloudBrowserSTRMUnavailable(match, *item)
		}
		selected.Ancestors = ancestry
		selected.Item = *item
		item.SelectionToken, err = s.signSelection(selected)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (s *CloudBrowserService) signSelection(selection cloudBrowserSelection) (string, error) {
	if len(s.key) != 32 {
		return "", cloudMoveError(500, "无法创建目录选择凭据")
	}
	selection.Item.SelectionToken = ""
	data, err := json.Marshal(selection)
	if err != nil {
		return "", cloudMoveError(500, "无法创建目录选择凭据")
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *CloudBrowserService) parseSelection(token string, userID, directoryID uint) (cloudBrowserSelection, error) {
	var selection cloudBrowserSelection
	bad := cloudMoveError(400, "文件选择已失效，请手动刷新目录后重新选择")
	if len(token) > 32<<10 || len(s.key) != 32 {
		return selection, bad
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return selection, bad
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return selection, bad
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return selection, bad
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(data, &selection) != nil || selection.UserID != userID || selection.EntryDirectoryID != directoryID || selection.Expires < time.Now().Unix() || len(selection.Ancestors) == 0 {
		return cloudBrowserSelection{}, bad
	}
	return selection, nil
}

func cloudBrowserProviderError(ctx context.Context, err error) error {
	var known *CloudFolderMoveError
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return cloudMoveError(408, "115 目录请求已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cloudMoveError(504, "115 目录请求超时，请稍后手动重试")
	}
	return cloudMoveError(http.StatusBadGateway, "115 目录请求失败，请检查所选方式的授权后手动重试；系统未自动重试或切换方式")
}
