package service

import (
	"encoding/json"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

type Mp2NotifyTargetItem struct {
	Storage   string `json:"storage"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Pickcode  string `json:"pickcode"`
	Extension string `json:"extension"`
}

type MoviePilot2NotifyRequestDataTransferinfo struct {
	Success      bool                `json:"success"`
	TargetItem   Mp2NotifyTargetItem `json:"target_item"`
	TransferType string              `json:"transfer_type"`
	FileListNew  []string            `json:"file_list_new"`
}

type MoviePilot2NotifyRequestData struct {
	Type string `json:"type"`
	Data struct {
		Transferinfo MoviePilot2NotifyRequestDataTransferinfo
	} `json:"data"`
}

type MoviePilot2NotifyService struct {
	logger         *logger.Logger
	download115Svc *Download115Service
}

// NewMoviePilot2NotifyService 创建新的 CD2NotifyService 实例
func NewMoviePilot2NotifyService(log *logger.Logger, download115Svc *Download115Service) *MoviePilot2NotifyService {
	return &MoviePilot2NotifyService{
		logger:         log,
		download115Svc: download115Svc,
	}
}

func (s *MoviePilot2NotifyService) ProcessNotify(data MoviePilot2NotifyRequestData, cloudPaths []model.CloudPath) {
	// 处理 movie-pilot v2 的通知逻辑
	// 处理整理完成的通知
	if data.Type == "transfer.complete" {
		// 以 JSON 格式打印请求体
		if b, err := json.MarshalIndent(data, "", "  "); err != nil {
			s.logger.Errorf("序列化 movie-pilot v2 请求体失败: %v", err)
		} else {
			s.logger.Infof("%s", string(b))
		}

		s.HandleFileNotify(data.Data.Transferinfo, cloudPaths)
	}
}

func (s *MoviePilot2NotifyService) HandleFileNotify(transferInfo MoviePilot2NotifyRequestDataTransferinfo, cloudPaths []model.CloudPath) {
	s.logger.Infof("处理 movie-pilot v2 通知: %s", transferInfo.TransferType)
	if !transferInfo.Success {
		return
	}

	// 处理文件通知逻辑
	// 单文件完成通知：不移除首级目录
	strmSvc := NewStrmService(s.logger, s.download115Svc)
	symlinkSvc := NewSymlinkService(s.logger)

	for _, cloudPath := range cloudPaths {
		if !pathhelper.IsSubPath(transferInfo.TargetItem.Path, cloudPath.SourcePath) {
			continue
		}

		if cloudPath.LinkType == model.LinkTypeStrm {
			if transferInfo.TargetItem.Storage == "u115" && transferInfo.TargetItem.Type == "file" {
				// 创建 STRM 文件
				strmSvc.CreateFile(transferInfo.TargetItem.Path, cloudPath)
				// TODO Cache Pickcode 优化启播速度
				return
			}
		}

		// 软链接相关操作
		if cloudPath.LinkType == model.LinkTypeSymlink {
			if transferInfo.TargetItem.Storage == "u115" && transferInfo.TargetItem.Type == "file" {
				if err := symlinkSvc.CreateFile(transferInfo.TargetItem.Path, cloudPath); err != nil {
					s.logger.Errorf("创建软链接失败: %v", err)
				}
				return
			}
		}
	}
}
