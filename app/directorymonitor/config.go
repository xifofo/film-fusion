// Package directorymonitor discovers stable changes with bounded memory. The
// database is the traversal queue, snapshot and event outbox; fsnotify is an
// optional latency optimization and periodic scans remain authoritative.
package directorymonitor

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"film-fusion/app/model"
)

const MaxGlobalWatches = 8192

func Models() []any {
	return []any{&model.LocalDirectoryState{}, &model.LocalDirectoryScan{}, &model.LocalDirectoryItem{}}
}

// NormalizeOptions applies defaults and enforces actual resource bounds.
func NormalizeOptions(o model.LocalDirectoryConfig) (model.LocalDirectoryConfig, error) {
	o.Events = append([]string(nil), o.Events...)
	o.IncludeExtensions = append([]string(nil), o.IncludeExtensions...)
	o.ExcludePatterns = append([]string(nil), o.ExcludePatterns...)
	if o.Mode == "" {
		o.Mode = "auto"
	}
	if o.Mode != "auto" && o.Mode != "poll" {
		return o, errors.New("监听模式必须为 auto 或 poll")
	}
	if len(o.Events) == 0 {
		o.Events = []string{"created"}
	}
	if len(o.Events) > 2 {
		return o, errors.New("目录事件最多选择新增和修改两种")
	}
	seen := map[string]bool{}
	for _, event := range o.Events {
		if (event != "created" && event != "modified") || seen[event] {
			return o, errors.New("目录事件必须为不重复的 created 或 modified")
		}
		seen[event] = true
	}
	if o.ItemType == "" {
		o.ItemType = "file"
	}
	if o.ItemType != "file" && o.ItemType != "directory" && o.ItemType != "all" {
		return o, errors.New("监控对象必须为 file、directory 或 all")
	}
	if o.InitialScan == "" {
		o.InitialScan = "baseline"
	}
	if o.InitialScan != "baseline" && o.InitialScan != "process_existing" {
		return o, errors.New("首次扫描策略必须为 baseline 或 process_existing")
	}
	if o.MaxWatches == 0 {
		o.MaxWatches = 1024
	}
	if o.MaxWatches < 1 || o.MaxWatches > MaxGlobalWatches {
		return o, fmt.Errorf("监听目录上限必须在 1 到 %d 之间", MaxGlobalWatches)
	}
	if o.BatchSize == 0 {
		o.BatchSize = 128
	}
	if o.BatchSize < 16 || o.BatchSize > 512 {
		return o, errors.New("扫描批次大小必须在 16 到 512 之间")
	}
	for _, bound := range []struct {
		name                       string
		value                      *int
		fallback, minimum, maximum int
	}{
		{"扫描时间预算", &o.ScanBudgetMS, 2000, 100, 10000},
		{"扫描项目预算", &o.ScanItemLimit, 4096, 128, 65536},
		{"待执行任务上限", &o.MaxPendingRuns, 1000, 1, 100000},
		{"历史保留天数", &o.HistoryRetentionDays, 30, 1, 365},
		{"缺失快照保留天数", &o.MissingRetentionDays, 7, 1, 365},
		{"历史任务保留上限", &o.HistoryMaxRuns, 10000, 100, 1000000},
	} {
		if *bound.value == 0 {
			*bound.value = bound.fallback
		}
		if *bound.value < bound.minimum || *bound.value > bound.maximum {
			return o, fmt.Errorf("%s必须在 %d 到 %d 之间", bound.name, bound.minimum, bound.maximum)
		}
	}
	if len(o.IncludeExtensions) > 128 || len(o.ExcludePatterns) > 128 {
		return o, errors.New("扩展名和排除规则分别不能超过 128 条")
	}
	for i, ext := range o.IncludeExtensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" || len(ext) > 64 || strings.ContainsAny(ext, "/\\*?[]") {
			return o, errors.New("文件扩展名格式无效")
		}
		o.IncludeExtensions[i] = "." + strings.TrimPrefix(ext, ".")
	}
	for _, pattern := range o.ExcludePatterns {
		if len(pattern) == 0 || len(pattern) > 512 {
			return o, errors.New("排除规则长度必须在 1 到 512 字符之间")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return o, fmt.Errorf("排除规则无效 %q: %w", pattern, err)
		}
	}
	return o, nil
}

func excluded(relative, name string, o model.LocalDirectoryConfig) bool {
	for _, pattern := range o.ExcludePatterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
		if matched, _ := path.Match(pattern, relative); matched {
			return true
		}
	}
	return false
}

func selected(name string, directory bool, o model.LocalDirectoryConfig) bool {
	if directory {
		return o.ItemType == "directory" || o.ItemType == "all"
	}
	if o.ItemType == "directory" {
		return false
	}
	if len(o.IncludeExtensions) == 0 {
		return true
	}
	for _, ext := range o.IncludeExtensions {
		if strings.EqualFold(path.Ext(name), ext) {
			return true
		}
	}
	return false
}

func hasEvent(o model.LocalDirectoryConfig, event string) bool {
	for _, value := range o.Events {
		if value == event {
			return true
		}
	}
	return false
}
