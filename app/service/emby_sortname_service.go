package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/utils/embyhelper"
	pyutil "film-fusion/app/utils/pinyin"
)

// 处理范围：顶层条目 + Folder。
// Folder 类型容纳"未被识别为 Movie 实体"的电影目录（典型如 strm 库目录、刮削未完成的目录），
// 在 Emby Web `GroupItemsIntoCollections=true` 视图下，它们也会被算进 /Items/Prefixes 字母索引。
// 不写 SortName 就会以中文 Name 派生，导致字母索引出现汉字。
// 不处理 Studio/Person/Episode/Season（不可写或数量过大）。
var sortNameAllowedTypes = map[string]struct{}{
	"Movie":  {},
	"Series": {},
	"BoxSet": {},
	"Folder": {},
}

const sortNameLockedField = "SortName"

// EmbySortNameService 把 Emby 媒体的 SortName 设置成拼音首字母。
//
// 设计原则：
//   - 远端比较：不维护本地表，每次依据 Emby 现状判断是否需要写
//   - 幂等：写入后把 LockedFields 加上 "SortName"，下次直接跳过
//   - 永不覆盖用户改动：远端 ForcedSortName 已非空且与目标不一致 → 跳过
//   - 单飞：同一 itemID 并发请求合并，backfill 全局互斥
//   - 后台执行：StartBackfill 立即返回 job 信息，goroutine 后跑，前端轮询查进度
type EmbySortNameService struct {
	cfg  *config.Config
	log  *logger.Logger
	emby *embyhelper.EmbyClient

	inflight sync.Map // itemID → *sync.Once（避免短时重复处理）

	jobMu      sync.Mutex
	currentJob *BackfillJob // 当前/最近一次任务（保留最后一次结果）
}

// NewEmbySortNameService 构造
func NewEmbySortNameService(cfg *config.Config, log *logger.Logger, emby *embyhelper.EmbyClient) *EmbySortNameService {
	return &EmbySortNameService{cfg: cfg, log: log, emby: emby}
}

