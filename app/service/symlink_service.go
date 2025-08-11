package service

import (
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"os"
	"path/filepath"
	"strings"
)

// SymlinkService 软链接处理服务
type SymlinkService struct {
	logger *logger.Logger
}

// NewSymlinkService 创建新的 SymlinkService
func NewSymlinkService(log *logger.Logger) *SymlinkService {
	return &SymlinkService{logger: log}
}

// CreateFile 针对单个文件创建软链接
func (s *SymlinkService) CreateFile(path string, cloudPath model.CloudPath) error {
	// 将 Windows 风格路径转换为 Linux 风格（如果配置了 IsWindowsPath）
	processPath := path
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	}

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过软链接处理", cloudPath.ID)
		return nil
	}

	// 命中过滤规则(include)，且不在 download 列表时才生成软链
	if cloudPath.FilterRules != "" {
		// include 未命中 -> 跳过
		if !pathhelper.IsFileMatchedByFilter(processPath, cloudPath.FilterRules, "include") {
			s.logger.Debugf("文件 %s 未命中 include 规则，跳过软链接", processPath)
			return nil
		}
		// 命中 download -> 跳过（不生成软链）
		if pathhelper.IsFileMatchedByFilter(processPath, cloudPath.FilterRules, "download") {
			s.logger.Debugf("文件 %s 命中 download 规则，跳过软链接", processPath)
			return nil
		}
	}

	linkPath := filepath.Join(cloudPath.LocalPath, processPath)
	targetPath := filepath.Join(cloudPath.ContentPrefix, processPath)

	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		s.logger.Errorf("创建目录失败: %v", err)
		return err
	}

	// 如果目标已存在：若为文件或链接则删除，目录则报错并跳过
	if fi, err := os.Lstat(linkPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			if rmErr := os.Remove(linkPath); rmErr != nil {
				s.logger.Errorf("删除已存在的目标失败: %s -> %v", linkPath, rmErr)
				return rmErr
			}
		} else {
			s.logger.Errorf("目标路径已存在且为目录，无法覆盖软链接: %s", linkPath)
			return nil
		}
	}

	// 规范化：避免 targetPath 或 linkPath 出现 \ 分隔符
	targetPath = strings.ReplaceAll(targetPath, "\\", "/")
	linkPath = strings.ReplaceAll(linkPath, "\\", "/")

	if err := os.Symlink(targetPath, linkPath); err != nil {
		s.logger.Errorf("创建软链接失败: %s -> %s, 错误: %v", linkPath, targetPath, err)
		return err
	}

	s.logger.Debugf("创建软链接: %s -> %s", linkPath, targetPath)
	return nil
}

// RenameFile 文件重命名时同步更新软链接
func (s *SymlinkService) RenameFile(originalPath, path string, cloudPath model.CloudPath) {
	// 1. 为新路径创建软链接
	if err := s.CreateFile(path, cloudPath); err != nil {
		s.logger.Errorf("为重命名文件创建新软链接失败: %s -> %v", path, err)
	}

	// 2. 删除原路径的软链接（如果在监控范围内）
	if pathhelper.IsSubPath(originalPath, cloudPath.SourcePath) {
		s.deleteFileLink(originalPath, cloudPath)
	}
}

// RenameDir 目录重命名时批量更新软链接
func (s *SymlinkService) RenameDir(originalPath, path string, cloudPath model.CloudPath) {
	s.logger.Debugf("目录重命名软链接处理: %s -> %s", originalPath, path)

	// 目录重命名比较复杂，需要遍历目录下所有文件重新创建软链接
	// 这里先删除原目录对应的所有软链接
	if pathhelper.IsSubPath(originalPath, cloudPath.SourcePath) {
		s.deleteDirectoryLinks(originalPath, cloudPath)
	}

	// 新目录下的文件软链接将通过后续的文件创建事件来处理
	// 或者可以选择主动遍历新目录创建软链接（但可能与后续事件重复）
}

