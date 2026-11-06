// 通用数据字典的读取、分页和字段规则；业务候选与管理员维护查询分开。
package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// DictionaryError 提供稳定原因供前端翻译，不向客户端暴露 SQL 或数据库错误。
type DictionaryError struct {
	Status  int
	Reason  string
	Message string
}

func (e *DictionaryError) Error() string { return e.Message }

var (
	ErrDictionaryInvalid  = &DictionaryError{400, "INVALID_INPUT", "字典字段或查询参数无效"}
	ErrDictionaryMissing  = &DictionaryError{404, "NOT_FOUND", "字典或字典项不存在"}
	ErrDictionaryExists   = &DictionaryError{409, "DUPLICATE", "字典编码或字典项值已存在"}
	ErrDictionaryVersion  = &DictionaryError{409, "VERSION_CONFLICT", "记录已被其他操作修改，请重新打开后确认"}
	ErrDictionaryDisabled = &DictionaryError{409, "DICTIONARY_DISABLED", "字典已停用"}

	dictionaryCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	app115ValuePattern    = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

// DictionaryService 由 HTTP 装配数据库，鉴权由项目统一管理员中间件执行。
type DictionaryService struct{ db *gorm.DB }

// NewDictionaryService 不创建额外连接，所有条件写入复用调用方数据库。
func NewDictionaryService(db *gorm.DB) *DictionaryService { return &DictionaryService{db: db} }

// DictionaryQuery 对完整结果先筛选和排序，再分页；IDs 仅供候选精确回显。
type DictionaryQuery struct {
	Page, PageSize      int
	Search, Sort, Order string
	Enabled             *bool
	IDs                 []string
}

// DictionaryPage 是通用管理及候选接口的分页响应。
type DictionaryPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// DictionaryOption 只包含可公开的稳定值和标签，不返回内部管理字段。
type DictionaryOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func validDictionaryText(value string, max int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		utf8.RuneCountInString(value) <= max && (!required || value != "" && strings.TrimSpace(value) == value)
}

func validDictionaryQuery(q DictionaryQuery) bool {
	return q.Page > 0 && q.Page <= 1_000_000 && q.PageSize > 0 && q.PageSize <= 100 &&
		validDictionaryText(q.Search, 100, false) && (q.Order == "" || q.Order == "asc" || q.Order == "desc") && len(q.IDs) <= 100
}

func dictionaryResultError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrDictionaryMissing
	}
	return err
}

// Get 返回包括停用类型的当前详情，编辑表单使用此版本作为保存前提。
func (s *DictionaryService) Get(ctx context.Context, code string) (model.Dictionary, error) {
	var row model.Dictionary
	err := s.db.WithContext(ctx).Where("code = ?", code).First(&row).Error
	return row, dictionaryResultError(err)
}

// GetItem 按父类型限定项 ID，防止通过其他类型的路径访问或修改记录。
func (s *DictionaryService) GetItem(ctx context.Context, code, id string) (model.DictionaryItem, error) {
	parent, err := s.Get(ctx, code)
	if err != nil {
		return model.DictionaryItem{}, err
	}

	var row model.DictionaryItem
	err = s.db.WithContext(ctx).Where("dictionary_id = ? AND id = ?", parent.ID, id).First(&row).Error
	return row, dictionaryResultError(err)
}

