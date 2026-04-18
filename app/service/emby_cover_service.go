package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/cover"
	"film-fusion/app/utils/embyhelper"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// EmbyCoverService 媒体库封面生成业务编排
//
// 职责：
//   - 同步 Emby 媒体库到本地配置表
//   - 生成单库 / 批量封面
//   - 上传到 Emby
//   - cron 定时批量生成
type EmbyCoverService struct {
	cfg    *config.Config
	log    *logger.Logger
	db     *gorm.DB
	emby   *embyhelper.EmbyClient
	cron   *cron.Cron
	cronID cron.EntryID
	mu     sync.Mutex
	running bool
}

// NewEmbyCoverService 构造
func NewEmbyCoverService(cfg *config.Config, log *logger.Logger, emby *embyhelper.EmbyClient) *EmbyCoverService {
	return &EmbyCoverService{
		cfg:  cfg,
		log:  log,
		db:   database.GetDB(),
		emby: emby,
	}
}

// LibraryView 一个媒体库的合并视图：Emby 元信息 + 本地配置
type LibraryView struct {
	EmbyLibraryID string     `json:"emby_library_id"`
	EmbyName      string     `json:"emby_name"`
	CollectionType string    `json:"collection_type"`
	CNTitle       string     `json:"cn_title"`
	ENSubtitle    string     `json:"en_subtitle"`
	TemplateID    string     `json:"template_id"`
	Enabled       bool       `json:"enabled"`
	LastGeneratedAt *time.Time `json:"last_generated_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Configured    bool       `json:"configured"` // 本地是否已建表项
}

// ListLibraries 列出所有媒体库（合并 Emby + 本地配置）
// 同时会自动 upsert 本地表，让 Emby 新加的库立即可见
func (s *EmbyCoverService) ListLibraries(ctx context.Context) ([]LibraryView, error) {
	libs, err := s.emby.ListLibraries()
	if err != nil {
		return nil, fmt.Errorf("拉取 Emby 媒体库失败: %w", err)
	}

	// 一次拉本地全部配置
	var locals []model.EmbyCoverLibrary
	if err := s.db.Find(&locals).Error; err != nil {
		return nil, fmt.Errorf("查询本地媒体库配置失败: %w", err)
	}
	localMap := make(map[string]*model.EmbyCoverLibrary, len(locals))
	for i := range locals {
		localMap[locals[i].EmbyLibraryID] = &locals[i]
	}

	out := make([]LibraryView, 0, len(libs))
	for _, lib := range libs {
		view := LibraryView{
			EmbyLibraryID:  lib.ID,
			EmbyName:       lib.Name,
			CollectionType: lib.CollectionType,
			TemplateID:     cover.DefaultTemplateID,
			Enabled:        true,
		}
		if local, ok := localMap[lib.ID]; ok {
			view.CNTitle = local.CNTitle
			view.ENSubtitle = local.ENSubtitle
			if local.TemplateID != "" {
				view.TemplateID = local.TemplateID
			}
			view.Enabled = local.Enabled
			view.LastGeneratedAt = local.LastGeneratedAt
			view.LastError = local.LastError
			view.Configured = true
		}
		out = append(out, view)
	}
	return out, nil
}

// UpsertLibraryConfig 创建或更新某个库的配置
func (s *EmbyCoverService) UpsertLibraryConfig(ctx context.Context, in model.EmbyCoverLibrary) (model.EmbyCoverLibrary, error) {
	if strings.TrimSpace(in.EmbyLibraryID) == "" {
		return model.EmbyCoverLibrary{}, errors.New("emby_library_id 不能为空")
	}
	if in.TemplateID == "" {
		in.TemplateID = cover.DefaultTemplateID
	}

	var existing model.EmbyCoverLibrary
	err := s.db.Where("emby_library_id = ?", in.EmbyLibraryID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.db.Create(&in).Error; err != nil {
			return model.EmbyCoverLibrary{}, fmt.Errorf("创建媒体库配置失败: %w", err)
		}
		return in, nil
	} else if err != nil {
		return model.EmbyCoverLibrary{}, fmt.Errorf("查询媒体库配置失败: %w", err)
	}

	existing.EmbyName = in.EmbyName
	existing.CNTitle = in.CNTitle
	existing.ENSubtitle = in.ENSubtitle
	existing.TemplateID = in.TemplateID
	existing.Enabled = in.Enabled
	if err := s.db.Save(&existing).Error; err != nil {
		return model.EmbyCoverLibrary{}, fmt.Errorf("更新媒体库配置失败: %w", err)
	}
	return existing, nil
}

// GenerateOptions 生成封面的运行时选项
type GenerateOptions struct {
	Upload bool // true 则上传到 Emby；false 仅返回字节（预览）
}

// GenerateLibraryCover 为单个媒体库生成封面
// 流程：取本地配置 → 拉最新 N 个海报 → 调模板渲染 → 可选上传
func (s *EmbyCoverService) GenerateLibraryCover(ctx context.Context, embyLibraryID string, opts GenerateOptions) ([]byte, error) {
	if strings.TrimSpace(embyLibraryID) == "" {
		return nil, errors.New("emby_library_id 不能为空")
	}

	// 1) 读本地配置（不存在时按默认值兜底，使用 Emby 库名作为中文主标）
	local, err := s.loadOrFallbackConfig(embyLibraryID)
	if err != nil {
		return nil, err
	}

	// 2) 拉海报
	posterCount := s.cfg.Emby.Cover.PosterCount
	if posterCount <= 0 {
		posterCount = 9
	}
	items, err := s.emby.ListLatestItems(embyLibraryID, posterCount, nil)
	if err != nil {
		return nil, fmt.Errorf("获取最新媒体失败: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("媒体库 %s 下没有 Movie/Series", embyLibraryID)
	}

	posters := make([][]byte, 0, len(items))
	for _, it := range items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, hasPrimary := it.ImageTags["Primary"]; !hasPrimary {
			s.log.Debugf("[emby-cover] item %s (%s) 无 Primary 海报，跳过", it.ID, it.Name)
			continue
		}
		data, _, derr := s.emby.DownloadImage(it.ID, "Primary", 600)
		if derr != nil {
			s.log.Warnf("[emby-cover] 下载海报失败 item=%s err=%v", it.ID, derr)
			continue
		}
		posters = append(posters, data)
	}
	if len(posters) == 0 {
		return nil, errors.New("所有最新媒体的海报都拉取失败")
	}

	// 3) 渲染
	cnTitle := strings.TrimSpace(local.CNTitle)
	if cnTitle == "" {
		cnTitle = local.EmbyName
	}
	in := cover.RenderInput{
		Width:       s.cfg.Emby.Cover.Width,
		Height:      s.cfg.Emby.Cover.Height,
		JPEGQuality: s.cfg.Emby.Cover.JpegQuality,
		CNTitle:     cnTitle,
		ENSubtitle:  local.ENSubtitle,
		Posters:     posters,
		FontCNPath:  s.cfg.Emby.Cover.FontCN,
		FontENPath:  s.cfg.Emby.Cover.FontEN,
	}
	out, err := cover.RenderWithTemplate(ctx, local.TemplateID, in)
	if err != nil {
		s.recordFailure(embyLibraryID, err)
		return nil, fmt.Errorf("渲染封面失败: %w", err)
	}

	// 4) 上传
	if opts.Upload {
		if err := s.emby.UploadPrimaryImage(embyLibraryID, out.JPEG, "image/jpeg"); err != nil {
			s.recordFailure(embyLibraryID, err)
			return out.JPEG, fmt.Errorf("上传 Emby 封面失败: %w", err)
		}
		s.recordSuccess(embyLibraryID)
		s.log.Infof("[emby-cover] 已生成并上传媒体库封面: %s (%s)", local.EmbyName, embyLibraryID)
	}

	return out.JPEG, nil
}

// GenerateAllEnabled 批量为所有 enabled=true 的库生成并上传封面
func (s *EmbyCoverService) GenerateAllEnabled(ctx context.Context) (success, failed int, errs []error) {
	libs, err := s.ListLibraries(ctx)
	if err != nil {
		return 0, 0, []error{err}
	}

	for _, lib := range libs {
		if !lib.Configured || !lib.Enabled {
			continue
		}
		if ctx.Err() != nil {
			return success, failed, append(errs, ctx.Err())
		}
		if _, err := s.GenerateLibraryCover(ctx, lib.EmbyLibraryID, GenerateOptions{Upload: true}); err != nil {
			failed++
			errs = append(errs, fmt.Errorf("[%s] %w", lib.EmbyName, err))
			continue
		}
		success++
	}
	return success, failed, errs
}

// loadOrFallbackConfig 加载本地配置；不存在时返回一个内存兜底（不写库）
func (s *EmbyCoverService) loadOrFallbackConfig(embyLibraryID string) (model.EmbyCoverLibrary, error) {
	var local model.EmbyCoverLibrary
	err := s.db.Where("emby_library_id = ?", embyLibraryID).First(&local).Error
	if err == nil {
		// 兜底 EmbyName / TemplateID
		if local.EmbyName == "" || local.TemplateID == "" {
			libs, lerr := s.emby.ListLibraries()
			if lerr == nil {
				for _, lib := range libs {
					if lib.ID == embyLibraryID {
						local.EmbyName = lib.Name
						break
					}
				}
			}
			if local.TemplateID == "" {
				local.TemplateID = cover.DefaultTemplateID
			}
		}
		return local, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.EmbyCoverLibrary{}, fmt.Errorf("查询本地配置失败: %w", err)
	}
	// 本地无配置：用 Emby 库名兜底
	libs, lerr := s.emby.ListLibraries()
	if lerr != nil {
		return model.EmbyCoverLibrary{}, fmt.Errorf("查不到本地配置且无法获取 Emby 库名: %w", lerr)
	}
	for _, lib := range libs {
		if lib.ID == embyLibraryID {
			return model.EmbyCoverLibrary{
				EmbyLibraryID: lib.ID,
				EmbyName:      lib.Name,
				CNTitle:       lib.Name,
				TemplateID:    cover.DefaultTemplateID,
				Enabled:       true,
			}, nil
		}
	}
	return model.EmbyCoverLibrary{}, fmt.Errorf("Emby 不存在该媒体库: %s", embyLibraryID)
}

func (s *EmbyCoverService) recordSuccess(embyLibraryID string) {
	now := time.Now()
	s.db.Model(&model.EmbyCoverLibrary{}).
		Where("emby_library_id = ?", embyLibraryID).
		Updates(map[string]interface{}{
			"last_generated_at": &now,
			"last_error":        "",
		})
}

func (s *EmbyCoverService) recordFailure(embyLibraryID string, err error) {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	s.db.Model(&model.EmbyCoverLibrary{}).
		Where("emby_library_id = ?", embyLibraryID).
		Update("last_error", msg)
}

// Start 启动 cron 定时任务（如果配置了 cron 表达式）
func (s *EmbyCoverService) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}
	if !s.cfg.Emby.Cover.Enabled {
		s.log.Info("[emby-cover] 功能未启用，跳过 cron 调度")
		return
	}
	expr := strings.TrimSpace(s.cfg.Emby.Cover.Cron)
	if expr == "" {
		s.log.Info("[emby-cover] 未配置 cron，仅支持手动触发")
		return
	}

	c := cron.New(cron.WithSeconds())
	// 兼容 5 段和 6 段 cron：先尝试解析为 5 段
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expr); err == nil {
		c = cron.New() // 5 段
	}
	id, err := c.AddFunc(expr, s.runScheduledJob)
	if err != nil {
		s.log.Errorf("[emby-cover] cron 表达式无效 %q: %v", expr, err)
		return
	}
	c.Start()
	s.cron = c
	s.cronID = id
	s.running = true
	s.log.Infof("[emby-cover] cron 调度已启动: %s", expr)
}

// runScheduledJob cron 触发的批量生成
func (s *EmbyCoverService) runScheduledJob() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s.log.Info("[emby-cover] 定时任务开始执行")
	success, failed, errs := s.GenerateAllEnabled(ctx)
	s.log.Infof("[emby-cover] 定时任务完成: 成功=%d 失败=%d", success, failed)
	for _, e := range errs {
		s.log.Warnf("[emby-cover] 失败明细: %v", e)
	}
}

// Stop 停止 cron
func (s *EmbyCoverService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cron == nil {
		return
	}
	ctx := s.cron.Stop()
	<-ctx.Done()
	s.cron = nil
	s.running = false
	s.log.Info("[emby-cover] cron 调度已停止")
}
