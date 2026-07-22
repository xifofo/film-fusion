package service

import (
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"fmt"
)

const MaxCD2NotifyItems = 1000

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

func (r Cd2FileNotifyRequest) Validate() error {
	if len(r.Data) == 0 {
		return fmt.Errorf("data must contain at least one item")
	}
	if len(r.Data) > MaxCD2NotifyItems {
		return fmt.Errorf("data exceeds the maximum of %d items", MaxCD2NotifyItems)
	}
	for i, item := range r.Data {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("data[%d]: %w", i, err)
		}
	}
	return nil
}

func (d Cd2FileNotifyRequestData) Validate() error {
	switch d.Action {
	case "create", "delete", "rename":
	default:
		return fmt.Errorf("unsupported action %q", d.Action)
	}
	if d.IsDir != "true" && d.IsDir != "false" {
		return fmt.Errorf("is_dir must be true or false")
	}
	if _, err := pathhelper.NormalizeUntrustedPath(d.SourceFile); err != nil {
		return fmt.Errorf("invalid source_file: %w", err)
	}
	if d.Action == "rename" {
		if _, err := pathhelper.NormalizeUntrustedPath(d.DestinationFile); err != nil {
			return fmt.Errorf("invalid destination_file: %w", err)
		}
	} else if d.DestinationFile != "" {
		if _, err := pathhelper.NormalizeUntrustedPath(d.DestinationFile); err != nil {
			return fmt.Errorf("invalid destination_file: %w", err)
		}
	}
	return nil
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
		sourceInside := pathhelper.IsSubPath(data.SourceFile, cloudPath.SourcePath)
		destinationInside := data.Action == "rename" && pathhelper.IsSubPath(data.DestinationFile, cloudPath.SourcePath)
		if !sourceInside && !destinationInside {
			s.logger.Debugf("%s 和 %s 不是 %s 的子路径", data.SourceFile, data.DestinationFile, cloudPath.SourcePath)
			continue
		}

		// STRM 相关操作
		if cloudPath.LinkType == model.LinkTypeStrm {
			// 一般复制操作会触发
			if data.Action == "create" && sourceInside && data.IsDir == "true" {
				strmSvc.CreateDir(data.SourceFile, cloudPath)
				return
			}

			if data.Action == "create" && sourceInside && data.IsDir == "false" {
				strmSvc.CreateFile(data.SourceFile, cloudPath)
				return
			}

			if data.Action == "rename" && data.IsDir == "false" {
				strmSvc.RenameFile(data.SourceFile, data.DestinationFile, cloudPath, sourceInside, destinationInside)
				return
			}

			if data.Action == "rename" && data.IsDir == "true" {
				// 目录重命名，需要处理目录下的所有文件并删除原目录
				strmSvc.RenameDir(data.SourceFile, data.DestinationFile, cloudPath, sourceInside, destinationInside)
				return
			}

			if data.Action == "delete" && sourceInside {
				strmSvc.DeleteStrm(data.SourceFile, cloudPath, data.IsDir == "true")
				return
			}
		}

		return
	}
}
