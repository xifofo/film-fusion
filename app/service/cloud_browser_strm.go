package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

type CloudBrowserSTRMItemResult struct {
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	LocalPath   string `json:"local_path,omitempty"`
	DirectoryID uint   `json:"directory_id,omitempty"`
}

// The caller loads configurations for one authenticated user and storage. Match
// real 115 IDs, not names, and prefer the deepest configured ancestor.
func cloudBrowserMatchDirectory(configs []model.MediaDirectory, ancestry []CloudBrowserBreadcrumb) (*model.MediaDirectory, int, error) {
	seen := make(map[string]bool, len(ancestry))
	for i, crumb := range ancestry {
		if !cloudMoveID(crumb.ID, true) || seen[crumb.ID] || (i > 0 && crumb.ParentID != ancestry[i-1].ID) {
			return nil, -1, errors.New("目录层级无效，无法匹配目录配置")
		}
		if crumb.ID != "0" && !cloudSTRMFileComponent(crumb.Name) {
			return nil, -1, errors.New("目录名称不能安全映射到本地路径")
		}
		seen[crumb.ID] = true
	}
	for depth := len(ancestry) - 1; depth >= 0; depth-- {
		var matched *model.MediaDirectory
		for i := range configs {
			if configs[i].DirectoryID != ancestry[depth].ID {
				continue
			}
			if matched != nil {
				return nil, -1, errors.New("同一目录匹配多个目录配置，请先修正重复配置")
			}
			matched = &configs[i]
		}
		if matched != nil {
			return matched, depth, nil
		}
	}
	return nil, -1, nil
}

func cloudBrowserAliasParts(name string) ([]string, error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" {
		return nil, nil
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if !cloudSTRMFileComponent(part) {
			return nil, errors.New("目录配置名称含不安全的路径片段")
		}
	}
	return parts, nil
}

func cloudBrowserExtensions(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	var values []string
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		if json.Unmarshal([]byte(raw), &values) != nil {
			return nil, errors.New("文件后缀过滤规则无效")
		}
	} else if strings.HasPrefix(raw, `"`) {
		var value string
		if json.Unmarshal([]byte(raw), &value) != nil {
			return nil, errors.New("文件后缀过滤规则无效")
		}
		values = []string{value}
	} else {
		values = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
		})
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "."))
		if value != "" {
			result[value] = true
		}
	}
	return result, nil
}

func cloudBrowserSubtitle(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".zip", ".rar", ".7z", ".gz", ".bz2", ".xz", ".tar":
		ext = strings.ToLower(path.Ext(strings.TrimSuffix(name, path.Ext(name))))
	}
	switch ext {
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		return true
	}
	return false
}

// Availability does not touch the provider or local filesystem. Directories use
// only configuration checks; each discovered file gets its own filter check.
func cloudBrowserSTRMUnavailable(config *model.MediaDirectory, item CloudBrowserEntry) string {
	if config == nil {
		return "未匹配当前账号的目录配置"
	}
	if root := strings.TrimSpace(config.SavePath); root == "" || !filepath.IsAbs(root) {
		return "目录配置需要设置本地绝对保存路径"
	}
	if _, err := cloudBrowserAliasParts(config.DirectoryName); err != nil {
		return err.Error()
	}
	for _, r := range config.ContentPrefix {
		if unicode.IsControl(r) {
			return "STRM 内容前缀含无效控制字符"
		}
	}
	include, err := cloudBrowserExtensions(config.IncludeExtensions)
	if err != nil {
		return err.Error()
	}
	exclude, err := cloudBrowserExtensions(config.ExcludeExtensions)
	if err != nil {
		return err.Error()
	}
	if config.ExcludeSmallerThanMB > math.MaxInt64/(1024*1024) {
		return "目录配置的最小文件大小无效"
	}
	if !item.IsFile {
		return ""
	}
	if !cloudSTRMFileComponent(item.Name) || item.Size < 0 {
		return "文件名称或大小无效"
	}
	if !item.Complete {
		return "文件尚未上传完成"
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(item.Name), "."))
	if ext == "strm" || cloudBrowserSubtitle(item.Name) {
		return "字幕及已有 STRM 文件不生成 STRM"
	}
	if (len(include) > 0 && !include[ext]) || exclude[ext] {
		return "文件被目录配置的后缀规则排除"
	}
	if config.ExcludeSmallerThanMB > 0 && item.Size < int64(config.ExcludeSmallerThanMB)*1024*1024 {
		return "文件小于目录配置的最小大小"
	}
	return ""
}

// The recursive job uses the exact same path calculation before writing to
// detect two different cloud files (for example Film.mkv / Film.mp4) that would
// otherwise overwrite the same STRM during one operation.
func cloudBrowserSTRMTarget(configs []model.MediaDirectory, ancestry []CloudBrowserBreadcrumb, item CloudBrowserEntry) (string, error) {
	if !item.IsFile || len(ancestry) == 0 || item.ParentID != ancestry[len(ancestry)-1].ID {
		return "", errors.New("文件不属于已确认的目录")
	}
	config, depth, err := cloudBrowserMatchDirectory(configs, ancestry)
	if err != nil {
		return "", err
	}
	if reason := cloudBrowserSTRMUnavailable(config, item); reason != "" {
		return "", errors.New(reason)
	}
	_, root, relative, err := cloudBrowserSTRMPaths(config, depth, ancestry, item)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, relative), nil
}

