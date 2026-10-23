package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

type CloudFolderMoveError struct {
	Status  int
	Message string
}

func (e *CloudFolderMoveError) Error() string         { return e.Message }
func cloudMoveError(status int, message string) error { return &CloudFolderMoveError{status, message} }

type CloudBrowseItem struct {
	FileID   string `json:"file_id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	IsFile   bool   `json:"is_file"`
	Size     int64  `json:"size"`
}
type CloudBrowseResult struct {
	Items  []CloudBrowseItem `json:"items"`
	Total  int64             `json:"total"`
	CID    string            `json:"cid"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
}
type CloudFolderMoveRequest struct {
	AccessMethod   string `json:"access_method"`
	SourceParentID string `json:"source_parent_id"`
	FileID         string `json:"file_id"`
	FileName       string `json:"file_name"`
	TargetParentID string `json:"target_parent_id"`
	FolderName     string `json:"folder_name"`
	FolderID       string `json:"folder_id,omitempty"`
}
type CloudFolderMoveResult struct {
	Status       string                     `json:"status"`
	FolderID     string                     `json:"folder_id"`
	FolderName   string                     `json:"folder_name"`
	FileID       string                     `json:"file_id"`
	FileName     string                     `json:"file_name"`
	ReusedFolder bool                       `json:"reused_folder"`
	STRM         *CloudFolderMoveStrmResult `json:"strm,omitempty"`
}

type cloudFolderProvider interface {
	List(context.Context, string, int, int) (*CloudBrowseResult, error)
	Mkdir(context.Context, string, string) (string, error)
	Move(context.Context, string, string) error
	ResolveDirectoryPath(context.Context, string) (string, error)
	Close()
}
type CloudFolderMoveService struct {
	db          *gorm.DB
	web         *Web115Service
	newProvider func(context.Context, model.CloudStorage, string) (cloudFolderProvider, error)
}

func NewCloudFolderMoveService(db *gorm.DB, web *Web115Service) *CloudFolderMoveService {
	s := &CloudFolderMoveService{db: db, web: web}
	s.newProvider = s.makeProvider
	return s
}

func cloudMoveID(id string, root bool) bool {
	if id == "" || len(id) > 20 || (len(id) > 1 && id[0] == '0') {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && (root || n > 0)
}
func cloudMoveName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\<>:"|?*`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (s *CloudFolderMoveService) storage(ctx context.Context, userID, storageID uint, method string) (model.CloudStorage, error) {
	var storage model.CloudStorage
	if userID == 0 || storageID == 0 || (method != "cookie" && method != "openapi") {
		return storage, cloudMoveError(400, "请选择有效账号及 Cookie 或 OpenAPI 访问方式")
	}
	if s.db == nil {
		return storage, cloudMoveError(500, "存储数据库不可用")
	}
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", storageID, userID).First(&storage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return storage, cloudMoveError(404, "未找到当前用户的存储账号")
		}
		return storage, cloudMoveError(500, "读取存储账号失败")
	}
	if storage.StorageType != model.StorageType115Open || storage.Status != model.StatusActive {
		return storage, cloudMoveError(409, "请选择启用中的 115 存储账号")
	}
	if method == "cookie" && strings.TrimSpace(storage.Cookie) == "" {
		return storage, cloudMoveError(400, "所选账号尚未配置 Cookie")
	}
	if method == "openapi" {
		if strings.TrimSpace(storage.AccessToken) == "" {
			return storage, cloudMoveError(400, "所选账号尚未配置 OpenAPI 授权")
		}
		if storage.TokenExpiresAt != nil && !storage.TokenExpiresAt.After(time.Now()) {
			return storage, cloudMoveError(409, "OpenAPI 授权已过期，请先在存储设置中更新授权")
		}
	}
	return storage, nil
}
func (s *CloudFolderMoveService) Browse(ctx context.Context, userID, storageID uint, method, cid string, offset, limit int) (*CloudBrowseResult, error) {
	if !cloudMoveID(cid, true) || offset < 0 || offset > 10000000 || limit < 1 || limit > 200 {
		return nil, cloudMoveError(400, "目录或分页参数无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	storage, err := s.storage(ctx, userID, storageID, method)
	if err != nil {
		return nil, err
	}
	p, err := s.newProvider(ctx, storage, method)
	if err != nil {
		return nil, safeCloudProviderError(ctx, err)
	}
	defer p.Close()
	page, err := p.List(ctx, cid, offset, limit)
	if err != nil {
		return nil, safeCloudProviderError(ctx, err)
	}
	return page, nil
}
func safeCloudProviderError(ctx context.Context, err error) error {
	var known *CloudFolderMoveError
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return cloudMoveError(408, "操作已取消；如已开始移动，请重新检查文件位置")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cloudMoveError(504, "115 请求超时；请检查文件位置后重试")
	}
	return cloudMoveError(502, "115 请求失败，请检查所选访问方式的授权后重试")
}

// The same database lock is used by organize execution. A bounded lease also
// protects against a worker crash; no provider mutation is retried implicitly.
func (s *CloudFolderMoveService) lock(ctx context.Context, storageID uint) (func(), error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, cloudMoveError(500, "创建账号操作锁失败")
	}
	key := "folder-move-" + hex.EncodeToString(random[:])
	until := time.Now().Add(3 * time.Minute)
	if err := s.db.WithContext(ctx).Where("cloud_storage_id = ? AND lease_until IS NOT NULL AND lease_until < ?", storageID, time.Now()).Delete(&model.OrganizeStorageLock{}).Error; err != nil {
		return nil, cloudMoveError(500, "检查账号操作锁失败")
	}
	if err := s.db.WithContext(ctx).Create(&model.OrganizeStorageLock{CloudStorageID: storageID, PlanID: key, LeaseUntil: &until}).Error; err != nil {
		return nil, cloudMoveError(409, "此账号正在整理或移动文件，请稍后重试")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.db.WithContext(cleanup).Where("cloud_storage_id = ? AND plan_id = ?", storageID, key).Delete(&model.OrganizeStorageLock{})
	}, nil
}

