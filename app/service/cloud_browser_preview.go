package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

// CloudBrowserSTRMPreview describes what generation would do for one sample
// file. It is computed with the same plan as generation but never writes.
//
// Status values:
//   - create / update / unchanged: the STRM would be created, overwritten with
//     new content, or left as is because the content already matches.
//   - managed: an organize job owns the local file, generation keeps it.
//   - skipped: the directory configuration excludes the file (see Message).
//   - unsafe: the local path cannot be inspected safely (symlink, non-file...).
//   - empty: a directory selection has no eligible file on its first page.
type CloudBrowserSTRMPreview struct {
	Status               string `json:"status"`
	Message              string `json:"message"`
	Sampled              bool   `json:"sampled"`
	SampleFileID         string `json:"sample_file_id,omitempty"`
	SampleName           string `json:"sample_name,omitempty"`
	MatchedDirectoryName string `json:"matched_directory_name,omitempty"`
	LocalPath            string `json:"local_path,omitempty"`
	Content              string `json:"content,omitempty"`
}

// PreviewSTRM resolves a signed selection to one sample file and reports the
// target path, content and expected outcome. A file selection needs no 115
// request; a directory selection lists only its first page and never descends
// into subdirectories. No lock is taken and nothing is written.
func (s *CloudBrowserService) PreviewSTRM(ctx context.Context, userID, directoryID uint, req CloudBrowserGenerateRequest) (*CloudBrowserSTRMPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	selected, storage, configs, err := s.prepareSelection(ctx, userID, directoryID, req.SelectionToken)
	if err != nil {
		return nil, err
	}
	if selected.Item.IsFile {
		return s.previewFile(ctx, configs, selected.Ancestors, selected.Item, false), nil
	}

	p, err := s.newProvider(storage, selected.Method)
	if err != nil {
		return nil, cloudBrowserProviderError(ctx, err)
	}
	cid := selected.Item.FileID
	page, err := p.List(ctx, cid, 0, CloudBrowserMaxPageSize)
	if err != nil {
		return nil, cloudBrowserProviderError(ctx, err)
	}
	if page == nil || page.CID != cid || page.Offset != 0 || !cloudBrowserValidAncestry(page.Breadcrumbs, cid) || len(page.Items) > CloudBrowserMaxPageSize {
		return nil, cloudMoveError(502, "115 返回的目录路径或分页无效")
	}
	for _, item := range page.Items {
		if item.ParentID != cid || !cloudMoveID(item.FileID, false) {
			return nil, cloudMoveError(502, "115 返回了不属于当前目录的条目")
		}
		if !item.IsFile {
			continue
		}
		// Only an eligible file is a useful sample; excluded files are skipped.
		if plan, reason, planErr := planCloudBrowserSTRM(configs, page.Breadcrumbs, item); planErr == nil && reason == "" && plan != nil {
			return s.previewFile(ctx, configs, page.Breadcrumbs, item, true), nil
		}
	}
	return &CloudBrowserSTRMPreview{
		Status:  "empty",
		Sampled: true,
		Message: "该目录第一页（最多 1150 项）没有可生成 STRM 的文件；子目录未展开预览，生成时仍会递归处理",
	}, nil
}

func (s *CloudBrowserService) previewFile(ctx context.Context, configs []model.MediaDirectory, ancestry []CloudBrowserBreadcrumb, item CloudBrowserEntry, sampled bool) *CloudBrowserSTRMPreview {
	result := &CloudBrowserSTRMPreview{Sampled: sampled, SampleFileID: item.FileID, SampleName: item.Name}
	plan, reason, err := planCloudBrowserSTRM(configs, ancestry, item)
	if err != nil {
		result.Status, result.Message = "unsafe", err.Error()
		return result
	}
	if reason != "" {
		result.Status, result.Message = "skipped", reason
		return result
	}
	result.MatchedDirectoryName = plan.config.DirectoryName
	result.LocalPath, result.Content = plan.localPath, plan.content
	managed, err := s.managedSTRM(ctx, plan.localPath)
	if err != nil {
		result.Status, result.Message = "unsafe", "无法确认本地 STRM 的管理状态"
		return result
	}
	if managed {
		result.Status, result.Message = "managed", "本地 STRM 由整理任务管理，生成时会保留原文件"
		return result
	}
	status, err := inspectCloudBrowserSTRM(plan.root, plan.relative, plan.content)
	if err != nil {
		result.Status, result.Message = "unsafe", "无法安全检查本地 STRM，请检查目录权限、符号链接或文件冲突"
		return result
	}
	result.Status = status
	switch status {
	case "create":
		result.Message = "本地尚无此 STRM，将新建"
	case "update":
		result.Message = "本地已有此 STRM 且内容不同，将覆盖"
	default:
		result.Message = "本地 STRM 内容一致，不会改动"
	}
	return result
}

// inspectCloudBrowserSTRM is the read-only counterpart of writeCloudBrowserSTRM:
// it applies the same root/symlink rules but creates no directory or file.
func inspectCloudBrowserSTRM(rootPath, relative, content string) (string, error) {
	if !filepath.IsAbs(rootPath) || !filepath.IsLocal(relative) || !strings.EqualFold(filepath.Ext(relative), ".strm") {
		return "", pathhelper.ErrUnsafePath
	}
	info, err := os.Lstat(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return "create", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", pathhelper.ErrUnsafePath
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	// Missing parents mean the file does not exist yet; existing parents must be
	// real directories, exactly as generation requires.
	parent := "."
	for _, component := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		parent = filepath.Join(parent, component)
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return "create", nil
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", pathhelper.ErrUnsafePath
		}
	}
	exists, same, err := cloudBrowserExistingSTRM(root, relative, content)
	switch {
	case err != nil:
		return "", err
	case !exists:
		return "create", nil
	case same:
		return "unchanged", nil
	default:
		return "update", nil
	}
}