func cloudBrowserSTRMPaths(config *model.MediaDirectory, depth int, ancestry []CloudBrowserBreadcrumb, item CloudBrowserEntry) ([]string, string, string, error) {
	parts, err := cloudBrowserAliasParts(config.DirectoryName)
	if err != nil {
		return nil, "", "", err
	}
	for _, crumb := range ancestry[depth+1:] {
		parts = append(parts, crumb.Name)
	}
	parts = append(parts, item.Name)
	cloudRelative := strings.Join(parts, "/")
	localRelative := filepath.FromSlash(strings.TrimSuffix(cloudRelative, path.Ext(item.Name)) + ".strm")
	if !filepath.IsLocal(localRelative) {
		return nil, "", "", pathhelper.ErrUnsafePath
	}
	return parts, filepath.Clean(strings.TrimSpace(config.SavePath)), localRelative, nil
}

// writeSTRM is deliberately local-only. The enclosing job holds the organize
// storage lock; invoking StrmService here could perform hidden 115 lookups.
func (s *CloudBrowserService) writeSTRM(ctx context.Context, configs []model.MediaDirectory, ancestry []CloudBrowserBreadcrumb, item CloudBrowserEntry) CloudBrowserSTRMItemResult {
	result := CloudBrowserSTRMItemResult{FileID: item.FileID, Name: item.Name, Status: "failed"}
	fail := func(message string) CloudBrowserSTRMItemResult { result.Message = message; return result }
	if err := ctx.Err(); err != nil {
		return fail("STRM 生成已取消或超时")
	}
	if !item.IsFile || len(ancestry) == 0 || item.ParentID != ancestry[len(ancestry)-1].ID {
		return fail("文件不属于已确认的目录，已停止生成 STRM")
	}
	config, depth, err := cloudBrowserMatchDirectory(configs, ancestry)
	if err != nil {
		return fail(err.Error())
	}
	if reason := cloudBrowserSTRMUnavailable(config, item); reason != "" {
		result.Status = "skipped"
		return fail(reason)
	}
	result.DirectoryID = config.ID
	parts, root, localRelative, err := cloudBrowserSTRMPaths(config, depth, ancestry, item)
	if err != nil {
		return fail("文件无法安全映射到本地 STRM 路径")
	}
	result.LocalPath = filepath.Join(root, localRelative)
	if s.db == nil {
		return fail("无法确认本地 STRM 的管理状态")
	}
	var managed int64
	if err := s.db.WithContext(ctx).Model(&model.OrganizeManagedStrm{}).Where("path = ?", result.LocalPath).Count(&managed).Error; err != nil {
		return fail("无法确认本地 STRM 的管理状态")
	}
	if managed > 0 {
		result.Status = "skipped"
		return fail("本地 STRM 由整理任务管理，已保留原文件")
	}
	nextPath := "/" + strings.Join(parts, "/")
	if config.ContentEncodeURI {
		encoded := make([]string, len(parts))
		for i, part := range parts {
			encoded[i] = url.PathEscape(part)
		}
		nextPath = "/" + strings.Join(encoded, "/")
	}
	content := pathhelper.SafeFilePathJoin(strings.TrimSpace(config.ContentPrefix), nextPath)
	status, err := writeCloudBrowserSTRM(ctx, root, localRelative, content)
	if err != nil {
		if ctx.Err() != nil {
			return fail("STRM 生成已取消或超时")
		}
		return fail("本地 STRM 写入失败，请检查目录权限、符号链接或文件冲突")
	}
	result.Status = status
	switch status {
	case "created":
		result.Message = "STRM 已生成"
	case "updated":
		result.Message = "STRM 已重新生成"
	default:
		result.Message = "STRM 内容一致，无需更新"
	}
	return result
}

func cloudBrowserSTRMParents(root *os.Root, relative string) error {
	if !filepath.IsLocal(relative) {
		return pathhelper.ErrUnsafePath
	}
	parent := "."
	for _, component := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		parent = filepath.Join(parent, component)
		info, err := root.Lstat(parent)
		if os.IsNotExist(err) {
			if err := root.Mkdir(parent, 0755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = root.Lstat(parent)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return pathhelper.ErrUnsafePath
		}
	}
	return nil
}

func cloudBrowserExistingSTRM(root *os.Root, relative, content string) (bool, bool, error) {
	info, err := root.Lstat(relative)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() {
		return true, false, pathhelper.ErrUnsafePath
	}
	file, err := root.Open(relative)
	if err != nil {
		return true, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return true, false, pathhelper.ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	return true, bytes.Equal(data, []byte(content)), err
}

// Regeneration replaces an ordinary STRM only after a complete temporary file
// has been synced. os.Root confines both the temporary file and atomic rename.
func writeCloudBrowserSTRM(ctx context.Context, rootPath, relative, content string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(rootPath) || !filepath.IsLocal(relative) || !strings.EqualFold(filepath.Ext(relative), ".strm") {
		return "", pathhelper.ErrUnsafePath
	}
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return "", err
	}
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", pathhelper.ErrUnsafePath
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := cloudBrowserSTRMParents(root, relative); err != nil {
		return "", err
	}
	exists, same, err := cloudBrowserExistingSTRM(root, relative, content)
	if err != nil {
		return "", err
	}
	if same {
		return "unchanged", nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".filmfusion-browser-strm-"+hex.EncodeToString(nonce[:]))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	defer root.Remove(temporary)
	if _, err = io.WriteString(file, content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := cloudBrowserSTRMParents(root, relative); err != nil {
		return "", err
	}
	// Reject a newly introduced symlink/non-file before publication too.
	if _, _, err := cloudBrowserExistingSTRM(root, relative, content); err != nil {
		return "", err
	}
	if err := root.Rename(temporary, relative); err != nil {
		return "", err
	}
	if exists {
		return "updated", nil
	}
	return "created", nil
}