func cloudFolderEntries(ctx context.Context, p cloudFolderProvider, cid string) ([]CloudBrowseItem, error) {
	items := make([]CloudBrowseItem, 0)
	seen := map[string]bool{}
	for offset := 0; offset < 10000; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := p.List(ctx, cid, offset, 200)
		if err != nil {
			return nil, err
		}
		if page == nil || page.CID != cid || page.Offset != offset || page.Total < 0 || len(page.Items) > 200 || (len(page.Items) > 0 && page.Total < int64(offset+len(page.Items))) {
			return nil, errors.New("invalid directory response")
		}
		for _, item := range page.Items {
			if !cloudMoveID(item.FileID, false) || item.ParentID != cid || item.Name == "" || seen[item.FileID] {
				return nil, errors.New("invalid directory entry")
			}
			seen[item.FileID] = true
			items = append(items, item)
		}
		offset += len(page.Items)
		if int64(offset) >= page.Total {
			return items, nil
		}
		if len(page.Items) == 0 {
			return nil, errors.New("incomplete directory listing")
		}
	}
	return nil, cloudMoveError(409, "目录超过 10000 个条目，请选择较小的目录后操作")
}
func cloudFindFolder(items []CloudBrowseItem, name, id string) (string, error) {
	found := ""
	for _, item := range items {
		if item.FileID == id && id != "" && (item.Name != name || item.IsFile) {
			return "", cloudMoveError(409, "重试目录已被重命名或不再是文件夹，请重新选择位置")
		}
		if !strings.EqualFold(item.Name, name) {
			continue
		}
		if item.Name != name || item.IsFile || found != "" {
			return "", cloudMoveError(409, "目标位置存在同名文件或多个同名目录，无法安全创建文件夹")
		}
		found = item.FileID
	}
	if id != "" && found != id {
		return "", cloudMoveError(409, "重试目录不在指定位置或名称已改变，请重新选择位置")
	}
	return found, nil
}
func cloudCheckDestination(items []CloudBrowseItem, req CloudFolderMoveRequest) (bool, error) {
	already := false
	for _, item := range items {
		if item.FileID == req.FileID {
			if !item.IsFile || item.Name != req.FileName {
				return false, cloudMoveError(409, "所选文件名称已改变，请重新选择文件")
			}
			already = true
		} else if strings.EqualFold(item.Name, req.FileName) {
			return false, cloudMoveError(409, "目标文件夹中已有同名条目，已停止移动以避免覆盖")
		}
	}
	return already, nil
}
func cloudCheckSource(items []CloudBrowseItem, req CloudFolderMoveRequest) error {
	for _, item := range items {
		if item.FileID == req.FileID {
			if !item.IsFile || item.Name != req.FileName {
				return cloudMoveError(409, "所选条目不是原文件或名称已改变，请重新选择文件")
			}
			return nil
		}
	}
	return cloudMoveError(409, "原目录中已找不到所选文件，请刷新后重新选择")
}

