package service

import (
	"context"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

// SymlinkService 软链接处理服务
type SymlinkService struct {
	logger     *logger.Logger
	sdk115Open *sdk115.Client
}

// NewSymlinkService 创建新的 SymlinkService
func NewSymlinkService(log *logger.Logger) *SymlinkService {
	return &SymlinkService{
		logger:     log,
		sdk115Open: sdk115.New(),
	}
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

	// 规范化路径：根据运行环境使用正确的路径分隔符
	targetPath = s.normalizePath(targetPath)
	linkPath = s.normalizePath(linkPath)

	if err := os.Symlink(targetPath, linkPath); err != nil {
		// 在 Windows 系统上提供更详细的错误信息
		if runtime.GOOS == "windows" {
			s.logger.Errorf("创建软链接失败: %s -> %s, 错误: %v", linkPath, targetPath, err)
			s.logger.Errorf("提示：在 Windows 系统上创建软链接需要以下条件之一：")
			s.logger.Errorf("1. 以管理员权限运行程序")
			s.logger.Errorf("2. 启用 Windows 开发者模式")
			s.logger.Errorf("3. 设置本地策略允许创建符号链接")
		} else {
			s.logger.Errorf("创建软链接失败: %s -> %s, 错误: %v", linkPath, targetPath, err)
		}
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
func (s *SymlinkService) RenameDir(originalPath, path string, cloudPath model.CloudPath, isDeleteOriginal bool) {
	var processPath string
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	} else {
		processPath = path
	}

	if cloudPath.CloudStorage.StorageType == model.StorageType115Open && pathhelper.IsSubPath(processPath, cloudPath.SourcePath) {
		s.WalkDirWith115OpenAPI(processPath, cloudPath)
	}

	// 目录重命名比较复杂，需要遍历目录下所有文件重新创建软链接
	// 这里先删除原目录对应的所有软链接
	if pathhelper.IsSubPath(originalPath, cloudPath.SourcePath) && isDeleteOriginal {
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
	linkPath = s.normalizePath(linkPath)

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
	linkDirPath = s.normalizePath(linkDirPath)

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

// WalkDirWith115OpenAPI 使用115 Open API递归遍历目录创建软链接
// 该方法会：
// 1. 使用115 SDK获取指定目录下的所有文件和子目录
// 2. 对符合过滤规则的文件调用 pathhelper.IsFileMatchedByFilter() 进行过滤
// 3. 为通过过滤的文件创建软链接
// 4. 对子目录进行递归遍历
//
// 参数：
//   - dirPath: 要遍历的目录路径
//   - cloudPath: 云盘路径配置信息，包含过滤规则等
func (s *SymlinkService) WalkDirWith115OpenAPI(dirPath string, cloudPath model.CloudPath) error {
	// 设置访问令牌
	s.sdk115Open.SetAccessToken(cloudPath.CloudStorage.AccessToken)

	processPath := dirPath
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(dirPath)
	}

	sourceCloudPath := filepath.Join("/", processPath)
	// 转换路径为云盘路径
	if cloudPath.SourceType == model.SourceTypeCloudDrive2 {
		sourceCloudPath = filepath.Join("/", pathhelper.RemoveFirstDir(processPath))
	}

	// 获取目录信息
	folderInfo, err := s.sdk115Open.GetFolderInfoByPath(context.Background(), pathhelper.ConvertToLinuxPath(sourceCloudPath))
	if err != nil {
		s.logger.Errorf("获取115Open目录信息失败: %s, 错误: %v", sourceCloudPath, err)
		return err
	}

	s.logger.Infof("开始使用115Open API批量创建软链接，目录: %s (CID: %s)", sourceCloudPath, folderInfo.FileID)

	// 递归遍历目录
	var createdCount, skippedCount, errorCount int
	s.walkDir115(folderInfo.FileID, processPath, cloudPath, 0, &createdCount, &skippedCount, &errorCount)

	s.logger.Infof("批量创建软链接完成，创建: %d, 跳过: %d, 错误: %d", createdCount, skippedCount, errorCount)
	return nil
}

// walkDir115 递归遍历115目录的内部实现
// 该方法处理分页获取文件列表，并对每个文件/目录进行相应处理
//
// 参数：
//   - cid: 115目录的ID (从GetFolderInfoByPath获取的FileID)
//   - currentPath: 当前遍历的路径
//   - cloudPath: 云盘路径配置信息
//   - depth: 当前递归深度，用于防止无限递归
//   - createdCount, skippedCount, errorCount: 统计信息指针
func (s *SymlinkService) walkDir115(cid, currentPath string, cloudPath model.CloudPath, depth int, createdCount, skippedCount, errorCount *int) {
	// 防止无限递归
	maxDepth := 50
	if depth >= maxDepth {
		s.logger.Warnf("达到最大递归深度 %d，停止遍历: %s", maxDepth, currentPath)
		return
	}

	// 获取当前目录下的文件列表
	req := &sdk115.GetFilesReq{
		CID:     cid,
		ShowDir: true, // 显示目录
		Stdir:   1,    // 显示文件夹
		Limit:   1150, // 一次获取1150个文件
		Offset:  0,
	}

	for {
		resp, err := s.sdk115Open.GetFiles(context.Background(), req)
		if err != nil {
			s.logger.Errorf("获取115Open目录文件列表失败: CID=%s, 错误: %v", cid, err)
			return
		}

		s.logger.Debugf("获取到 %d 个文件/目录, CID: %s", len(resp.Data), cid)

		// 处理每个文件/目录
		for _, file := range resp.Data {
			filePath := filepath.Join(currentPath, file.Fn)

			// 如果是目录 (Fc == "0")
			if file.Fc == "0" {
				s.logger.Debugf("发现目录: %s", filePath)
				// 递归处理子目录
				s.walkDir115(file.Fid, filePath, cloudPath, depth+1, createdCount, skippedCount, errorCount)
			} else {
				// 如果是文件 (Fc == "1")
				s.logger.Debugf("处理文件: %s", filePath)

				// 检查过滤规则
				if cloudPath.FilterRules != "" {
					// include 未命中 -> 跳过
					if !pathhelper.IsFileMatchedByFilter(filePath, cloudPath.FilterRules, "include") {
						s.logger.Debugf("文件 %s 未命中 include 规则，跳过软链接", filePath)
						*skippedCount++
						continue
					}
					// 命中 download -> 跳过（不生成软链）
					if pathhelper.IsFileMatchedByFilter(filePath, cloudPath.FilterRules, "download") {
						s.logger.Debugf("文件 %s 命中 download 规则，跳过软链接", filePath)
						*skippedCount++
						continue
					}
				}

				// 创建软链接
				if createErr := s.CreateFile(filePath, cloudPath); createErr != nil {
					*errorCount++
				} else {
					*createdCount++
				}
			}
		}

		// 检查是否还有更多文件
		if req.Offset+req.Limit >= resp.Count {
			break
		}

		// 继续获取下一批文件
		req.Offset += req.Limit
	}
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

			// 根据配置转换路径格式：如果是 Windows 路径配置，转换为 Linux 风格用于内部处理
			if cloudPath.IsWindowsPath {
				relPath = pathhelper.ConvertToLinuxPath(relPath)
			} else {
				// 统一使用正斜杠进行内部处理
				relPath = strings.ReplaceAll(relPath, "\\", "/")
			}

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

// normalizePath 根据运行环境规范化路径分隔符
func (s *SymlinkService) normalizePath(path string) string {
	if runtime.GOOS == "windows" {
		// Windows 系统：使用反斜杠分隔符
		return strings.ReplaceAll(path, "/", "\\")
	}
	// Unix-like 系统：使用正斜杠分隔符
	return strings.ReplaceAll(path, "\\", "/")
}

// CheckSymlinkSupport 检查当前系统是否支持创建软链接
func (s *SymlinkService) CheckSymlinkSupport() error {
	// 创建临时文件用于测试
	tempDir := os.TempDir()
	testTarget := filepath.Join(tempDir, "filmfusion_symlink_test_target")
	testLink := filepath.Join(tempDir, "filmfusion_symlink_test_link")

	// 清理可能存在的测试文件
	os.Remove(testTarget)
	os.Remove(testLink)

	// 创建测试目标文件
	if err := os.WriteFile(testTarget, []byte("test"), 0644); err != nil {
		return err
	}
	defer os.Remove(testTarget)

	// 尝试创建软链接
	if err := os.Symlink(testTarget, testLink); err != nil {
		if runtime.GOOS == "windows" {
			s.logger.Warnf("Windows 系统软链接支持检查失败: %v", err)
			s.logger.Warnf("请确保满足以下条件之一：")
			s.logger.Warnf("1. 以管理员权限运行程序")
			s.logger.Warnf("2. 启用 Windows 开发者模式 (设置 -> 更新和安全 -> 开发者选项)")
			s.logger.Warnf("3. 在本地安全策略中启用 '创建符号链接' 权限")
		}
		return err
	}
	defer os.Remove(testLink)

	s.logger.Infof("系统软链接支持检查通过")
	return nil
}