// DeleteLink 删除对应的软链接或目录
func (s *SymlinkService) DeleteLink(path string, cloudPath model.CloudPath, isDir bool) {
	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过软链接删除", cloudPath.ID)
		return
	}

	// 判断是否在监控路径范围内
	if !pathhelper.IsSubPath(path, cloudPath.SourcePath) {
		s.logger.Debugf("路径 %s 不在监控范围内，跳过删除", path)
		return
	}

	if isDir {
		s.deleteDirectoryLinks(path, cloudPath)
	} else {
		s.deleteFileLink(path, cloudPath)
	}
}

// deleteFileLink 删除单个文件对应的软链接
func (s *SymlinkService) deleteFileLink(path string, cloudPath model.CloudPath) {
	processPath := path
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	}

	linkPath := filepath.Join(cloudPath.LocalPath, processPath)
	linkPath = strings.ReplaceAll(linkPath, "\\", "/")

	// 检查是否存在且为软链接
	if fi, err := os.Lstat(linkPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if rmErr := os.Remove(linkPath); rmErr != nil {
				s.logger.Errorf("删除软链接失败: %s -> %v", linkPath, rmErr)
			} else {
				s.logger.Debugf("删除软链接: %s", linkPath)
			}
		} else {
			s.logger.Debugf("目标不是软链接，跳过删除: %s", linkPath)
		}
	}
}

// deleteDirectoryLinks 删除目录及其下所有软链接
func (s *SymlinkService) deleteDirectoryLinks(path string, cloudPath model.CloudPath) {
	processPath := path
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	}

	linkDirPath := filepath.Join(cloudPath.LocalPath, processPath)
	linkDirPath = strings.ReplaceAll(linkDirPath, "\\", "/")

	// 递归删除目录下的所有内容（但只删除软链接，保留其他文件）
	err := filepath.Walk(linkDirPath, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 忽略错误，继续处理其他文件
		}

		// 如果是软链接，则删除
		if info.Mode()&os.ModeSymlink != 0 {
			if rmErr := os.Remove(walkPath); rmErr != nil {
				s.logger.Errorf("删除软链接失败: %s -> %v", walkPath, rmErr)
			} else {
				s.logger.Debugf("删除软链接: %s", walkPath)
			}
		}
		return nil
	})

	if err != nil {
		s.logger.Errorf("遍历目录删除软链接时出错: %s -> %v", linkDirPath, err)
	}

	// 尝试删除空目录（如果目录下只有软链接的话）
	if rmErr := os.Remove(linkDirPath); rmErr == nil {
		s.logger.Debugf("删除空目录: %s", linkDirPath)
	}
}

// WalkDirAndCreateLinks 批量遍历目录创建软链接（类似 STRM 的批量生成）
func (s *SymlinkService) WalkDirAndCreateLinks(dirPath string, cloudPath model.CloudPath) error {
	processPath := dirPath
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(dirPath)
	}

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过批量软链接创建", cloudPath.ID)
		return nil
	}

	// 构建源目录路径（ContentPrefix + 路径）
	sourceDir := filepath.Join(cloudPath.ContentPrefix, processPath)
	sourceDir = strings.ReplaceAll(sourceDir, "\\", "/")

	s.logger.Infof("开始批量创建软链接，源目录: %s", sourceDir)

	// 统计信息
	var createdCount, skippedCount, errorCount int

	// 递归遍历源目录
	err := filepath.Walk(sourceDir, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			s.logger.Errorf("遍历目录时出错: %s -> %v", walkPath, err)
			errorCount++
			return nil // 继续处理其他文件
		}

		// 跳过目录，只处理文件
		if info.IsDir() {
			return nil
		}

		// 计算相对路径
		relPath, err := filepath.Rel(cloudPath.ContentPrefix, walkPath)
		if err != nil {
			s.logger.Errorf("计算相对路径失败: %s -> %v", walkPath, err)
			errorCount++
			return nil
		}

		// 统一路径分隔符
		relPath = strings.ReplaceAll(relPath, "\\", "/")

		// 检查过滤规则
		if cloudPath.FilterRules != "" {
			if !pathhelper.IsFileMatchedByFilter(relPath, cloudPath.FilterRules, "include") {
				skippedCount++
				return nil
			}
			if pathhelper.IsFileMatchedByFilter(relPath, cloudPath.FilterRules, "download") {
				skippedCount++
				return nil
			}
		}

		// 创建软链接
		if createErr := s.CreateFile(relPath, cloudPath); createErr != nil {
			errorCount++
		} else {
			createdCount++
		}

		return nil
	})

	if err != nil {
		s.logger.Errorf("批量创建软链接失败: %v", err)
		return err
	}

	s.logger.Infof("批量创建软链接完成，创建: %d, 跳过: %d, 错误: %d", createdCount, skippedCount, errorCount)
	return nil
}