func (s *CloudFolderMoveService) Move(ctx context.Context, userID, storageID uint, req CloudFolderMoveRequest) (*CloudFolderMoveResult, error) {
	req.FolderName = strings.TrimSpace(req.FolderName)
	if !cloudMoveID(req.SourceParentID, true) || !cloudMoveID(req.TargetParentID, true) || !cloudMoveID(req.FileID, false) || (req.FolderID != "" && !cloudMoveID(req.FolderID, false)) || !cloudMoveName(req.FolderName) || req.FileName == "" || len(req.FileName) > 1024 {
		return nil, cloudMoveError(400, "请选择源文件、自定义目标位置，并填写有效文件夹名称")
	}
	result := &CloudFolderMoveResult{Status: "pending", FolderName: req.FolderName, FileID: req.FileID, FileName: req.FileName}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	storage, err := s.storage(ctx, userID, storageID, req.AccessMethod)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(ctx, storageID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := s.newProvider(ctx, storage, req.AccessMethod)
	if err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	defer p.Close()
	target, err := cloudFolderEntries(ctx, p, req.TargetParentID)
	if err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	result.FolderID, err = cloudFindFolder(target, req.FolderName, req.FolderID)
	if err != nil {
		return result, err
	}
	result.ReusedFolder = result.FolderID != ""
	if result.FolderID != "" {
		entries, e := cloudFolderEntries(ctx, p, result.FolderID)
		if e != nil {
			return result, safeCloudProviderError(ctx, e)
		}
		already, e := cloudCheckDestination(entries, req)
		if e != nil {
			return result, e
		}
		if already {
			result.Status = "already_moved"
			result.STRM = s.createMovedFileSTRM(ctx, p, userID, storageID, result)
			return result, nil
		}
	}
	source, err := cloudFolderEntries(ctx, p, req.SourceParentID)
	if err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	if err = cloudCheckSource(source, req); err != nil {
		return result, err
	}
	if result.FolderID == "" {
		created, createErr := p.Mkdir(ctx, req.TargetParentID, req.FolderName)
		if cloudMoveID(created, false) {
			result.FolderID = created
		}
		// Read back even after an ambiguous create failure, but never retry it.
		entries, readErr := cloudFolderEntries(ctx, p, req.TargetParentID)
		if readErr == nil {
			found, findErr := cloudFindFolder(entries, req.FolderName, created)
			if findErr == nil {
				result.FolderID = found
			} else {
				return result, findErr
			}
		}
		if createErr != nil {
			return result, safeCloudProviderError(ctx, createErr)
		}
		if readErr != nil {
			if cloudMoveID(created, false) {
				result.FolderID = created
			}
			return result, safeCloudProviderError(ctx, readErr)
		}
		if result.FolderID == "" || !cloudMoveID(created, false) || result.FolderID != created {
			return result, cloudMoveError(502, "无法确认新建文件夹，请刷新目标位置后重试")
		}
	}
	// Refresh both sides immediately before the one mutation, including reused
	// folders; another 115 client may have changed their contents meanwhile.
	entries, err := cloudFolderEntries(ctx, p, result.FolderID)
	if err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	already, err := cloudCheckDestination(entries, req)
	if err != nil {
		return result, err
	}
	if already {
		result.Status = "already_moved"
		result.STRM = s.createMovedFileSTRM(ctx, p, userID, storageID, result)
		return result, nil
	}
	source, err = cloudFolderEntries(ctx, p, req.SourceParentID)
	if err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	if err = cloudCheckSource(source, req); err != nil {
		return result, err
	}
	parentEntries, parentErr := cloudFolderEntries(ctx, p, req.TargetParentID)
	if parentErr != nil {
		return result, safeCloudProviderError(ctx, parentErr)
	}
	if _, parentErr = cloudFindFolder(parentEntries, req.FolderName, result.FolderID); parentErr != nil {
		return result, parentErr
	}
	if err = ctx.Err(); err != nil {
		return result, safeCloudProviderError(ctx, err)
	}
	moveErr := p.Move(ctx, req.FileID, result.FolderID)
	verified, verifyErr := cloudFolderEntries(ctx, p, result.FolderID)
	if verifyErr == nil {
		moved, e := cloudCheckDestination(verified, req)
		if e == nil && moved {
			result.Status = "moved"
			result.STRM = s.createMovedFileSTRM(ctx, p, userID, storageID, result)
			return result, nil
		}
		if e != nil {
			return result, e
		}
	}
	if moveErr != nil {
		return result, safeCloudProviderError(ctx, moveErr)
	}
	if verifyErr != nil {
		return result, safeCloudProviderError(ctx, verifyErr)
	}
	return result, cloudMoveError(http.StatusBadGateway, fmt.Sprintf("115 尚未确认文件已移动；文件夹 %s 已保留，可安全重试", result.FolderName))
}
