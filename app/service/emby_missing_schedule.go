package service

import (
	"fmt"
	"strings"

	"film-fusion/app/model"

	"github.com/robfig/cron/v3"
)

// Start 启动定时调度(应用启动时调用)。
func (s *EmbyMissingService) Start() {
	// 清除上次异常退出残留的"扫描中"标记
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Update("scanning", false).Error
	s.Reschedule()
}

// Stop 停止定时调度。
func (s *EmbyMissingService) Stop() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
		s.cron = nil
		s.log.Info("[emby-missing] 定时调度已停止")
	}
}

// Reschedule 根据 DB 设置重建 cron 调度。
func (s *EmbyMissingService) Reschedule() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
		s.cron = nil
	}

	st, err := s.getOrCreateSetting()
	if err != nil {
		s.log.Warnf("[emby-missing] 读取设置失败，跳过调度: %v", err)
		return
	}
	if !st.ScheduleEnabled || strings.TrimSpace(st.Cron) == "" {
		s.log.Info("[emby-missing] 定时扫描未启用")
		return
	}

	expr := strings.TrimSpace(st.Cron)
	c := cron.New(cron.WithSeconds())
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, perr := parser.Parse(expr); perr == nil {
		c = cron.New() // 5 段
	}
	if _, aerr := c.AddFunc(expr, s.runScheduledJob); aerr != nil {
		s.log.Errorf("[emby-missing] cron 表达式无效 %q: %v", expr, aerr)
		return
	}
	c.Start()
	s.cron = c
	s.log.Infof("[emby-missing] 定时扫描已启动: %s", expr)
}

func (s *EmbyMissingService) runScheduledJob() {
	st, err := s.getOrCreateSetting()
	if err != nil {
		s.log.Warnf("[emby-missing] 定时任务读取设置失败: %v", err)
		return
	}
	if err := s.Trigger(ScanOptions{
		LibraryID:          st.LibraryID,
		IncludeSpecials:    st.IncludeSpecials,
		IncludeUnaired:     st.IncludeUnaired,
		RescanIntervalDays: st.RescanIntervalDays,
	}); err != nil {
		s.log.Warnf("[emby-missing] 定时任务触发失败: %v", err)
	}
}

// validateCron 校验 cron 表达式(兼容 5 段与 6 段)。
func validateCron(expr string) error {
	expr = strings.TrimSpace(expr)
	parser5 := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser5.Parse(expr); err == nil {
		return nil
	}
	parser6 := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser6.Parse(expr); err == nil {
		return nil
	}
	return fmt.Errorf("无效的 cron 表达式: %s", expr)
}
