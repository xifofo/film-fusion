package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

type CloudFolderMoveStrmResult struct {
	Status      string `json:"status"`
	CloudPathID uint   `json:"cloud_path_id,omitempty"`
	LocalPath   string `json:"local_path,omitempty"`
	Message     string `json:"message"`
}

// createMovedFileSTRM runs only after cloud membership has been verified. Its
// outcome never changes a successful move into a failed/ambiguous cloud move.
func (s *CloudFolderMoveService) createMovedFileSTRM(ctx context.Context, provider cloudFolderProvider, userID, storageID uint, moved *CloudFolderMoveResult) *CloudFolderMoveStrmResult {
	result := &CloudFolderMoveStrmResult{Status: "failed"}
	fail := func(message string) *CloudFolderMoveStrmResult { result.Message = message; return result }
	if ctx.Err() != nil {
		return fail("文件已移动，STRM 生成已取消或超时，可重试生成")
	}
	if !cloudSTRMFileComponent(moved.FileName) {
		return fail("文件已移动，但文件名无法安全用于本地 STRM 路径")
	}
	var mappings []model.CloudPath
	if s.db == nil || s.db.WithContext(ctx).Where("user_id = ? AND cloud_storage_id = ? AND link_type = ?", userID, storageID, model.LinkTypeStrm).Find(&mappings).Error != nil {
		return fail("文件已移动，但读取云路径映射失败，可重试生成 STRM")
	}
	if len(mappings) == 0 {
		result.Status, result.Message = "skipped", "当前账号未配置 STRM 云路径映射"
		return result
	}
	directory, err := provider.ResolveDirectoryPath(ctx, moved.FolderID)
	if err != nil {
		return fail("文件已移动，但无法确认目标文件夹的云端路径，可重试生成 STRM")
	}
	if !strings.HasPrefix(directory, "/") || strings.ContainsRune(directory, '\\') || !utf8.ValidString(directory) {
		return fail("目标文件夹的云端路径无效，已停止生成 STRM")
	}
	for _, char := range directory {
		if unicode.IsControl(char) {
			return fail("目标文件夹的云端路径无效，已停止生成 STRM")
		}
	}
	directory, err = pathhelper.NormalizeUntrustedPath(directory)
	if err != nil {
		return fail("目标文件夹的云端路径无效，已停止生成 STRM")
	}
	cloudPath := path.Join(directory, moved.FileName)
	mapping, eventPath, err := cloudMovedSTRMMapping(mappings, cloudPath)
	if err != nil {
		return fail("目标路径匹配多个同级云路径映射，请保留唯一映射后重试生成 STRM")
	}
	if mapping == nil {
		result.Status, result.Message = "skipped", "目标文件不在当前账号的 STRM 云路径映射范围内"
		return result
	}
	result.CloudPathID = mapping.ID
	allowed, filterErr := cloudMovedSTRMFilter(cloudPath, mapping.FilterRules)
	if filterErr != nil {
		return fail("云路径映射的过滤规则无效，请修正后重试生成 STRM")
	}
	if !allowed {
		result.Status, result.Message = "skipped", "文件被映射过滤规则排除或属于下载类扩展名，未生成 STRM"
		return result
	}
	root := strings.TrimSpace(mapping.LocalPath)
	if root == "" || !filepath.IsAbs(root) {
		return fail("云路径映射未配置有效的本地绝对路径")
	}
	localPath, err := pathhelper.JoinUnderRoot(root, eventPath)
	if err != nil {
		return fail("目标文件无法安全映射到本地 STRM 路径")
	}
	localPath = strings.TrimSuffix(localPath, filepath.Ext(localPath)) + ".strm"
	result.LocalPath = localPath
	relative, err := filepath.Rel(filepath.Clean(root), localPath)
	if err != nil || !filepath.IsLocal(relative) {
		return fail("目标文件无法安全映射到本地 STRM 路径")
	}
	// This local-only guard deliberately avoids protectUpgradeEntry, whose
	// recovery checks can create an extra Cookie client and query 115.
	if s.db.Migrator().HasTable(&model.OrganizeManagedStrm{}) {
		var count int64
		if s.db.WithContext(ctx).Model(&model.OrganizeManagedStrm{}).Where("path = ?", localPath).Count(&count).Error != nil {
			return fail("无法确认本地 STRM 的管理状态，请稍后重试")
		}
		if count > 0 {
			return fail("本地 STRM 已由整理任务管理，已保留原文件")
		}
	}
	nextPath := eventPath
	if mapping.ContentEncodeURI {
		parts := strings.Split(nextPath, "/")
		for i, part := range parts {
			parts[i] = url.PathEscape(part)
		}
		nextPath = strings.Join(parts, "/")
	}
	content := pathhelper.SafeFilePathJoin(mapping.ContentPrefix, nextPath)
	if err := writeCloudMovedSTRM(ctx, root, relative, content); err != nil {
		if errors.Is(err, errCloudSTRMContentConflict) {
			return fail("本地 STRM 已存在且内容不同，已保留原文件")
		}
		return fail("文件已移动，但本地 STRM 写入失败，请检查映射目录权限后重试")
	}
	result.Status, result.Message = "created", "本地 STRM 已生成或已存在相同内容"
	return result
}

