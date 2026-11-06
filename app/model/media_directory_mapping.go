package model

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"film-fusion/app/utils/pathhelper"
	"gorm.io/gorm"
)

// MappingRuntimeConfig 将公共输出根换算回同步事件的坐标系。
// 事件本身保留挂载名，云端查询、过滤、删除保护和 Emby 相对路径规则均保持原样。
// 换算后的副本清空前缀，因此多层调用不会重复换算，也绝不能将此副本保存回数据库。
func (d MediaDirectory) MappingRuntimeConfig() (MediaDirectory, error) {
	if d.MappingPathPrefix == "" {
		return d, nil
	}
	prefix := d.MappingPathPrefix
	if d.SourceType != SourceTypeCloudDrive2 || !d.MappingEnabled || !d.OrganizeEnabled ||
		prefix != "/"+strings.Split(strings.TrimPrefix(d.SourcePath, "/"), "/")[0] || prefix == "/" {
		return d, fmt.Errorf("同步输出换算需要同时启用整理与 CloudDrive2 同步，且前缀必须是源路径的挂载名")
	}
	if _, err := pathhelper.NormalizeUntrustedPath(prefix); err != nil || path.Clean(prefix) != prefix {
		return d, fmt.Errorf("同步输出换算前缀无效")
	}
	root := filepath.ToSlash(filepath.Clean(d.SavePath))
	if !filepath.IsAbs(d.SavePath) || !strings.HasSuffix(root, prefix) {
		return d, fmt.Errorf("STRM 保存根目录末尾必须包含同步换算前缀 %s", prefix)
	}
	contentPrefix := prefix
	if d.ContentEncodeURI {
		contentPrefix = "/" + url.PathEscape(strings.TrimPrefix(prefix, "/"))
	}
	content := strings.TrimRight(d.ContentPrefix, "/")
	if !strings.HasSuffix(content, contentPrefix) {
		return d, fmt.Errorf("STRM 内容前缀末尾必须包含同步换算前缀 %s", contentPrefix)
	}
	d.SavePath = strings.TrimSuffix(root, prefix)
	if d.SavePath == "" {
		d.SavePath = "/"
	}
	d.ContentPrefix = strings.TrimSuffix(content, contentPrefix)
	if d.ContentPrefix == "" && strings.HasPrefix(content, "/") {
		d.ContentPrefix = "/"
	}
	d.MappingPathPrefix = ""
	return d, nil
}

func MappingRuntimeConfigs(rows []MediaDirectory) ([]MediaDirectory, error) {
	result := make([]MediaDirectory, len(rows))
	for i, row := range rows {
		value, err := row.MappingRuntimeConfig()
		if err != nil {
			return nil, err
		}
		result[i] = value
	}
	return result, nil
}

// ResolveMergedMediaDirectoryID 只解析同一用户的合并来源，供合并前已启动的消费者使用。
func ResolveMergedMediaDirectoryID(db *gorm.DB, userID, id uint) (uint, error) {
	var row MediaDirectory
	if err := db.Unscoped().Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		return 0, err
	}
	if !row.DeletedAt.Valid {
		return row.ID, nil
	}
	if row.MergedIntoID == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	var target MediaDirectory
	if err := db.Where("id = ? AND user_id = ? AND cloud_storage_id = ?", row.MergedIntoID, userID, row.CloudStorageID).First(&target).Error; err != nil {
		return 0, err
	}
	return target.ID, nil
}
