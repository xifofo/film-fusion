package handler

// 文件删除以当前授权根目录为边界；快照更新采用新切片，避免并发状态响应读到正在修改的数据。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"film-fusion/app/model"
)

func removeScannedVersionFile(file EmbyVersionFile, mapping model.MediaDirectory) (string, error) {
	if !mapping.MappingEnabled || mapping.ID != file.CloudPathID || strings.TrimSpace(mapping.SavePath) == "" {
		return "failed", errors.New("媒体目录映射已失效，请重新检查")
	}
	rootPath, err := filepath.Abs(strings.TrimSpace(mapping.SavePath))
	if err != nil {
		return "failed", errors.New("本地根目录无效")
	}
	scannedRoot, err := filepath.Abs(file.LocalRoot)
	if err != nil || rootPath != scannedRoot {
		return "failed", errors.New("媒体目录路径已改变，请重新检查")
	}
	absPath, err := filepath.Abs(file.Path)
	if err != nil {
		return "failed", errors.New("文件路径无效")
	}
	rel, err := filepath.Rel(rootPath, absPath)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "failed", errors.New("文件不在当前媒体目录内")
	}
	if _, supported := embyVersionMediaExts[strings.ToLower(filepath.Ext(rel))]; !supported {
		return "failed", errors.New("只允许删除扫描结果中的媒体或 STRM 文件")
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "failed", errors.New("本地根目录不可访问，请检查挂载状态")
	}
	defer root.Close()

	// 显式拒绝符号链接路径。Root 进一步阻止父目录在检查过程中被换成指向根目录外的链接。
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		info, statErr := root.Lstat(filepath.Join(parts[:i+1]...))
		if statErr != nil {
			if os.IsNotExist(statErr) && i == len(parts)-1 {
				return "missing", nil
			}
			return "failed", errors.New("文件所在目录不可访问或已改变，请重新检查")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "failed", errors.New("不允许通过符号链接删除文件，请重新检查实际目录")
		}
	}

	// 固定父目录句柄，删除只作用于该目录下的单个文件名；不递归删除目录和配套文件。
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return "failed", errors.New("无法访问文件所在目录")
	}
	defer parent.Close()
	name := filepath.Base(rel)
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "failed", errors.New("目标不是普通文件或无法读取")
	}
	if file.identity == nil || !os.SameFile(file.identity, info) || info.Size() != file.FileSize || !info.ModTime().Equal(file.ModifiedAt) {
		return "failed", errors.New("文件自扫描后已修改或替换，请重新检查后再删除")
	}
	if err := parent.Remove(name); err != nil {
		if os.IsNotExist(err) {
			return "missing", nil
		}
		return "failed", fmt.Errorf("删除失败，请检查文件权限和挂载状态: %w", err)
	}
	return "deleted", nil
}

// applyVersionDeletes 从最新扫描快照移除已删除/确认不存在的文件，并重新计算重复项。
func (h *EmbyVersionCheckHandler) applyVersionDeletes(owner uint, jobID string, removed map[string]bool) {
	if len(removed) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	job := h.jobs[owner]
	if job == nil || job.ID != jobID || job.Result == nil {
		return
	}

	result := *job.Result
	result.Items = nil
	result.ScannedPaths = append([]EmbyVersionScannedPath(nil), job.Result.ScannedPaths...)
	result.DuplicateMovieCount, result.DuplicateEpisodeCount = 0, 0
	removedByMapping := make(map[uint]int)
	counted := make(map[string]bool)
	for _, original := range job.Result.Items {
		item := original
		item.Files = nil
		for _, file := range original.Files {
			if removed[file.Path] {
				if !counted[file.Path] {
					removedByMapping[file.CloudPathID]++
					counted[file.Path] = true
				}
			} else {
				item.Files = append(item.Files, file)
			}
		}
		if len(item.Files) == 0 {
			if item.MediaType == "movie" {
				result.MovieGroupCount--
			} else {
				result.EpisodeGroupCount--
			}
		}
		if len(item.Files) < 2 {
			continue
		}
		item.VersionCount = len(item.Files)
		item.VersionLabels = collectEmbyVersionLabels(item.Files)
		item.CloudPathIDs = collectEmbyVersionCloudPathIDs(item.Files)
		result.Items = append(result.Items, item)
		if item.MediaType == "movie" {
			result.DuplicateMovieCount++
		} else {
			result.DuplicateEpisodeCount++
		}
	}
	result.TotalFiles -= len(counted)
	result.DuplicateItemCount = len(result.Items)
	for i := range result.ScannedPaths {
		result.ScannedPaths[i].FileCount -= removedByMapping[result.ScannedPaths[i].CloudPathID]
	}
	job.Result = &result
}
