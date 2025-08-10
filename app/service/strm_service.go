package service

import (
	"context"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"os"
	"path/filepath"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

// StrmService STRM 文件处理服务
type StrmService struct {
	logger         *logger.Logger
	sdk115Open     *sdk115.Client
	download115Svc *Download115Service
	removeFirstDir bool
}

// NewStrmService 创建新的 StrmService
func NewStrmService(log *logger.Logger, download115Svc *Download115Service, removeFirstDir bool) *StrmService {
	return &StrmService{
		logger:         log,
		sdk115Open:     sdk115.New(),
		download115Svc: download115Svc,
		removeFirstDir: removeFirstDir,
	}
}

func (s *StrmService) CreateFile(path string, cloudPath model.CloudPath) error {
	// 根据 IsWindowsPath 字段决定是否转换路径
	var processPath string
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	} else {
		processPath = path
	}

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过 STRM 文件处理", cloudPath.ID)
		return nil
	}

	// 判断是否在 include 过滤规则中
	if cloudPath.FilterRules != "" {
		// 获取文件扩展名
		fileExt := strings.ToLower(filepath.Ext(processPath))

		if !pathhelper.IsFileInAnyFilterRules(fileExt, cloudPath.FilterRules) {
			s.logger.Debugf("文件 %s 不在 include 过滤规则中，跳过处理", processPath)
			return nil
		}
	}

	// 处理 115OPEN API 创建 STRM 文件
	if cloudPath.CloudStorage.StorageType == model.StorageType115Open {
		s.CreateStrmOrDownloadWith115OpenAPI(processPath, cloudPath)
		return nil
	}

	return nil
}

func (s *StrmService) RenameFile(originalPath, path string, cloudPath model.CloudPath) {
	var processPath string
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	} else {
		processPath = path
	}

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过 STRM 文件处理", cloudPath.ID)
		return
	}

	// 判断是否在 include 过滤规则中
	if cloudPath.FilterRules != "" {
		// 获取文件扩展名
		fileExt := strings.ToLower(filepath.Ext(processPath))

		if !pathhelper.IsFileInAnyFilterRules(fileExt, cloudPath.FilterRules) {
			s.logger.Debugf("文件 %s 不在 include 过滤规则中，跳过处理", processPath)
			return
		}
	}

	// 处理 115OPEN API 创建 STRM 文件
	if cloudPath.CloudStorage.StorageType == model.StorageType115Open {
		s.CreateStrmOrDownloadWith115OpenAPI(processPath, cloudPath)
		// 不能 Return --- 因为可能需要删除原来的文件
	}

	// 原路径也在监控目录内时，需要删除本地的内容
	if pathhelper.IsSubPath(originalPath, cloudPath.SourcePath) {
		savePath := filepath.Join(cloudPath.LocalPath, originalPath)
		s.DeleteAction(savePath, false)
	}
}

func (s *StrmService) RenameDir(originalPath, path string, cloudPath model.CloudPath) {
	var processPath string
	if cloudPath.IsWindowsPath {
		processPath = pathhelper.ConvertToLinuxPath(path)
	} else {
		processPath = path
	}

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过 STRM 文件处理", cloudPath.ID)
		return
	}

	if cloudPath.CloudStorage.StorageType == model.StorageType115Open {
		s.WalkDirWith115OpenAPI(processPath, cloudPath)
	}

	// 原路径也在监控目录内时，需要删除本地的内容
	if pathhelper.IsSubPath(originalPath, cloudPath.SourcePath) {
		savePath := filepath.Join(cloudPath.LocalPath, originalPath)
		s.DeleteAction(savePath, true)
	}
}

// WalkDirWith115OpenAPI 使用115 Open API递归遍历目录
// 该方法会：
// 1. 使用115 SDK获取指定目录下的所有文件和子目录
// 2. 对符合过滤规则的文件调用 pathhelper.IsFileInAnyFilterRules() 进行过滤
// 3. 为通过过滤的文件创建STRM文件或添加到下载队列
// 4. 对子目录进行递归遍历
//
// 参数：
//   - dirPath: 要遍历的目录路径
//   - cloudPath: 云盘路径配置信息，包含过滤规则等
func (s *StrmService) WalkDirWith115OpenAPI(dirPath string, cloudPath model.CloudPath) {
	// 设置访问令牌
	s.sdk115Open.SetAccessToken(cloudPath.CloudStorage.AccessToken)

	// 转换路径为云盘路径
	sourceCloudPath := filepath.Join("/", pathhelper.RemoveFirstDir(dirPath))

	// 获取目录信息
	folderInfo, err := s.sdk115Open.GetFolderInfoByPath(context.Background(), sourceCloudPath)
	if err != nil {
		s.logger.Errorf("获取115Open目录信息失败: %s, 错误: %v", sourceCloudPath, err)
		return
	}

	s.logger.Debugf("开始遍历115Open目录: %s (CID: %s)", sourceCloudPath, folderInfo.FileID)

	// 递归遍历目录
	s.walkDir115(folderInfo.FileID, dirPath, cloudPath, 0)
}

