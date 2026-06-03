package service

import (
	"context"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"sync"
	"time"
)

// OrganizeLogCleaner 定时清理 organize_logs 超过保留期的数据
type OrganizeLogCleaner struct {
	logger    *logger.Logger
	retention time.Duration
	interval  time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewOrganizeLogCleaner 创建清理器。retention=保留期, interval=扫描间隔。
// 传入 0 时使用默认值（7 天 / 24 小时）。
func NewOrganizeLogCleaner(log *logger.Logger, retention, interval time.Duration) *OrganizeLogCleaner {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &OrganizeLogCleaner{
		logger:    log,
		retention: retention,
		interval:  interval,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start 异步启动清理循环：启动 1 分钟后跑一次，之后每 interval 跑一次
func (c *OrganizeLogCleaner) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		// 启动后延迟一点再首次执行，避免和迁移等抢资源
		select {
		case <-time.After(time.Minute):
		case <-c.ctx.Done():
			return
		}
		c.runOnce()

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.runOnce()
			}
		}
	}()
	c.logger.Infof("OrganizeLogCleaner 已启动：保留 %s，每 %s 扫描一次", c.retention, c.interval)
}

// Stop 停止清理循环
func (c *OrganizeLogCleaner) Stop() {
	c.cancel()
	c.wg.Wait()
}

// runOnce 执行一次清理
func (c *OrganizeLogCleaner) runOnce() {
	cutoff := time.Now().Add(-c.retention)
	res := database.DB.Where("created_at < ?", cutoff).Delete(&model.OrganizeLog{})
	if res.Error != nil {
		c.logger.Warnf("清理 organize_logs 失败: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		c.logger.Infof("清理 organize_logs 完成：删除 %d 条 (cutoff=%s)", res.RowsAffected, cutoff.Format(time.RFC3339))
	}
}
