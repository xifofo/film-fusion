package cover

import (
	"fmt"
	"sort"
	"sync"
)

// 全局模板注册表
var (
	templateRegistry   = make(map[string]Template)
	templateRegistryMu sync.RWMutex
)

// Register 注册一个模板（包初始化时调用）
func Register(t Template) {
	if t == nil || t.ID() == "" {
		return
	}
	templateRegistryMu.Lock()
	defer templateRegistryMu.Unlock()
	templateRegistry[t.ID()] = t
}

// Get 根据 ID 获取模板；找不到返回错误
func Get(id string) (Template, error) {
	templateRegistryMu.RLock()
	defer templateRegistryMu.RUnlock()
	t, ok := templateRegistry[id]
	if !ok {
		return nil, fmt.Errorf("封面模板不存在: %s", id)
	}
	return t, nil
}

// TemplateMeta 模板元数据（用于 API 列表展示）
type TemplateMeta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// List 列出所有已注册模板
func List() []TemplateMeta {
	templateRegistryMu.RLock()
	defer templateRegistryMu.RUnlock()
	out := make([]TemplateMeta, 0, len(templateRegistry))
	for _, t := range templateRegistry {
		out = append(out, TemplateMeta{ID: t.ID(), Name: t.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DefaultTemplateID 默认模板 ID（前端首次进来时使用）
const DefaultTemplateID = "multi_grid"