// walkDir115 递归遍历115目录的内部实现
// 该方法处理分页获取文件列表，并对每个文件/目录进行相应处理
//
// 参数：
//   - cid: 115目录的ID (从GetFolderInfoByPath获取的PickCode)
//   - currentPath: 当前遍历的路径
//   - cloudPath: 云盘路径配置信息
//   - depth: 当前递归深度，用于防止无限递归
func (s *StrmService) walkDir115(cid, currentPath string, cloudPath model.CloudPath, depth int) {
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
				s.walkDir115(file.Fid, filePath, cloudPath, depth+1)
			} else {
				// 如果是文件 (Fc == "1")
				fileExt := strings.ToLower(filepath.Ext(file.Fn))

				// 检查文件是否在过滤规则中
				if cloudPath.FilterRules != "" && !pathhelper.IsFileInAnyFilterRules(fileExt, cloudPath.FilterRules) {
					s.logger.Debugf("文件 %s 不在过滤规则中，跳过处理", filePath)
					continue
				}

				s.logger.Debugf("处理文件: %s", filePath)

				// 为符合过滤规则的文件创建STRM文件或下载
				s.CreateStrmOrDownloadWith115OpenAPI(filePath, cloudPath)
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

func (s *StrmService) CreateStrmOrDownloadWith115OpenAPI(path string, cloudPath model.CloudPath) {
	savePath := filepath.Join(cloudPath.LocalPath, path)
	fileExt := strings.ToLower(filepath.Ext(savePath))

	fullPathName := savePath[:len(savePath)-len(fileExt)]

	// 如果匹配中下载的后缀直接调用 115Open API 下载
	if pathhelper.IsFileMatchedByFilter(fileExt, cloudPath.FilterRules, "download") {
		sourceCloudPath := filepath.Join("/", path)
		if s.removeFirstDir {
			sourceCloudPath = filepath.Join("/", pathhelper.RemoveFirstDir(path))
		}

		// 不重复下载
		if _, err := os.Stat(savePath); err == nil {
			s.logger.Infof("本地文件已存在，跳过下载: %s", savePath)
			return
		}

		s.sdk115Open.SetAccessToken(cloudPath.CloudStorage.AccessToken)
		folderInfo, err := s.sdk115Open.GetFolderInfoByPath(context.Background(), sourceCloudPath)
		if err != nil {
			s.logger.Errorf("获取115Open文件夹信息失败: %v", err)
			return
		}

		s.logger.Debugf("获取115Open信息成功: %s", folderInfo.PickCode)
		s.download115Svc.AddDownloadTask(cloudPath.CloudStorage.ID, folderInfo.PickCode, savePath)
		return
	}

	// 添加新的扩展名
	strmFilePath := fullPathName + ".strm"

	// 判断本地文件是否存在，如果存在则删除
	if _, err := os.Stat(strmFilePath); err == nil {
		err := os.Remove(strmFilePath)
		if err != nil {
			s.logger.Errorf("删除已存在的 STRM 文件失败: %v", err)
			return
		}
	}

	content := filepath.Join(cloudPath.ContentPrefix, path)

	if cloudPath.StrmContentType == model.StrmContentTypePath && cloudPath.IsWindowsPath {
		content = pathhelper.ConvertToWindowsPath(content)
	}

	// 提前创建文件夹
	err := os.MkdirAll(filepath.Dir(savePath), 0755)
	if err != nil {
		s.logger.Errorf("创建目录失败: %v", err)
		return
	}

	err = os.WriteFile(strmFilePath, []byte(content), 0777)
	if err != nil {
		s.logger.Errorf("创建 STRM 文件失败: %v", err)
		return
	}

	s.logger.Debugf("创建 STRM 文件到: %s", strmFilePath)
	s.logger.Debugf("STRM 文件内容: %s", content)
}

func (s *StrmService) DeleteStrm(path string, cloudPath model.CloudPath, isDir bool) {
	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过 STRM 文件删除", cloudPath.ID)
		return
	}

	// 判断是否子目录
	if !pathhelper.IsSubPath(path, cloudPath.SourcePath) {
		s.logger.Debugf("路径 %s 不是 CloudPath (ID: %d) 的子目录，跳过删除", path, cloudPath.ID)
		return
	}

	savePath := filepath.Join(cloudPath.LocalPath, path)
	s.DeleteAction(savePath, isDir)
}

func (s *StrmService) DeleteAction(localPath string, isDirectory bool) {
	// 如果是文件夹直接全部删除
	if isDirectory {
		err := os.RemoveAll(localPath)
		if err != nil && !os.IsNotExist(err) {
			s.logger.Errorf("删除本地文件夹失败： %s", localPath)
		}

		return
	}

	// 获取删除文件的后缀
	ext := filepath.Ext(localPath)

	fullPathName := localPath[:len(localPath)-len(ext)]
	nfoFilename := fullPathName + ".nfo"

	// 删除对应的 nfo 文件
	err := os.Remove(nfoFilename)
	if err != nil && !os.IsNotExist(err) {
		s.logger.Errorf("删除本地 nfo 文件失败：%s", nfoFilename)
		s.logger.Errorf("错误原因：%v", err)
	}

	// 删除 STRM 文件
	strmFilename := fullPathName + ".strm"
	err = os.Remove(strmFilename)
	if err != nil && !os.IsNotExist(err) {
		s.logger.Errorf("删除本地 strm 文件失败：%s", strmFilename)
		s.logger.Errorf("错误原因：%v", err)
	}

	// TODO 有必要时支持删除在下载扩展名内的文件
}
