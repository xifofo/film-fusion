package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EmbyWatchService 观看记录业务：
//   - 配置「被统计」的 Emby 用户；
//   - 历史回填（调用 Emby API 的已观看 UserData）；
//   - 实时入库（由 Emby 播放 webhook 触发）；
//   - 统计查询（记录列表 / 日历 / 总览 / 年度报告）。
type EmbyWatchService struct {
	cfg  *config.Config
	log  *logger.Logger
	db   *gorm.DB
	emby *embyhelper.EmbyClient

	backfillMu sync.Mutex
	progress   map[string]*BackfillProgress
}

// BackfillProgress 单个用户的历史回填进度（内存态，供前端轮询展示）。
type BackfillProgress struct {
	Running    bool       `json:"running"`
	Scanned    int        `json:"scanned"`
	Inserted   int        `json:"inserted"`
	Total      int        `json:"total"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
}

// NewEmbyWatchService 构造
func NewEmbyWatchService(cfg *config.Config, log *logger.Logger, emby *embyhelper.EmbyClient) *EmbyWatchService {
	return &EmbyWatchService{
		cfg:      cfg,
		log:      log,
		db:       database.GetDB(),
		emby:     emby,
		progress: make(map[string]*BackfillProgress),
	}
}

// ---------------- 用户配置 ----------------

// WatchUserView Emby 用户 + 是否被统计 + 回填状态（供前端配置与切换）。
type WatchUserView struct {
	EmbyUserID     string     `json:"emby_user_id"`
	EmbyUserName   string     `json:"emby_user_name"`
	Tracked        bool       `json:"tracked"`
	Backfilling    bool       `json:"backfilling"`
	LastBackfillAt *time.Time `json:"last_backfill_at"`
	RecordCount    int64      `json:"record_count"`
}

// ListUsersWithFlag 合并 Emby 用户列表与本地「被统计」配置，返回带状态的视图。
func (s *EmbyWatchService) ListUsersWithFlag() ([]WatchUserView, error) {
	embyUsers, err := s.emby.ListUsers()
	if err != nil {
		return nil, err
	}

	var tracked []model.EmbyWatchUser
	if err := s.db.Find(&tracked).Error; err != nil {
		return nil, err
	}
	trackedMap := make(map[string]model.EmbyWatchUser, len(tracked))
	for _, t := range tracked {
		trackedMap[t.EmbyUserID] = t
	}

	out := make([]WatchUserView, 0, len(embyUsers))
	for _, u := range embyUsers {
		view := WatchUserView{
			EmbyUserID:   u.ID,
			EmbyUserName: u.Name,
			Backfilling:  s.isBackfilling(u.ID),
		}
		if t, ok := trackedMap[u.ID]; ok {
			view.Tracked = t.Enabled
			view.LastBackfillAt = t.LastBackfillAt
			view.EmbyUserName = firstNonEmpty(u.Name, t.EmbyUserName)
		}
		s.db.Model(&model.EmbyWatchRecord{}).Where("emby_user_id = ?", u.ID).Count(&view.RecordCount)
		out = append(out, view)
	}
	return out, nil
}

// SaveTrackedUsers 用所选 Emby 用户ID集合覆盖「被统计」配置（不在集合内的标记为禁用）。
func (s *EmbyWatchService) SaveTrackedUsers(userIDs []string) error {
	embyUsers, err := s.emby.ListUsers()
	if err != nil {
		return err
	}
	nameMap := make(map[string]string, len(embyUsers))
	for _, u := range embyUsers {
		nameMap[u.ID] = u.Name
	}

	selected := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			selected[id] = true
		}
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 先把所有现有配置禁用，再启用所选（保留 last_backfill_at 等历史字段）
		if err := tx.Model(&model.EmbyWatchUser{}).Where("1 = 1").Update("enabled", false).Error; err != nil {
			return err
		}
		for id := range selected {
			row := model.EmbyWatchUser{EmbyUserID: id, EmbyUserName: nameMap[id], Enabled: true}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "emby_user_id"}},
				DoUpdates: clause.Assignments(map[string]any{"enabled": true, "emby_user_name": nameMap[id]}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetTrackedUser 取被统计用户配置（enabled=true 才算）。
func (s *EmbyWatchService) GetTrackedUser(embyUserID string) (*model.EmbyWatchUser, bool) {
	embyUserID = strings.TrimSpace(embyUserID)
	if embyUserID == "" {
		return nil, false
	}
	var u model.EmbyWatchUser
	if err := s.db.Where("emby_user_id = ? AND enabled = ?", embyUserID, true).First(&u).Error; err != nil {
		return nil, false
	}
	return &u, true
}

// ---------------- 采集规则设置 ----------------

// GetSetting 取采集规则设置（单例，不存在则按默认值创建）。
func (s *EmbyWatchService) GetSetting() (*model.EmbyWatchSetting, error) {
	var st model.EmbyWatchSetting
	err := s.db.First(&st, model.EmbyWatchSettingID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = model.EmbyWatchSetting{
			ID:                  model.EmbyWatchSettingID,
			CompletionThreshold: 0.9,
			CountPlaybackStop:   true,
			CountMarkPlayed:     true,
		}
		if cerr := s.db.Create(&st).Error; cerr != nil {
			return nil, cerr
		}
		return &st, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpdateSetting 更新采集规则设置（阈值会被夹到 0-1）。
func (s *EmbyWatchService) UpdateSetting(in model.EmbyWatchSetting) (*model.EmbyWatchSetting, error) {
	st, err := s.GetSetting()
	if err != nil {
		return nil, err
	}
	threshold := in.CompletionThreshold
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 1 {
		threshold = 1
	}
	st.CompletionThreshold = threshold
	st.CountPlaybackStop = in.CountPlaybackStop
	st.CountMarkPlayed = in.CountMarkPlayed
	if err := s.db.Save(st).Error; err != nil {
		return nil, err
	}
	return st, nil
}

// ---------------- 图片代理 ----------------

// ItemImage 通过后端代理拉取 Emby 条目图片（前端 <img> 无法携带 JWT 头，改由后端代理）。
func (s *EmbyWatchService) ItemImage(itemID, imageType string, maxWidth int) ([]byte, string, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, "", fmt.Errorf("item_id 不能为空")
	}
	if strings.TrimSpace(imageType) == "" {
		imageType = "Primary"
	}
	if maxWidth <= 0 {
		maxWidth = 300
	}
	return s.emby.DownloadImage(itemID, imageType, maxWidth)
}

// ---------------- 历史回填 ----------------

func (s *EmbyWatchService) isBackfilling(embyUserID string) bool {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	p := s.progress[embyUserID]
	return p != nil && p.Running
}

// BackfillStatus 返回某用户的回填进度快照（无记录时返回空进度）。
func (s *EmbyWatchService) BackfillStatus(embyUserID string) *BackfillProgress {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if p := s.progress[strings.TrimSpace(embyUserID)]; p != nil {
		cp := *p
		return &cp
	}
	return &BackfillProgress{}
}

func (s *EmbyWatchService) updateProgress(embyUserID string, fn func(p *BackfillProgress)) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if p := s.progress[embyUserID]; p != nil {
		fn(p)
	}
}

// BackfillUser 异步回填某 Emby 用户的历史观看记录（按 UserData.LastPlayedDate 落日期）。
func (s *EmbyWatchService) BackfillUser(embyUserID string) error {
	embyUserID = strings.TrimSpace(embyUserID)
	if embyUserID == "" {
		return fmt.Errorf("emby_user_id 不能为空")
	}

	s.backfillMu.Lock()
	if p := s.progress[embyUserID]; p != nil && p.Running {
		s.backfillMu.Unlock()
		return fmt.Errorf("该用户正在回填中")
	}
	now := time.Now()
	s.progress[embyUserID] = &BackfillProgress{Running: true, StartedAt: &now}
	s.backfillMu.Unlock()

	var userName string
	if t, ok := s.GetTrackedUser(embyUserID); ok {
		userName = t.EmbyUserName
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.updateProgress(embyUserID, func(p *BackfillProgress) {
					p.Running = false
					p.Error = fmt.Sprintf("panic: %v", r)
					fin := time.Now()
					p.FinishedAt = &fin
				})
				s.log.Errorf("[emby-watch] 回填 panic: %v", r)
			}
		}()

		s.log.Infof("[emby-watch] 历史回填开始: user=%s", embyUserID)
		inserted, scanned, err := s.runBackfill(embyUserID, userName)
		fin := time.Now()
		s.updateProgress(embyUserID, func(p *BackfillProgress) {
			p.Running = false
			p.FinishedAt = &fin
			p.Inserted = inserted
			p.Scanned = scanned
			if err != nil {
				p.Error = err.Error()
			}
		})
		if err != nil {
			s.log.Warnf("[emby-watch] 历史回填失败: user=%s err=%v", embyUserID, err)
			return
		}
		_ = s.db.Model(&model.EmbyWatchUser{}).Where("emby_user_id = ?", embyUserID).Update("last_backfill_at", &fin).Error
		s.log.Infof("[emby-watch] 历史回填完成: user=%s 扫描=%d 新增=%d", embyUserID, scanned, inserted)
	}()
	return nil
}

func (s *EmbyWatchService) runBackfill(embyUserID, userName string) (insertedCount int, scannedCount int, err error) {
	const pageLimit = 200
	startIndex := 0
	skippedCount := 0   // 非 Movie/Episode 跳过数
	fallbackCount := 0  // 缺少 LastPlayedDate、回退日期数
	for page := 0; page < 2000; page++ {
		items, total, listErr := s.emby.ListPlayedItems(embyUserID, startIndex, pageLimit)
		if listErr != nil {
			return insertedCount, scannedCount, listErr
		}
		if total > 0 {
			s.updateProgress(embyUserID, func(p *BackfillProgress) { p.Total = total })
		}
		if len(items) == 0 {
			break
		}
		batch := make([]model.EmbyWatchRecord, 0, len(items))
		for _, it := range items {
			scannedCount++
			rec, ok := s.buildRecordFromPlayed(embyUserID, userName, it)
			if !ok {
				skippedCount++
				continue
			}
			if _, dok := parseEmbyTime(it.UserData.LastPlayedDate); !dok {
				fallbackCount++
			}
			batch = append(batch, rec)
		}
		if len(batch) > 0 {
			res := s.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(batch, 200)
			if res.Error != nil {
				return insertedCount, scannedCount, res.Error
			}
			insertedCount += int(res.RowsAffected)
		}
		curScanned, curInserted := scannedCount, insertedCount
		s.updateProgress(embyUserID, func(p *BackfillProgress) {
			p.Scanned = curScanned
			p.Inserted = curInserted
		})
		startIndex += len(items)
		if total > 0 && startIndex >= total {
			break
		}
	}
	if skippedCount > 0 {
		s.log.Warnf("[emby-watch] 回填跳过 %d 条(非 Movie/Episode) user=%s", skippedCount, embyUserID)
	}
	if fallbackCount > 0 {
		s.log.Infof("[emby-watch] 回填中 %d 条缺少 LastPlayedDate，已回退 DateCreated/当前时间 user=%s", fallbackCount, embyUserID)
	}
	return insertedCount, scannedCount, nil
}

// buildRecordFromPlayed 由 Emby 已观看条目构造一条观看记录。
// 观看时间优先用 LastPlayedDate；缺失时回退 DateCreated，再回退当前时间，避免漏计。
// 仅 Movie/Episode 入库，其它类型返回 ok=false。
func (s *EmbyWatchService) buildRecordFromPlayed(embyUserID, userName string, it embyhelper.PlayedItem) (model.EmbyWatchRecord, bool) {
	itemType := strings.TrimSpace(it.Type)
	if itemType != "Movie" && itemType != "Episode" {
		return model.EmbyWatchRecord{}, false
	}
	watchedAt, ok := parseEmbyTime(it.UserData.LastPlayedDate)
	if !ok {
		if t, ok2 := parseEmbyTime(it.DateCreated); ok2 {
			watchedAt = t
		} else {
			watchedAt = time.Now()
		}
	}
	local := watchedAt.Local()
	rec := model.EmbyWatchRecord{
		EmbyUserID:     embyUserID,
		EmbyUserName:   userName,
		ItemID:         it.ID,
		ItemType:       itemType,
		Title:          it.Name,
		ProductionYear: it.ProductionYear,
		RuntimeMinutes: embyhelper.RuntimeMinutesFromTicks(it.RunTimeTicks),
		WatchedAt:      local,
		WatchedDate:    local.Format("2006-01-02"),
		Source:         model.WatchSourceBackfill,
	}
	if itemType == "Episode" {
		rec.SeriesID = it.SeriesID
		rec.SeriesName = it.SeriesName
		rec.SeasonNumber = it.ParentIndexNumber
		rec.EpisodeNumber = it.IndexNumber
	}
	return rec, true
}

// ---------------- 实时入库（webhook） ----------------

// WatchEventInput 由 webhook 播放事件解析出的入库数据。
type WatchEventInput struct {
	EmbyUserID     string
	EmbyUserName   string
	ItemID         string
	ItemType       string
	Title          string
	SeriesID       string
	SeriesName     string
	SeasonNumber   *int
	EpisodeNumber  *int
	ProductionYear int
	RuntimeMinutes int
	WatchedAt      time.Time
}

// RecordWatch 写入一条观看记录（webhook 来源）。仅处理 Movie/Episode；
// 命中唯一键(用户+条目+日期)时静默忽略，避免重复计数。
func (s *EmbyWatchService) RecordWatch(in WatchEventInput) error {
	itemType := strings.TrimSpace(in.ItemType)
	if itemType != "Movie" && itemType != "Episode" {
		return nil
	}
	if strings.TrimSpace(in.ItemID) == "" || strings.TrimSpace(in.EmbyUserID) == "" {
		return nil
	}
	watchedAt := in.WatchedAt
	if watchedAt.IsZero() {
		watchedAt = time.Now()
	}
	local := watchedAt.Local()
	rec := model.EmbyWatchRecord{
		EmbyUserID:     in.EmbyUserID,
		EmbyUserName:   in.EmbyUserName,
		ItemID:         in.ItemID,
		ItemType:       itemType,
		Title:          in.Title,
		ProductionYear: in.ProductionYear,
		RuntimeMinutes: in.RuntimeMinutes,
		WatchedAt:      local,
		WatchedDate:    local.Format("2006-01-02"),
		Source:         model.WatchSourceWebhook,
	}
	if itemType == "Episode" {
		rec.SeriesID = in.SeriesID
		rec.SeriesName = in.SeriesName
		rec.SeasonNumber = in.SeasonNumber
		rec.EpisodeNumber = in.EpisodeNumber
	}
	res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		s.log.Infof("[emby-watch] 记录观看: user=%s type=%s title=%s date=%s", in.EmbyUserID, itemType, in.Title, rec.WatchedDate)
	}
	return nil
}

// ---------------- 统计查询 ----------------

// RecordListParams 记录列表查询参数
type RecordListParams struct {
	EmbyUserID string
	ItemType   string
	SeriesID   string
	StartDate  string
	EndDate    string
	Page       int
	PageSize   int
}

// RecordListResult 记录列表结果
type RecordListResult struct {
	List  []model.EmbyWatchRecord `json:"list"`
	Total int64                   `json:"total"`
}

// ListRecords 分页查询观看记录
func (s *EmbyWatchService) ListRecords(p RecordListParams) (*RecordListResult, error) {
	if strings.TrimSpace(p.EmbyUserID) == "" {
		return nil, fmt.Errorf("emby_user_id 不能为空")
	}
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PageSize <= 0 {
		p.PageSize = 20
	}

	q := s.db.Model(&model.EmbyWatchRecord{}).Where("emby_user_id = ?", p.EmbyUserID)
	if t := strings.TrimSpace(p.ItemType); t != "" {
		q = q.Where("item_type = ?", t)
	}
	if sid := strings.TrimSpace(p.SeriesID); sid != "" {
		q = q.Where("series_id = ?", sid)
	}
	if d := strings.TrimSpace(p.StartDate); d != "" {
		q = q.Where("watched_date >= ?", d)
	}
	if d := strings.TrimSpace(p.EndDate); d != "" {
		q = q.Where("watched_date <= ?", d)
	}

	var total int64
	q.Count(&total)

	var list []model.EmbyWatchRecord
	if err := q.Order("watched_at desc, id desc").
		Offset((p.Page - 1) * p.PageSize).
		Limit(p.PageSize).
		Find(&list).Error; err != nil {
		return nil, err
	}
	return &RecordListResult{List: list, Total: total}, nil
}

// DeleteRecord 删除单条观看记录；传 embyUserID 时限定到该用户，避免跨用户误删。
func (s *EmbyWatchService) DeleteRecord(id uint, embyUserID string) (int64, error) {
	if id == 0 {
		return 0, fmt.Errorf("id 不能为空")
	}
	q := s.db.Where("id = ?", id)
	if u := strings.TrimSpace(embyUserID); u != "" {
		q = q.Where("emby_user_id = ?", u)
	}
	res := q.Delete(&model.EmbyWatchRecord{})
	return res.RowsAffected, res.Error
}

// ClearRecords 清空某用户的全部观看记录。
func (s *EmbyWatchService) ClearRecords(embyUserID string) (int64, error) {
	embyUserID = strings.TrimSpace(embyUserID)
	if embyUserID == "" {
		return 0, fmt.Errorf("emby_user_id 不能为空")
	}
	res := s.db.Where("emby_user_id = ?", embyUserID).Delete(&model.EmbyWatchRecord{})
	return res.RowsAffected, res.Error
}

// ---------------- 画廊（海报墙）----------------

// GalleryParams 画廊查询参数
type GalleryParams struct {
	EmbyUserID string
	ItemType   string // Movie / Episode
	Year       int
	Page       int
	PageSize   int
}

// GalleryCard 画廊卡片：电影按「片」聚合、剧集按「剧」聚合。
type GalleryCard struct {
	ID          string `json:"id"`           // item_id(电影) 或 series_id(剧集)
	PosterID    string `json:"poster_id"`    // 用于图片代理的条目ID
	Title       string `json:"title"`        // 片名 / 剧名
	Year        int    `json:"year"`         // 出品年份
	Count       int    `json:"count"`        // 电影:观看次数; 剧集:看过集数
	LastWatched string `json:"last_watched"` // 最近观看日期 YYYY-MM-DD
	ItemType    string `json:"item_type"`    // Movie / Episode
	EmbyURL     string `json:"emby_url"`     // 在 Emby Web 中打开该条目的地址
}

// GalleryResult 画廊结果
type GalleryResult struct {
	List  []GalleryCard `json:"list"`
	Total int64         `json:"total"`
	Years []int         `json:"years"`
}

// Gallery 海报墙：电影按 item_id 去重、剧集按 series_id 去重，按最近观看排序。
func (s *EmbyWatchService) Gallery(p GalleryParams) (*GalleryResult, error) {
	if strings.TrimSpace(p.EmbyUserID) == "" {
		return nil, fmt.Errorf("emby_user_id 不能为空")
	}
	itemType := strings.TrimSpace(p.ItemType)
	if itemType != "Movie" && itemType != "Episode" {
		itemType = "Movie"
	}
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PageSize <= 0 {
		p.PageSize = 24
	}

	scope := func() *gorm.DB {
		q := s.db.Model(&model.EmbyWatchRecord{}).
			Where("emby_user_id = ? AND item_type = ?", p.EmbyUserID, itemType)
		if p.Year > 0 {
			q = q.Where("watched_date LIKE ?", fmt.Sprintf("%04d-%%", p.Year))
		}
		if itemType == "Episode" {
			q = q.Where("series_id != ?", "")
		}
		return q
	}

	out := &GalleryResult{Years: s.listYears(p.EmbyUserID)}

	groupCol := "item_id"
	if itemType == "Episode" {
		groupCol = "series_id"
	}
	scope().Distinct(groupCol).Count(&out.Total)

	type galleryRow struct {
		ID              string
		PosterID        string
		Title           string
		Year            int
		Count           int
		LastWatchedDate string
	}
	var rows []galleryRow
	selectExpr := "item_id as id, item_id as poster_id, MAX(title) as title, " +
		"MAX(production_year) as year, COUNT(*) as count, MAX(watched_date) as last_watched_date"
	if itemType == "Episode" {
		selectExpr = "series_id as id, series_id as poster_id, MAX(series_name) as title, " +
			"MAX(production_year) as year, COUNT(DISTINCT item_id) as count, MAX(watched_date) as last_watched_date"
	}
	if err := scope().
		Select(selectExpr).
		Group(groupCol).
		Order("last_watched_date desc").
		Offset((p.Page - 1) * p.PageSize).
		Limit(p.PageSize).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	cards := make([]GalleryCard, 0, len(rows))
	for _, r := range rows {
		cards = append(cards, GalleryCard{
			ID:          r.ID,
			PosterID:    r.PosterID,
			Title:       r.Title,
			Year:        r.Year,
			Count:       r.Count,
			LastWatched: r.LastWatchedDate,
			ItemType:    itemType,
			EmbyURL:     s.emby.WebItemURL(r.ID),
		})
	}
	out.List = cards
	return out, nil
}

// CalendarDay 日历某天的聚合
type CalendarDay struct {
	Date         string `json:"date"`
	Total        int    `json:"total"`
	MovieCount   int    `json:"movie_count"`
	EpisodeCount int    `json:"episode_count"`
}

// Calendar 返回某年某月逐日的观看聚合（仅有记录的天）。
func (s *EmbyWatchService) Calendar(embyUserID string, year, month int) ([]CalendarDay, error) {
	if strings.TrimSpace(embyUserID) == "" {
		return nil, fmt.Errorf("emby_user_id 不能为空")
	}
	if year <= 0 || month <= 0 || month > 12 {
		now := time.Now()
		year, month = now.Year(), int(now.Month())
	}
	prefix := fmt.Sprintf("%04d-%02d-%%", year, month)

	var rows []CalendarDay
	err := s.db.Model(&model.EmbyWatchRecord{}).
		Select("watched_date as date, COUNT(*) as total, "+
			"SUM(CASE WHEN item_type = 'Movie' THEN 1 ELSE 0 END) as movie_count, "+
			"SUM(CASE WHEN item_type = 'Episode' THEN 1 ELSE 0 END) as episode_count").
		Where("emby_user_id = ? AND watched_date LIKE ?", embyUserID, prefix).
		Group("watched_date").
		Order("watched_date asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// TopSeriesItem Top 剧集项
type TopSeriesItem struct {
	SeriesID     string `json:"series_id"`
	SeriesName   string `json:"series_name"`
	EpisodeCount int    `json:"episode_count"`
}

// MonthCount 月度计数
type MonthCount struct {
	Month string `json:"month"` // YYYY-MM
	Total int    `json:"total"`
}

// WatchSummary 总览统计
type WatchSummary struct {
	Year         int             `json:"year"` // 0 表示全部
	MovieCount   int64           `json:"movie_count"`
	EpisodeCount int64           `json:"episode_count"`
	SeriesCount  int64           `json:"series_count"`
	TotalMinutes int64           `json:"total_minutes"`
	ActiveDays   int64           `json:"active_days"`
	TopSeries    []TopSeriesItem `json:"top_series"`
	Monthly      []MonthCount    `json:"monthly"`
	Years        []int           `json:"years"`
}

func (s *EmbyWatchService) baseScope(embyUserID string, year int) *gorm.DB {
	q := s.db.Model(&model.EmbyWatchRecord{}).Where("emby_user_id = ?", embyUserID)
	if year > 0 {
		q = q.Where("watched_date LIKE ?", fmt.Sprintf("%04d-%%", year))
	}
	return q
}

// Summary 总览统计（year=0 表示全部）。
func (s *EmbyWatchService) Summary(embyUserID string, year int) (*WatchSummary, error) {
	if strings.TrimSpace(embyUserID) == "" {
		return nil, fmt.Errorf("emby_user_id 不能为空")
	}
	out := &WatchSummary{Year: year}

	s.baseScope(embyUserID, year).Where("item_type = ?", "Movie").Count(&out.MovieCount)
	s.baseScope(embyUserID, year).Where("item_type = ?", "Episode").Count(&out.EpisodeCount)
	s.baseScope(embyUserID, year).Where("item_type = ? AND series_id != ?", "Episode", "").
		Distinct("series_id").Count(&out.SeriesCount)

	var minutes struct{ Total int64 }
	s.baseScope(embyUserID, year).Select("COALESCE(SUM(runtime_minutes),0) as total").Scan(&minutes)
	out.TotalMinutes = minutes.Total

	s.baseScope(embyUserID, year).Distinct("watched_date").Count(&out.ActiveDays)

	// Top 剧集（按观看集数）
	var tops []TopSeriesItem
	s.baseScope(embyUserID, year).
		Select("series_id, series_name, COUNT(*) as episode_count").
		Where("item_type = ? AND series_id != ?", "Episode", "").
		Group("series_id, series_name").
		Order("episode_count desc").
		Limit(10).
		Scan(&tops)
	out.TopSeries = tops

	// 月度
	out.Monthly = s.monthly(embyUserID, year)

	// 可选年份列表
	out.Years = s.listYears(embyUserID)
	return out, nil
}

func (s *EmbyWatchService) monthly(embyUserID string, year int) []MonthCount {
	var rows []MonthCount
	s.baseScope(embyUserID, year).
		Select("substr(watched_date,1,7) as month, COUNT(*) as total").
		Group("month").
		Order("month asc").
		Scan(&rows)
	return rows
}

func (s *EmbyWatchService) listYears(embyUserID string) []int {
	var rows []struct{ Y string }
	s.db.Model(&model.EmbyWatchRecord{}).
		Where("emby_user_id = ?", embyUserID).
		Select("substr(watched_date,1,4) as y").
		Group("y").
		Order("y desc").
		Scan(&rows)
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		var n int
		if _, err := fmt.Sscanf(r.Y, "%d", &n); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// WeekdayCount 星期分布（0=周日 ... 6=周六）
type WeekdayCount struct {
	Weekday int `json:"weekday"`
	Total   int `json:"total"`
}

// DayCount 某天计数
type DayCount struct {
	Date  string `json:"date"`
	Total int    `json:"total"`
}

// HourCount 时段（0-23 时）计数
type HourCount struct {
	Hour  int `json:"hour"`
	Total int `json:"total"`
}

// AnnualReport 年度报告
type AnnualReport struct {
	Year             int             `json:"year"`
	MovieCount       int64           `json:"movie_count"`
	EpisodeCount     int64           `json:"episode_count"`
	SeriesCount      int64           `json:"series_count"`
	TotalMinutes     int64           `json:"total_minutes"`
	ActiveDays       int64           `json:"active_days"`
	AvgMinutesPerDay int64           `json:"avg_minutes_per_day"` // 活跃日均观看时长
	LongestStreak    int             `json:"longest_streak"`      // 最长连续观看天数
	FirstWatch       string          `json:"first_watch"`
	LastWatch        string          `json:"last_watch"`
	BusiestDay       DayCount        `json:"busiest_day"`
	TopSeries        []TopSeriesItem `json:"top_series"`
	Monthly          []MonthCount    `json:"monthly"`
	Weekday          []WeekdayCount  `json:"weekday"`
	Hourly           []HourCount     `json:"hourly"` // 时段分布(24 桶)
	Years            []int           `json:"years"`
}

// AnnualReport 生成某年的年度报告。
func (s *EmbyWatchService) AnnualReport(embyUserID string, year int) (*AnnualReport, error) {
	if strings.TrimSpace(embyUserID) == "" {
		return nil, fmt.Errorf("emby_user_id 不能为空")
	}
	if year <= 0 {
		year = time.Now().Year()
	}
	out := &AnnualReport{Year: year}

	s.baseScope(embyUserID, year).Where("item_type = ?", "Movie").Count(&out.MovieCount)
	s.baseScope(embyUserID, year).Where("item_type = ?", "Episode").Count(&out.EpisodeCount)
	s.baseScope(embyUserID, year).Where("item_type = ? AND series_id != ?", "Episode", "").
		Distinct("series_id").Count(&out.SeriesCount)

	var minutes struct{ Total int64 }
	s.baseScope(embyUserID, year).Select("COALESCE(SUM(runtime_minutes),0) as total").Scan(&minutes)
	out.TotalMinutes = minutes.Total

	s.baseScope(embyUserID, year).Distinct("watched_date").Count(&out.ActiveDays)

	var firstLast struct {
		First string
		Last  string
	}
	s.baseScope(embyUserID, year).Select("MIN(watched_date) as first, MAX(watched_date) as last").Scan(&firstLast)
	out.FirstWatch = firstLast.First
	out.LastWatch = firstLast.Last

	var busiest DayCount
	s.baseScope(embyUserID, year).
		Select("watched_date as date, COUNT(*) as total").
		Group("watched_date").
		Order("total desc").
		Limit(1).
		Scan(&busiest)
	out.BusiestDay = busiest

	var tops []TopSeriesItem
	s.baseScope(embyUserID, year).
		Select("series_id, series_name, COUNT(*) as episode_count").
		Where("item_type = ? AND series_id != ?", "Episode", "").
		Group("series_id, series_name").
		Order("episode_count desc").
		Limit(10).
		Scan(&tops)
	out.TopSeries = tops

	out.Monthly = s.monthly(embyUserID, year)

	// 星期分布
	var wk []WeekdayCount
	s.baseScope(embyUserID, year).
		Select("CAST(strftime('%w', watched_date) AS INTEGER) as weekday, COUNT(*) as total").
		Group("weekday").
		Order("weekday asc").
		Scan(&wk)
	out.Weekday = wk

	// 活跃日均时长
	if out.ActiveDays > 0 {
		out.AvgMinutesPerDay = out.TotalMinutes / out.ActiveDays
	}
	// 最长连续观看天数
	out.LongestStreak = s.longestStreak(embyUserID, year)
	// 时段分布
	out.Hourly = s.hourly(embyUserID, year)

	out.Years = s.listYears(embyUserID)
	return out, nil
}

// longestStreak 计算最长连续观看天数（按去重后的观看日期相邻判断）。
func (s *EmbyWatchService) longestStreak(embyUserID string, year int) int {
	var rows []struct{ WatchedDate string }
	s.baseScope(embyUserID, year).
		Select("DISTINCT watched_date as watched_date").
		Order("watched_date asc").
		Scan(&rows)
	if len(rows) == 0 {
		return 0
	}
	best, cur := 1, 1
	var prev time.Time
	have := false
	for _, r := range rows {
		d, err := time.Parse("2006-01-02", r.WatchedDate)
		if err != nil {
			continue
		}
		if have {
			diff := d.Sub(prev)
			if diff == 24*time.Hour {
				cur++
			} else if diff > 24*time.Hour {
				cur = 1
			}
			// diff == 0（重复日期）忽略，不改变 cur
		}
		if cur > best {
			best = cur
		}
		prev = d
		have = true
	}
	return best
}

// hourly 时段分布（按本地小时分桶，0-23 各一桶）。
func (s *EmbyWatchService) hourly(embyUserID string, year int) []HourCount {
	var rows []struct{ WatchedAt time.Time }
	s.baseScope(embyUserID, year).
		Select("watched_at").
		Scan(&rows)
	buckets := make([]int, 24)
	for _, r := range rows {
		h := r.WatchedAt.Local().Hour()
		if h >= 0 && h < 24 {
			buckets[h]++
		}
	}
	out := make([]HourCount, 0, 24)
	for h := 0; h < 24; h++ {
		out = append(out, HourCount{Hour: h, Total: buckets[h]})
	}
	return out
}

// AnnualShareImage 生成年度报告分享图（PNG），复用封面字体与 gg 绘制。
func (s *EmbyWatchService) AnnualShareImage(embyUserID string, year int) ([]byte, error) {
	rep, err := s.AnnualReport(embyUserID, year)
	if err != nil {
		return nil, err
	}
	userName := ""
	if t, ok := s.GetTrackedUser(embyUserID); ok {
		userName = firstNonEmpty(t.EmbyUserName, embyUserID)
	} else {
		userName = embyUserID
	}
	// 取年度最常看的剧/片海报作为背景底图（失败则在渲染层回退纯色渐变）
	var hero []byte
	if pid := s.pickSharePosterID(embyUserID, year, rep); pid != "" {
		if data, _, derr := s.emby.DownloadImage(pid, "Primary", 800); derr == nil {
			hero = data
		}
	}
	return renderAnnualShareImage(rep, userName, hero, s.cfg.Emby.Cover.FontCN, s.cfg.Emby.Cover.FontEN)
}

// pickSharePosterID 选一张代表性海报的条目ID：优先年度最爱剧集，其次年度最近看的电影。
func (s *EmbyWatchService) pickSharePosterID(embyUserID string, year int, rep *AnnualReport) string {
	if rep != nil {
		for _, t := range rep.TopSeries {
			if strings.TrimSpace(t.SeriesID) != "" {
				return t.SeriesID
			}
		}
	}
	var r struct{ ItemID string }
	s.baseScope(embyUserID, year).
		Where("item_type = ?", "Movie").
		Select("item_id").
		Order("watched_at desc").
		Limit(1).
		Scan(&r)
	return strings.TrimSpace(r.ItemID)
}

// ---------------- 辅助 ----------------

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// parseEmbyTime 解析 Emby 返回的时间字符串（ISO8601，可能带 7 位小数与 Z）。
func parseEmbyTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.0000000Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
