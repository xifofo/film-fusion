package organize

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func (h *Service) batchRenameAndMove(webClient *driver.Pan115Client, items []Organize115ItemResult) error {
	renameMap := make(map[string]string)
	for _, item := range items {
		if strings.TrimSpace(item.FileID) == "" {
			continue
		}
		newName := strings.TrimSpace(item.RenameTo)
		if newName == "" {
			continue
		}
		if newName == strings.TrimSpace(item.FileName) {
			continue
		}
		renameMap[item.FileID] = newName
	}

	if err := h.web115Svc.BatchRename(webClient, renameMap); err != nil {
		return fmt.Errorf("批量重命名失败: %w", err)
	}

	moveGroups := make(map[string][]string)
	for _, item := range items {
		dirID := strings.TrimSpace(item.TargetDirID)
		if dirID == "" {
			return fmt.Errorf("目标目录ID为空，无法移动文件: %s", item.FileName)
		}
		if strings.TrimSpace(item.FileID) == "" {
			return fmt.Errorf("文件ID为空，无法移动文件: %s", item.FileName)
		}
		moveGroups[dirID] = append(moveGroups[dirID], item.FileID)
	}

	for dirID, fileIDs := range moveGroups {
		if err := h.web115Svc.MoveFiles(webClient, dirID, fileIDs); err != nil {
			return fmt.Errorf("移动文件失败(目录ID=%s): %w", dirID, err)
		}
	}

	return nil
}

func (h *Service) generateStrmFiles(dir model.CloudDirectory, items *[]Organize115ItemResult, dryRun bool) error {
	if items == nil || len(*items) == 0 {
		return nil
	}

	savePath := strings.TrimSpace(dir.SavePath)
	if savePath == "" {
		return fmt.Errorf("保存路径为空，无法生成 STRM 文件")
	}

	contentPrefix := strings.TrimSpace(dir.ContentPrefix)
	encodeURI := dir.ContentEncodeURI

	for i := range *items {
		item := &(*items)[i]
		if strings.TrimSpace(item.TargetPath) == "" {
			continue
		}
		name := item.RenameTo
		if strings.TrimSpace(name) == "" {
			name = item.FileName
		}
		if item.IsSubtitle || isSubtitleFile(name) {
			continue
		}
		strmPath, content := buildStrmInfo(savePath, contentPrefix, item.TargetPath, encodeURI)
		item.StrmPath = strmPath
		item.StrmContent = content

		if dryRun {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(strmPath), 0755); err != nil {
			return fmt.Errorf("创建 STRM 目录失败: %w", err)
		}

		if err := os.WriteFile(strmPath, []byte(content), 0777); err != nil {
			return fmt.Errorf("写入 STRM 文件失败: %w", err)
		}
	}

	return nil
}

func buildStrmInfo(savePath, contentPrefix, targetPath string, encodeURI bool) (string, string) {
	localPath := pathhelper.SafeFilePathJoin(savePath, targetPath)
	ext := filepath.Ext(localPath)
	strmPath := localPath
	if ext != "" {
		strmPath = localPath[:len(localPath)-len(ext)] + ".strm"
	} else {
		strmPath = localPath + ".strm"
	}

	nextPath := targetPath
	if encodeURI {
		parts := strings.Split(nextPath, "/")
		for i, part := range parts {
			parts[i] = url.PathEscape(part)
		}
		nextPath = strings.Join(parts, "/")
	}

	content := pathhelper.SafeFilePathJoin(contentPrefix, nextPath)
	return strmPath, content
}

func (h *Service) cachePickcodeCaches(dir model.CloudDirectory, items []Organize115ItemResult) {
	if len(items) == 0 {
		return
	}
	// 缓存 key 与 STRM 内容空间对齐：ContentPrefix + TargetPath
	// 与 Emby 播放代理(getDownloadURL)侧使用的 cacheKey 同构，保证命中
	contentPrefix := strings.TrimSpace(dir.ContentPrefix)
	created := 0
	for _, item := range items {
		if isOrganizeSubtitleItem(item) {
			continue
		}
		targetPath := strings.TrimSpace(item.TargetPath)
		if targetPath == "" || strings.TrimSpace(item.PickCode) == "" {
			continue
		}
		filePath := pathhelper.SafeFilePathJoin(contentPrefix, targetPath)
		_, isCreated, err := model.UpsertPickcodeCache(h.store(), filePath, item.PickCode)
		if err != nil {
			h.logger.Warnf("缓存 pickcode 失败: %s, err=%v", filePath, err)
			continue
		}
		if isCreated {
			created++
		}
	}
	if created > 0 {
		h.logger.Infof("缓存 pickcode 完成: 新增 %d 条", created)
	}
}
