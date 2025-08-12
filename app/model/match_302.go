package model

type Match302 struct {
	ID             uint   `gorm:"primarykey" json:"id"`
	SourcePath     string `gorm:"size:500;not null;comment:源路径" json:"source_path"`
	TargetPath     string `gorm:"size:500;comment:目标路径" json:"target_path"`
	CloudStorageID uint   `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`
}

func (Match302) TableName() string {
	return "match_302"
}