// CheckAndRepairLinks 检查并修复损坏的软链接
func (s *SymlinkService) CheckAndRepairLinks(cloudPath model.CloudPath) error {
	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过软链接检查", cloudPath.ID)
		return nil
	}

	localDir := cloudPath.LocalPath
	s.logger.Infof("开始检查软链接健康状态，本地目录: %s", localDir)

	var totalCount, brokenCount, repairedCount, removedCount int

	err := filepath.Walk(localDir, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 忽略访问错误
		}

		// 只处理软链接
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}

		totalCount++

		// 读取软链接目标
		target, err := os.Readlink(walkPath)
		if err != nil {
			s.logger.Errorf("读取软链接目标失败: %s -> %v", walkPath, err)
			brokenCount++
			return nil
		}

		// 检查目标是否存在
		if _, err := os.Stat(target); os.IsNotExist(err) {
			brokenCount++
			s.logger.Debugf("发现损坏的软链接: %s -> %s", walkPath, target)

			// 尝试修复：重新创建软链接
			relPath, err := filepath.Rel(cloudPath.LocalPath, walkPath)
			if err != nil {
				s.logger.Errorf("计算相对路径失败: %s -> %v", walkPath, err)
				return nil
			}

			relPath = strings.ReplaceAll(relPath, "\\", "/")

			// 删除损坏的链接
			if rmErr := os.Remove(walkPath); rmErr != nil {
				s.logger.Errorf("删除损坏的软链接失败: %s -> %v", walkPath, rmErr)
				return nil
			}
			removedCount++

			// 尝试重新创建
			if repairErr := s.CreateFile(relPath, cloudPath); repairErr != nil {
				s.logger.Errorf("修复软链接失败: %s -> %v", relPath, repairErr)
			} else {
				repairedCount++
				s.logger.Debugf("成功修复软链接: %s", walkPath)
			}
		}

		return nil
	})

	if err != nil {
		s.logger.Errorf("检查软链接时出错: %v", err)
		return err
	}

	s.logger.Infof("软链接健康检查完成，总计: %d, 损坏: %d, 已删除: %d, 已修复: %d",
		totalCount, brokenCount, removedCount, repairedCount)
	return nil
}

// ValidateContentPrefix 验证 ContentPrefix 是否为有效的挂载点
func (s *SymlinkService) ValidateContentPrefix(contentPrefix string) error {
	if contentPrefix == "" {
		return nil // 空路径跳过验证
	}

	// 检查路径是否存在
	if _, err := os.Stat(contentPrefix); os.IsNotExist(err) {
		return err
	}

	// 检查路径是否可访问
	testFile := filepath.Join(contentPrefix, ".filmfusion_test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return err
	}

	// 清理测试文件
	os.Remove(testFile)

	s.logger.Debugf("ContentPrefix 验证通过: %s", contentPrefix)
	return nil
}
