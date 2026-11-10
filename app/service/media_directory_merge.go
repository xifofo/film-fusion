package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"gorm.io/gorm"
)

type MediaDirectoryMergePreview struct {
	Directory      model.MediaDirectory `json:"directory"`
	MappingID      uint                 `json:"mapping_id"`
	Revision       string               `json:"revision"`
	References     map[string]int64     `json:"references"`
	SampleSavePath string               `json:"sample_save_path"`
	SampleContent  string               `json:"sample_content"`
}

// PreviewMediaDirectoryMerge 只接受一条整理配置和一条同账号的纯同步配置。
// 保留整理 ID、命名、过滤和洗版设置；同步的来源、规则、Emby 前缀原样复制。
func PreviewMediaDirectoryMerge(db *gorm.DB, userID, targetID, mappingID uint) (*MediaDirectoryMergePreview, error) {
	if targetID == 0 || mappingID == 0 || targetID == mappingID {
		return nil, fmt.Errorf("请选择不同的整理目录和同步目录")
	}
	var target, mapping model.MediaDirectory
	for id, row := range map[uint]*model.MediaDirectory{targetID: &target, mappingID: &mapping} {
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(row).Error; err != nil {
			return nil, fmt.Errorf("媒体目录不存在或无权限")
		}
	}
	if !target.OrganizeEnabled || target.MappingEnabled || !mapping.MappingEnabled || mapping.OrganizeEnabled || mapping.MappingPathPrefix != "" {
		return nil, fmt.Errorf("合并需要一条仅用于整理的目录和一条仅用于同步的目录")
	}
	if target.CloudStorageID != mapping.CloudStorageID {
		return nil, fmt.Errorf("不能合并不同网盘账号的目录")
	}
	if mapping.SourceType != model.SourceTypeCloudDrive2 || mapping.LinkType != model.LinkTypeStrm {
		return nil, fmt.Errorf("当前无损合并仅支持 CloudDrive2 的 STRM 同步目录")
	}
	cloudRoot, err := pathhelper.NormalizeUntrustedPath("/" + strings.Trim(target.DirectoryName, "/"))
	if err != nil {
		return nil, err
	}
	source, err := pathhelper.NormalizeUntrustedPath(mapping.SourcePath)
	if err != nil || source != mapping.SourcePath || source == "/" {
		return nil, fmt.Errorf("同步源路径无效")
	}
	mount := "/" + strings.Split(strings.TrimPrefix(source, "/"), "/")[0]
	if strings.TrimPrefix(source, mount) != cloudRoot {
		return nil, fmt.Errorf("同步源路径与整理目录的云端路径不一致，不能自动合并")
	}
	if target.ContentEncodeURI != mapping.ContentEncodeURI {
		return nil, fmt.Errorf("两条目录的 URI 编码设置不同，不能无损合并")
	}
	if !filepath.IsAbs(target.SavePath) || filepath.Clean(target.SavePath) != pathhelper.SafeFilePathJoin(mapping.SavePath, mount) {
		return nil, fmt.Errorf("STRM 保存路径不等价，不能无损合并")
	}
	contentMount := mount
	if mapping.ContentEncodeURI {
		contentMount = "/" + url.PathEscape(strings.TrimPrefix(mount, "/"))
	}
	if strings.TrimRight(target.ContentPrefix, "/") != strings.TrimRight(pathhelper.SafeFilePathJoin(mapping.ContentPrefix, contentMount), "/") {
		return nil, fmt.Errorf("STRM 内容前缀不等价，不能无损合并")
	}
	// 两侧都检查；历史快照保留原值，未完成的计划和恢复任务仍阻止合并。
	if err := checkMediaDirectoryOrganizeTasks(db, []uint{targetID, mappingID}); err != nil {
		return nil, err
	}

	data, err := json.Marshal([]model.MediaDirectory{target, mapping})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	target.MappingEnabled = true
	target.SourcePath, target.SourceType = mapping.SourcePath, mapping.SourceType
	target.EmbyPathPrefix, target.LinkType = mapping.EmbyPathPrefix, mapping.LinkType
	target.FilterRules, target.StrmContentType = mapping.FilterRules, mapping.StrmContentType
	target.MappingPathPrefix = mount
	if _, err := target.MappingRuntimeConfig(); err != nil {
		return nil, err
	}
	refs, err := mediaDirectoryMergeReferences(db, userID, mappingID, targetID, false)
	if err != nil {
		return nil, err
	}
	sample := path.Join(cloudRoot, "示例.mkv")
	contentSample := sample
	if target.ContentEncodeURI {
		parts := strings.Split(sample, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		contentSample = strings.Join(parts, "/")
	}
	return &MediaDirectoryMergePreview{Directory: target, MappingID: mappingID, Revision: hex.EncodeToString(hash[:]), References: refs, SampleSavePath: strings.TrimSuffix(pathhelper.SafeFilePathJoin(target.SavePath, sample), ".mkv") + ".strm", SampleContent: pathhelper.SafeFilePathJoin(target.ContentPrefix, contentSample)}, nil
}

// ApplyMediaDirectoryMerge 必须在已取得写锁的事务内调用；引用和两条配置一并提交或回滚。
func ApplyMediaDirectoryMerge(db *gorm.DB, userID uint, preview *MediaDirectoryMergePreview) error {
	if _, err := mediaDirectoryMergeReferences(db, userID, preview.MappingID, preview.Directory.ID, true); err != nil {
		return err
	}
	if err := db.Model(&model.MediaDirectory{}).Where("id = ? AND user_id = ?", preview.MappingID, userID).Update("merged_into_id", preview.Directory.ID).Error; err != nil {
		return err
	}
	if err := db.Where("id = ? AND user_id = ?", preview.MappingID, userID).Delete(&model.MediaDirectory{}).Error; err != nil {
		return err
	}
	return db.Omit("CloudStorage", "User").Save(&preview.Directory).Error
}