// List 管理类型，支持编码、名称、说明搜索和日期字段全结果排序。
func (s *DictionaryService) List(ctx context.Context, q DictionaryQuery) (DictionaryPage[model.Dictionary], error) {
	result := DictionaryPage[model.Dictionary]{Items: []model.Dictionary{}, Page: q.Page, PageSize: q.PageSize}
	fields := map[string]string{"code": "code", "name": "name", "createdAt": "created_at", "updatedAt": "updated_at"}
	order, err := dictionaryOrder(q, fields, "created_at DESC")
	if err != nil {
		return result, err
	}

	db := s.db.WithContext(ctx).Model(&model.Dictionary{})
	if q.Search != "" {
		keyword := strings.ToLower(q.Search)
		db = db.Where("instr(lower(code), ?) > 0 OR instr(lower(name), ?) > 0 OR instr(lower(description), ?) > 0", keyword, keyword, keyword)
	}
	if q.Enabled != nil {
		db = db.Where("enabled = ?", *q.Enabled)
	}
	if err := db.Count(&result.Total).Error; err != nil {
		return result, err
	}
	err = db.Order(order).Order("id ASC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&result.Items).Error
	return result, err
}

// Items 管理查询含停用项，不受父类型启停状态限制。
func (s *DictionaryService) Items(ctx context.Context, code string, q DictionaryQuery) (DictionaryPage[model.DictionaryItem], error) {
	result := DictionaryPage[model.DictionaryItem]{Items: []model.DictionaryItem{}, Page: q.Page, PageSize: q.PageSize}
	fields := map[string]string{"value": "value", "label": "label", "sortOrder": "sort_order", "createdAt": "created_at", "updatedAt": "updated_at"}
	order, err := dictionaryOrder(q, fields, "sort_order ASC")
	if err != nil {
		return result, err
	}
	parent, err := s.Get(ctx, code)
	if err != nil {
		return result, err
	}

	db := s.db.WithContext(ctx).Model(&model.DictionaryItem{}).Where("dictionary_id = ?", parent.ID)
	if q.Search != "" {
		keyword := strings.ToLower(q.Search)
		db = db.Where("instr(lower(value), ?) > 0 OR instr(lower(label), ?) > 0 OR instr(lower(description), ?) > 0", keyword, keyword, keyword)
	}
	if q.Enabled != nil {
		db = db.Where("enabled = ?", *q.Enabled)
	}
	if err := db.Count(&result.Total).Error; err != nil {
		return result, err
	}
	err = db.Order(order).Order("id ASC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&result.Items).Error
	return result, err
}

func dictionaryOrder(q DictionaryQuery, fields map[string]string, fallback string) (string, error) {
	if !validDictionaryQuery(q) {
		return "", ErrDictionaryInvalid
	}
	if q.Sort == "" {
		return fallback, nil
	}
	field, ok := fields[q.Sort]
	if !ok {
		return "", ErrDictionaryInvalid
	}
	order := "ASC"
	if q.Order == "desc" {
		order = "DESC"
	}
	return field + " " + order, nil
}

// Options 读取当前启用候选；停用类型明确报错，手动 Client ID 不受候选列表约束。
func (s *DictionaryService) Options(ctx context.Context, code string, q DictionaryQuery) (DictionaryPage[DictionaryOption], error) {
	result := DictionaryPage[DictionaryOption]{Items: []DictionaryOption{}, Page: q.Page, PageSize: q.PageSize}
	if !validDictionaryQuery(q) {
		return result, ErrDictionaryInvalid
	}
	for _, value := range q.IDs {
		if !validDictionaryText(value, 128, true) {
			return result, ErrDictionaryInvalid
		}
	}
	parent, err := s.Get(ctx, code)
	if err != nil {
		return result, err
	}
	if !parent.Enabled {
		return result, ErrDictionaryDisabled
	}

	db := s.db.WithContext(ctx).Model(&model.DictionaryItem{}).Where("dictionary_id = ? AND enabled = ?", parent.ID, true)
	if len(q.IDs) > 0 {
		db = db.Where("value IN ?", q.IDs)
	} else if q.Search != "" {
		keyword := strings.ToLower(q.Search)
		db = db.Where("instr(lower(value), ?) > 0 OR instr(lower(label), ?) > 0", keyword, keyword)
	}
	if err := db.Count(&result.Total).Error; err != nil {
		return result, err
	}
	db = db.Select("value, label").Order("sort_order ASC, id ASC")
	if len(q.IDs) > 0 {
		result.Page, result.PageSize = 1, max(1, len(q.IDs))
	} else {
		db = db.Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize)
	}
	err = db.Find(&result.Items).Error
	return result, err
}
