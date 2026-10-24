// 历史修复只处理计划记录的 STRM 与旧版清单；所有删除均限制在原根目录内。
package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"film-fusion/app/model"
)

func plutoFishLegacyPath(scope, transferID, filename string) string {
	return filepath.Join("PlutoFish", fmt.Sprintf("%x", sha256.Sum256([]byte(scope))), fmt.Sprintf("%x", sha256.Sum256([]byte(transferID))), filename)
}

func plutoFishRepairManifest(repair model.PlutoFishSTRMRepair) (string, string) {
	data, _ := json.Marshal(struct {
		FileName string `json:"fileName"`
		Content  string `json:"content"`
	}{filepath.Base(repair.OldRelative), repair.OldContent})
	return filepath.Join(filepath.Dir(repair.OldRelative), ".transfer.json"), string(data)
}

// 除 os.Root 的越界保护，还拒绝根目录内的符号链接别名，避免清理另一目录。
func plutoFishRepairParents(root *os.Root, relative string) error {
	if !filepath.IsLocal(relative) {
		return errors.New("文件路径不安全")
	}
	parent := "."
	for _, part := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		parent = filepath.Join(parent, part)
		info, err := root.Lstat(parent)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("文件父目录不可访问或含符号链接")
		}
	}
	return nil
}

// preview 使用相同的只读校验；缺失文件可从已保存通知重建，内容冲突不能覆盖。
func plutoFishRepairFile(rootPath, relative, content string) (bool, error) {
	root, err := os.OpenRoot(rootPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("无法访问文件根目录")
	}
	defer root.Close()
	if err := plutoFishRepairParents(root, relative); err != nil {
		return false, err
	}
	exists, err := plutoFishExistingContent(root, relative, content)
	if err != nil {
		return exists, errors.New("文件内容冲突、不是普通文件或无法读取")
	}
	return exists, nil
}

func plutoFishRepairFiles(repair model.PlutoFishSTRMRepair) (string, error) {
	oldExists, err := plutoFishRepairFile(repair.OldRoot, repair.OldRelative, repair.OldContent)
	if err != nil {
		return "", fmt.Errorf("旧 STRM：%w", err)
	}
	manifest, content := plutoFishRepairManifest(repair)
	manifestExists, err := plutoFishRepairFile(repair.OldRoot, manifest, content)
	if err != nil {
		return "", fmt.Errorf("旧 .transfer.json：%w", err)
	}
	newExists, err := plutoFishRepairFile(repair.NewRoot, repair.NewRelative, repair.NewContent)
	if err != nil {
		return "", fmt.Errorf("目标 STRM：%w", err)
	}
	return fmt.Sprintf("%t/%t/%t", oldExists, manifestExists, newExists), nil
}

func plutoFishCleanupRepair(repair model.PlutoFishSTRMRepair) error {
	// 数据库路径已切换后仍再次确认新文件存在，才允许删除原文件。
	exists, err := plutoFishRepairFile(repair.NewRoot, repair.NewRelative, repair.NewContent)
	if err != nil || !exists {
		return errors.New("新 STRM 未通过校验，保留旧文件")
	}
	root, err := os.OpenRoot(repair.OldRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("无法访问旧目录，保留待清理状态")
	}
	defer root.Close()
	manifest, content := plutoFishRepairManifest(repair)
	for _, file := range []struct{ path, content string }{{repair.OldRelative, repair.OldContent}, {manifest, content}} {
		if err := plutoFishRepairParents(root, file.path); err != nil {
			return err
		}
		exists, err := plutoFishExistingContent(root, file.path, file.content)
		if err != nil {
			return errors.New("旧文件在修复期间发生变化，已停止清理")
		}
		if exists {
			if err := root.Remove(file.path); err != nil && !os.IsNotExist(err) {
				return errors.New("旧文件清理失败，可重新预览后继续")
			}
			if err := plutoFishSyncDirectories(root, filepath.Dir(file.path)); err != nil {
				return errors.New("旧文件删除尚未同步到磁盘，可继续清理")
			}
		}
	}

	// 只逐层删除旧任务、用户哈希和 PlutoFish 目录；Remove 不会删除非空目录。
	for dir := filepath.Dir(repair.OldRelative); dir != "."; dir = filepath.Dir(dir) {
		if err := plutoFishRepairParents(root, filepath.Join(dir, "placeholder")); err != nil {
			return err
		}
		err := root.Remove(dir)
		if os.IsNotExist(err) {
			continue
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			break
		}
		if err != nil {
			return errors.New("空哈希目录清理失败，可重新预览后继续")
		}
		if err := plutoFishSyncDirectories(root, filepath.Dir(dir)); err != nil {
			return errors.New("空目录删除尚未同步到磁盘，可继续清理")
		}
	}
	return nil
}
