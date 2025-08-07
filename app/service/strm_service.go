package service

import (
	"context"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"path/filepath"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

// StrmService STRM 文件处理服务
type StrmService struct {
	logger         *logger.Logger
	sdk115Open     *sdk115.Client
	download115Svc *Download115Service
}

// NewStrmService 创建新的 StrmService
func NewStrmService(log *logger.Logger, download115Svc *Download115Service) *StrmService {
	return &StrmService{
		logger:         log,
		sdk115Open:     sdk115.New(),
		download115Svc: download115Svc,
	}
}

func (s *StrmService) CreateFile(path string, cloudPath model.CloudPath) error {
	linuxPath := pathhelper.ConvertToLinuxPath(path)

	fmt.Println(linuxPath, "linuxPath")

	if cloudPath.LocalPath == "" {
		s.logger.Warnf("CloudPath (ID: %d) 没有设置 LocalPath，跳过 STRM 文件处理", cloudPath.ID)
		return nil
	}

	// 判断是否在 include 过滤规则中
	if cloudPath.FilterRules != "" {
		// 获取文件扩展名
		fileExt := strings.ToLower(filepath.Ext(linuxPath))

		if !pathhelper.IsFileInAnyFilterRules(fileExt, cloudPath.FilterRules) {
			s.logger.Debugf("文件 %s 不在 include 过滤规则中，跳过处理", linuxPath)
			return nil
		}
	}

	// 处理 115OPEN API 创建 STRM 文件
	if cloudPath.CloudStorage.StorageType == model.StorageType115Open {
		s.CreateStrmOrDownloadWith115OpenAPI(linuxPath, cloudPath)
		return nil
	}

	return nil
}

func (s *StrmService) CreateStrmOrDownloadWith115OpenAPI(path string, cloudPath model.CloudPath) {
	savePath := filepath.Join(cloudPath.LocalPath, path)
	fileExt := strings.ToLower(filepath.Ext(savePath))

	fullPathName := savePath[:len(savePath)-len(fileExt)]

	// 如果匹配中下载的后缀直接调用 115Open API 下载
	if pathhelper.IsFileMatchedByFilter(fileExt, cloudPath.FilterRules, "download") {
		// TODO 这里暂时只支持 CD2 的 path, 得支持其他路径
		sourceCloudPath := filepath.Join("/", pathhelper.RemoveFirstDir(path))

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

	s.logger.Debugf("创建或下载 STRM 文件到: %s", strmFilePath)
	// 本地文件是否存在

}
