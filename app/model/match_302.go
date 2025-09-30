package model

import (
	"film-fusion/app/utils/pathhelper"
	"net/url"
	"strings"
)

type Match302 struct {
	ID             uint   `gorm:"primarykey" json:"id"`
	SourcePath     string `gorm:"size:500;not null;comment:源路径" json:"source_path"`
	TargetPath     string `gorm:"size:500;comment:目标路径" json:"target_path"`
	CloudStorageID uint   `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`

	// 关联关系
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

func (Match302) TableName() string {
	return "match_302"
}

// GetMatchedPath 根据源路径规则转换目标路径
// 将参数 targetPath 中的 SourcePath 部分替换成 TargetPath
// 参数:
//   - targetPath: 原始路径
//
// 返回:
//   - string: 转换后的路径
func (m *Match302) GetMatchedPath(targetPath string) string {
	// 首先进行解码
	decodedTargetPath, err := url.PathUnescape(targetPath)
	if err != nil {
		return targetPath
	}

	normalizedSource := pathhelper.EnsureLeadingSlash(m.SourcePath)
	normalizedTarget := pathhelper.EnsureLeadingSlash(decodedTargetPath)
	normalizedTargetPath := pathhelper.EnsureLeadingSlash(m.TargetPath)

	// sourcePath 是 targetPath 的子路径 - 需要在 targetPath 基础上添加 sourcePath 到 targetPath 的映射
	if pathhelper.IsSubPath(normalizedTarget, normalizedSource) {
		// 计算需要从 targetPath 中移除的 sourcePath 的父路径部分
		// 然后添加 targetPath 的映射

		pathStr := strings.Replace(normalizedTarget, normalizedSource, normalizedTargetPath, 1)

		cleanPath := strings.ReplaceAll(pathStr, "//", "/")

		return cleanPath
	}

	// 没有匹配关系，返回原始路径
	return normalizedTarget
}
