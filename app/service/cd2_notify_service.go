package service

import (
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

type Cd2FileNotifyRequestData struct {
	Action          string `json:"action"`           // 操作类型 (create, rename, delete)
	IsDir           string `json:"is_dir"`           // 是否为目录
	SourceFile      string `json:"source_file"`      // 源文件路径
	DestinationFile string `json:"destination_file"` // 目标文件路径
}

type Cd2FileNotifyRequest struct {
	DeviceName string                     `json:"device_name"`
	UserName   string                     `json:"user_name"`
	Version    string                     `json:"version"`
	Data       []Cd2FileNotifyRequestData `json:"data"`
}

// CD2NotifyService 处理 CloudDrive2 的流媒体相关逻辑
type CD2NotifyService struct {
	logger         *logger.Logger
	download115Svc *Download115Service
}

// NewCD2NotifyService 创建新的 CD2NotifyService 实例
func NewCD2NotifyService(log *logger.Logger, download115Svc *Download115Service) *CD2NotifyService {
	return &CD2NotifyService{
		logger:         log,
		download115Svc: download115Svc,
	}
}

func (s *CD2NotifyService) ProcessFileNotify(dataItems []Cd2FileNotifyRequestData, cloudPaths []model.CloudPath) {
	for _, data := range dataItems {
		s.HandleFileNotify(data, cloudPaths)
	}
}

func (s *CD2NotifyService) HandleFileNotify(data Cd2FileNotifyRequestData, cloudPaths []model.CloudPath) {
	strmSvc := NewStrmService(s.logger, s.download115Svc)
	for _, cloudPath := range cloudPaths {
		// 如果 data.DestinationFile 和 data.SourceFile 都不是 cloudPath.SourcePath 的子路径就跳过
		if !pathhelper.IsSubPath(data.SourceFile, cloudPath.SourcePath) && !pathhelper.IsSubPath(data.DestinationFile, cloudPath.SourcePath) {
			continue
		}

		// STRM 相关操作
		if cloudPath.LinkType == model.LinkTypeStrm {
			if data.Action == "create" && data.IsDir == "false" {
				strmSvc.CreateFile(data.SourceFile, cloudPath)
				return
			}

			if data.Action == "rename" && data.IsDir == "false" {
				strmSvc.RenameFile(data.SourceFile, data.DestinationFile, cloudPath)
				return
			}

			if data.Action == "rename" && data.IsDir == "true" {
				// 目录重命名，需要处理目录下的所有文件并删除原目录
				strmSvc.RenameDir(data.SourceFile, data.DestinationFile, cloudPath)
				return
			}

			if data.Action == "delete" {
				strmSvc.DeleteStrm(data.SourceFile, cloudPath, data.IsDir == "true")
				return
			}
		}

		// 软连接相关操作
		if cloudPath.LinkType == model.LinkTypeSymlink {
			if data.Action == "create" && data.IsDir == "false" {
				s.logger.Debug("TODO 创建软连接操作")
				return
			}
		}

		return
	}
}