// BackfillJob 一次 backfill 的状态快照。指针字段用值传递，前端只读。
type BackfillJob struct {
	ID         string     `json:"id"`
	LibraryIDs []string   `json:"library_ids"`
	Force      bool       `json:"force"` // true 时忽略 LockedFields 锁定状态强制覆盖
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Running    bool       `json:"running"`

	Total   int `json:"total"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`

	// SkipReasons 统计各 skip 原因数量，便于诊断"为什么全跳过"
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`

	ErrorMsg string `json:"error_msg,omitempty"`
}

// ProcessResult 单个 Item 的处理结果，用于 backfill 汇总。
type ProcessResult struct {
	ItemID  string
	Name    string
	Action  string // "updated" / "skipped" / "error"
	Reason  string
	NewSort string
	Err     error
}

// ProcessItem 处理单个 Emby Item（webhook / backfill 共用入口）。
// force=true 时忽略 LockedFields 锁定状态强制覆盖。webhook 应使用 force=false。
func (s *EmbySortNameService) ProcessItem(ctx context.Context, itemID string, force bool) ProcessResult {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return ProcessResult{Action: "skipped", Reason: "itemID 为空"}
	}

	// 短期内同一 itemID 合并：用 LoadOrStore 抢锁
	once, loaded := s.inflight.LoadOrStore(itemID, &sync.Once{})
	if loaded {
		return ProcessResult{ItemID: itemID, Action: "skipped", Reason: "处理中"}
	}
	defer s.inflight.Delete(itemID)

	var result ProcessResult
	once.(*sync.Once).Do(func() {
		result = s.processItemLocked(ctx, itemID, force)
	})
	return result
}

func (s *EmbySortNameService) processItemLocked(ctx context.Context, itemID string, force bool) ProcessResult {
	if err := ctx.Err(); err != nil {
		return ProcessResult{ItemID: itemID, Action: "error", Err: err}
	}

	detail, err := s.emby.GetItemDetail(itemID)
	if err != nil {
		return ProcessResult{ItemID: itemID, Action: "error", Err: fmt.Errorf("拉取 Item 详情失败: %w", err)}
	}

	itemType, _ := detail["Type"].(string)
	name, _ := detail["Name"].(string)
	res := ProcessResult{ItemID: itemID, Name: name}

	if _, ok := sortNameAllowedTypes[itemType]; !ok {
		res.Action = "skipped"
		res.Reason = "类型不在处理范围: " + itemType
		return res
	}
	if strings.TrimSpace(name) == "" {
		res.Action = "skipped"
		res.Reason = "Name 为空"
		return res
	}

	newSort := pyutil.ToSortName(name)
	res.NewSort = newSort

	currentForced, _ := detail["ForcedSortName"].(string)
	currentSort, _ := detail["SortName"].(string)
	locked := extractLockedFields(detail["LockedFields"])

	// 用户显式锁定过 SortName → 视为明确不要动（force 模式忽略）
	if !force && containsField(locked, sortNameLockedField) {
		res.Action = "skipped"
		res.Reason = "SortName 字段已锁定"
		return res
	}
	// 已是目标值 → 幂等（force 模式跳过此短路：Emby 详情接口的 ForcedSortName
	// 字段在某些版本上不可信，可能塞的是 SortName 派生值，导致误判已匹配。
	// force 模式硬写一遍，写后回读才能拿到真实状态。）
	if !force && currentForced == newSort {
		res.Action = "skipped"
		res.Reason = "ForcedSortName 已匹配"
		return res
	}
	// 把当前状态打出来，便于诊断 Emby 字段语义
	s.log.Debugf("[emby-sortname] 准备写入 itemID=%s name=%q ForcedSortName=%q SortName=%q locked=%v target=%q",
		itemID, name, currentForced, currentSort, locked, newSort)
	// 其它情况一律覆盖：未锁定的非空 ForcedSortName 通常是刮削工具或 Emby 派生值，
	// 用户主动触发 backfill 就是要换成拼音首字母。写入后会锁定字段，下次自动跳过。

	// 写回：同时显式写 SortName 和 ForcedSortName 并锁定 SortName 字段。
	//   - ForcedSortName: 用户覆盖字段（数据来源真相）
	//   - SortName: 数据库实际排序列；Emby 不一定会从 ForcedSortName 自动同步过来，
	//     而 /Items/Prefixes 字母索引接口依赖 SortName，必须显式写入
	detail["ForcedSortName"] = newSort
	detail["SortName"] = newSort
	detail["LockedFields"] = appendField(locked, sortNameLockedField)

	status, respSnippet, err := s.emby.UpdateItem(itemID, detail)
	if err != nil {
		res.Action = "error"
		res.Err = err
		s.log.Warnf("[emby-sortname] update 失败 itemID=%s name=%q HTTP=%d resp=%q err=%v",
			itemID, name, status, respSnippet, err)
		return res
	}

	// 写后立即回读，验证 Emby 是否真的接受了我们的写入
	verify, verr := s.emby.GetItemDetail(itemID)
	if verr != nil {
		s.log.Warnf("[emby-sortname] 写后回读失败 itemID=%s: %v", itemID, verr)
	} else {
		afterForced, _ := verify["ForcedSortName"].(string)
		afterSort, _ := verify["SortName"].(string)
		afterLocked := extractLockedFields(verify["LockedFields"])
		if afterForced == newSort && afterSort == newSort && containsField(afterLocked, sortNameLockedField) {
			s.log.Infof("[emby-sortname] ✓ 已更新 itemID=%s name=%q type=%s sort=%s HTTP=%d",
				itemID, name, itemType, newSort, status)
		} else {
			s.log.Warnf("[emby-sortname] ✗ 字段未完全生效 itemID=%s name=%q HTTP=%d want=%q got_Forced=%q got_Sort=%q locked=%v",
				itemID, name, status, newSort, afterForced, afterSort, afterLocked)
		}
	}

	res.Action = "updated"
	res.Reason = fmt.Sprintf("ForcedSortName=%s (HTTP %d)", newSort, status)
	return res
}

// StartBackfill 启动一次后台 backfill。立刻返回当前任务快照，实际处理在 goroutine。
// libraryIDs 为空表示全库扫描。force=true 忽略锁定状态强制覆盖。
// 已有任务在跑时返回 ErrBackfillRunning。
func (s *EmbySortNameService) StartBackfill(libraryIDs []string, force bool) (BackfillJob, error) {
	s.jobMu.Lock()
	if s.currentJob != nil && s.currentJob.Running {
		running := *s.currentJob
		s.jobMu.Unlock()
		return running, ErrBackfillRunning
	}

	libs := append([]string(nil), libraryIDs...)
	job := &BackfillJob{
		ID:         fmt.Sprintf("sn-%d", time.Now().UnixNano()),
		LibraryIDs: libs,
		Force:      force,
		StartedAt:  time.Now(),
		Running:    true,
	}
	s.currentJob = job
	s.jobMu.Unlock()

	s.log.Infof("[emby-sortname] backfill 启动 jobID=%s force=%v library_ids=%v",
		job.ID, job.Force, job.LibraryIDs)

	go s.runBackfill(job)
	return *job, nil
}

// ErrBackfillRunning 已有 backfill 任务在跑
var ErrBackfillRunning = errors.New("已有 backfill 任务在执行")

// runBackfill 实际执行体，goroutine 中跑，独立于 HTTP 请求生命周期。
func (s *EmbySortNameService) runBackfill(job *BackfillJob) {
	// 单独 30 分钟 ctx，与请求 ctx 解耦：客户端断开 / 页面刷新都不会中断
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			s.log.Errorf("[emby-sortname] backfill panic: %v", r)
			s.finishJob(job, fmt.Sprintf("panic: %v", r))
		}
	}()

	targets := job.LibraryIDs
	if len(targets) == 0 {
		// 全库模式：必须逐库 ParentId 扫，不能省略 ParentId。
		// Emby 的坑：IncludeItemTypes 含 Folder 时，请求不带 ParentId 会返回空（不递归 Folder）。
		// 之前用 targets=[""] 直接省 ParentId 扫全库，导致所有非主动选择的库里的 Folder 全部漏处理。
		libs, err := s.emby.ListLibraries()
		if err != nil {
			s.finishJob(job, fmt.Sprintf("拉取媒体库列表失败: %v", err))
			return
		}
		targets = make([]string, 0, len(libs))
		for _, lib := range libs {
			if lib.ID != "" {
				targets = append(targets, lib.ID)
			}
		}
		s.log.Infof("[emby-sortname] 全库模式展开为 %d 个媒体库: %v", len(targets), targets)
	}

	const pageSize = 200
	for _, libID := range targets {
		startIndex := 0
		for {
			if err := ctx.Err(); err != nil {
				s.finishJob(job, err.Error())
				return
			}
			items, total, err := s.emby.ListItemsForSort(libID, nil, startIndex, pageSize)
			if err != nil {
				msg := fmt.Sprintf("扫描 Emby Items 失败 (libraryID=%q, startIndex=%d): %v", libID, startIndex, err)
				s.log.Warn(msg)
				s.finishJob(job, msg)
				return
			}
			if len(items) == 0 {
				break
			}
			for _, it := range items {
				res := s.processBriefItem(ctx, it, job.Force)
				s.tickJob(job, res)
			}
			startIndex += len(items)
			if startIndex >= total {
				break
			}
		}
	}

	s.finishJob(job, "")
}

// tickJob 把单条结果累加到 job 计数（受 jobMu 保护，前端轮询能拿到实时数据）。
func (s *EmbySortNameService) tickJob(job *BackfillJob, res ProcessResult) {
	s.jobMu.Lock()
	job.Total++
	switch res.Action {
	case "updated":
		job.Updated++
	case "skipped":
		job.Skipped++
		if job.SkipReasons == nil {
			job.SkipReasons = make(map[string]int)
		}
		// 用 reason 第一段做分类（避免高基数键）
		key := skipReasonKey(res.Reason)
		job.SkipReasons[key]++
	case "error":
		job.Errors++
	}
	s.jobMu.Unlock()

	if res.Action == "error" && res.Err != nil {
		s.log.Warnf("[emby-sortname] backfill 错误 itemID=%s: %v", res.ItemID, res.Err)
	}
}

// skipReasonKey 把 reason 归一化成有限的几个分类键
func skipReasonKey(reason string) string {
	switch {
	case strings.Contains(reason, "已锁定"):
		return "locked"
	case strings.Contains(reason, "已匹配"):
		return "already_matched"
	case strings.Contains(reason, "类型不在"):
		return "type_excluded"
	case strings.Contains(reason, "Name 为空"):
		return "empty_name"
	case strings.Contains(reason, "处理中"):
		return "inflight"
	default:
		return "other"
	}
}

// finishJob 标记任务结束（成功或失败），保留最终统计供前端读取。
func (s *EmbySortNameService) finishJob(job *BackfillJob, errMsg string) {
	s.jobMu.Lock()
	now := time.Now()
	job.FinishedAt = &now
	job.Running = false
	job.ErrorMsg = errMsg
	dur := now.Sub(job.StartedAt)
	s.jobMu.Unlock()

	if errMsg != "" {
		s.log.Warnf("[emby-sortname] backfill 终止 jobID=%s 原因=%s 耗时=%s 已处理 total=%d", job.ID, errMsg, dur, job.Total)
		return
	}
	s.log.Infof("[emby-sortname] backfill 完成 jobID=%s force=%v total=%d updated=%d skipped=%d (%v) errors=%d 耗时=%s",
		job.ID, job.Force, job.Total, job.Updated, job.Skipped, job.SkipReasons, job.Errors, dur)

	// 有实际写入时触发一次 Emby 全库刷新，重建 /Items/Prefixes 字母索引等派生缓存。
	// SortName/ForcedSortName 已加锁，刮削器不会覆盖。Refresh 是异步的，Emby 立即返回。
	if job.Updated > 0 {
		if status, _, err := s.emby.RefreshLibrary(); err != nil {
			s.log.Warnf("[emby-sortname] 触发 Emby 库刷新失败 HTTP=%d err=%v（不影响数据，元数据已写入；可手动在 Emby 后台 Library → Refresh Metadata 触发）", status, err)
		} else {
			s.log.Infof("[emby-sortname] 已触发 Emby 全库刷新 HTTP=%d，字母索引等派生缓存将在 Emby 后端线程异步重建（通常需几十秒到几分钟）", status)
		}
	}
}

// processBriefItem 在 backfill 路径上：用列表里的精简字段做快速判断，能跳过就不再 GET 详情。
// force=true 时不看锁定状态；且不信任 list 接口的 ForcedSortName 字段
// （某些 Emby 版本会把 SortName 派生值塞进该字段，导致误判已匹配），
// 强制走详情通道用 GET /Users/{uid}/Items/{id} 的权威值判断。
func (s *EmbySortNameService) processBriefItem(ctx context.Context, it embyhelper.ItemBrief, force bool) ProcessResult {
	if _, ok := sortNameAllowedTypes[it.Type]; !ok {
		return ProcessResult{ItemID: it.ID, Name: it.Name, Action: "skipped", Reason: "类型不在处理范围"}
	}
	if !force && containsField(it.LockedFields, sortNameLockedField) {
		return ProcessResult{ItemID: it.ID, Name: it.Name, Action: "skipped", Reason: "SortName 已锁定"}
	}
	if strings.TrimSpace(it.Name) == "" {
		return ProcessResult{ItemID: it.ID, Action: "skipped", Reason: "Name 为空"}
	}

	// 普通模式：信任 list 字段做快速短路（性能：避免 N 次 GET）
	if !force {
		newSort := pyutil.ToSortName(it.Name)
		if it.ForcedSortName == newSort {
			return ProcessResult{ItemID: it.ID, Name: it.Name, Action: "skipped", Reason: "ForcedSortName 已匹配"}
		}
	}

	// 进入详情通道：GET 拿真实 ForcedSortName → 必要时 POST 写入
	return s.ProcessItem(ctx, it.ID, force)
}

// JobSnapshot 当前/最近任务的只读快照（无任务时返回 nil）。
func (s *EmbySortNameService) JobSnapshot() *BackfillJob {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.currentJob == nil {
		return nil
	}
	cp := *s.currentJob
	// 切片浅拷贝足够，调用方只读
	return &cp
}

// IsBackfillRunning 当前是否有 backfill 在跑
func (s *EmbySortNameService) IsBackfillRunning() bool {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.currentJob != nil && s.currentJob.Running
}

// extractLockedFields 把 ItemDto 里的 LockedFields 字段统一成 []string。
// Emby JSON 反序列化时数组元素是 interface{}，需要逐个断言。
func extractLockedFields(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func containsField(fields []string, target string) bool {
	for _, f := range fields {
		if f == target {
			return true
		}
	}
	return false
}

func appendField(fields []string, target string) []string {
	if containsField(fields, target) {
		return fields
	}
	return append(fields, target)
}