func cloudSTRMFileComponent(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

// The mapping is selected in the real 115 path space. CD2's first source
// component is only a mount alias; restore it for the existing local/content
// path convention used by organize.upgradeBindings and StrmService.
func cloudMovedSTRMMapping(mappings []model.CloudPath, cloudPath string) (*model.CloudPath, string, error) {
	var selected *model.CloudPath
	selectedEvent, bestLength, ambiguous := "", -1, false
	for i := range mappings {
		mapping := &mappings[i]
		source, err := pathhelper.NormalizeUntrustedPath(mapping.SourcePath)
		if err != nil {
			continue
		}
		actualSource, eventPath := source, cloudPath
		switch mapping.SourceType {
		case model.SourceTypeCloudDrive2:
			if source == "/" {
				continue
			}
			mount := strings.Split(strings.TrimPrefix(source, "/"), "/")[0]
			actualSource = pathhelper.EnsureLeadingSlash(pathhelper.RemoveFirstDir(source))
			eventPath = path.Join("/", mount, cloudPath)
		case model.SourceTypeMoviePilot2:
		default:
			continue
		}
		if !pathhelper.IsSubPath(cloudPath, actualSource) {
			continue
		}
		if len(actualSource) > bestLength {
			selected, selectedEvent, bestLength, ambiguous = mapping, eventPath, len(actualSource), false
		} else if len(actualSource) == bestLength {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, "", errors.New("ambiguous cloud path mapping")
	}
	return selected, selectedEvent, nil
}

func cloudMovedSTRMFilter(cloudPath, filterJSON string) (bool, error) {
	filterJSON = strings.TrimSpace(filterJSON)
	var rules model.FilterRule
	if strings.TrimSpace(filterJSON) != "" && json.Unmarshal([]byte(filterJSON), &rules) != nil {
		return false, errors.New("invalid filter rules")
	}
	// An empty download list means no explicit download extension. Reusing the
	// generic matcher alone would treat an empty list as "all files".
	if len(rules.Download) > 0 && pathhelper.IsFileMatchedByFilter(cloudPath, filterJSON, "download") {
		return false, nil
	}
	return pathhelper.IsFileMatchedByFilter(cloudPath, filterJSON, "include"), nil
}

var errCloudSTRMContentConflict = errors.New("existing STRM content differs")

// Publish a complete local file without overwriting an existing entry. The
// temporary file and final link both remain inside os.Root, including when a
// directory contains a symlink. An existing matching file is an idempotent success.
func writeCloudMovedSTRM(ctx context.Context, rootPath, relative, content string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !filepath.IsLocal(relative) {
		return pathhelper.ErrUnsafePath
	}
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if exists, err := cloudSTRMExistingContent(root, relative, content); exists || err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".filmfusion-strm-"+hex.EncodeToString(nonce[:]))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = io.WriteString(file, content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := root.Link(temporary, relative); err != nil {
		if os.IsExist(err) {
			exists, existingErr := cloudSTRMExistingContent(root, relative, content)
			if exists || existingErr != nil {
				return existingErr
			}
		}
		return err
	}
	return nil
}

func cloudSTRMExistingContent(root *os.Root, relative, content string) (bool, error) {
	file, err := root.Open(relative)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return true, err
	}
	if !info.Mode().IsRegular() {
		return true, errCloudSTRMContentConflict
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	if err != nil {
		return true, err
	}
	if !bytes.Equal(data, []byte(content)) {
		return true, errCloudSTRMContentConflict
	}
	return true, nil
}
