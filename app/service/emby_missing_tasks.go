package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// ErrEmbyMissingScanInProgress 表示已有全量或单剧缺集扫描正在运行。
var ErrEmbyMissingScanInProgress = errors.New("扫描正在进行中")

// Trigger 异步触发一次扫描(带"正在扫描"互斥)。
func (s *EmbyMissingService) Trigger(opts ScanOptions) error {
	s.scanMu.Lock()
	if s.scanning {
		s.scanMu.Unlock()
		return ErrEmbyMissingScanInProgress
	}
	s.scanning = true
	s.scanMu.Unlock()
	_, _ = s.getOrCreateSetting()
	s.setScanningFlag(true)
	s.setProgress(ScanProgress{Phase: "preparing", Percent: 0})

	go func() {
		defer func() {
			s.scanMu.Lock()
			s.scanning = false
			s.scanMu.Unlock()
			if r := recover(); r != nil {
				s.log.Errorf("[emby-missing] 扫描 panic: %v", r)
				s.finishScan(false, ScanResult{}, fmt.Errorf("panic: %v", r))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.log.Info("[emby-missing] 缺集扫描开始")
		res, err := s.ScanOnce(ctx, opts)
		if err != nil {
			s.log.Warnf("[emby-missing] 缺集扫描失败: %v", err)
			s.finishScan(false, ScanResult{}, err)
			return
		}
		s.log.Infof("[emby-missing] 缺集扫描完成 series=%d missing=%d", res.SeriesCount, res.MissingCount)
		s.finishScan(true, res, nil)
	}()
	return nil
}

// RescanSeries 强制重新检查一部已在缺集快照中的剧，并只替换该剧的快照。
// 它不受 RescanIntervalDays 影响；Emby 查询失败时保留原快照。
func (s *EmbyMissingService) RescanSeries(ctx context.Context, seriesID string, opts ScanOptions) (*SeriesScanResult, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, fmt.Errorf("series_id 不能为空")
	}

	s.scanMu.Lock()
	if s.scanning {
		s.scanMu.Unlock()
		return nil, ErrEmbyMissingScanInProgress
	}
	s.scanning = true
	s.scanMu.Unlock()
	defer func() {
		s.scanMu.Lock()
		s.scanning = false
		s.scanMu.Unlock()
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var existing model.EmbyMissingEpisode
	if err := s.db.Where("series_id = ?", seriesID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("该剧已不在缺集列表中")
		}
		return nil, err
	}

	var blacklistCount int64
	if err := s.db.Model(&model.EmbyMissingBlacklist{}).
		Where("series_id = ?", seriesID).
		Count(&blacklistCount).Error; err != nil {
		return nil, err
	}
	if blacklistCount > 0 {
		return nil, fmt.Errorf("该剧已加入缺集检查黑名单")
	}

	ref := missingSeriesRef{
		seriesID:    existing.SeriesID,
		seriesName:  existing.SeriesName,
		libraryID:   existing.LibraryID,
		libraryName: existing.LibraryName,
	}
	rows, err := s.fetchSeriesMissing(ref, opts)
	if err != nil {
		return nil, fmt.Errorf("重新扫描剧集 %s 失败: %w", existing.SeriesName, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	scanRec := model.EmbyMissingSeriesScan{
		SeriesID:      existing.SeriesID,
		LibraryID:     existing.LibraryID,
		OptionsKey:    buildMissingOptionsKey(opts),
		LastCheckedAt: now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingSeriesScan{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return err
			}
		}
		return tx.Create(&scanRec).Error
	}); err != nil {
		return nil, err
	}

	return &SeriesScanResult{
		SeriesID:     seriesID,
		MissingCount: len(rows),
		Resolved:     len(rows) == 0,
	}, nil
}

// DeleteSeries 删除指定剧的缺集快照和增量检查记录，下次扫描可重新发现。
func (s *EmbyMissingService) DeleteSeries(ctx context.Context, seriesID string) error {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return fmt.Errorf("series_id 不能为空")
	}

	// 与全量/单剧扫描互斥，避免删除后被正在执行的扫描写回。
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanning {
		return ErrEmbyMissingScanInProgress
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error; err != nil {
			return err
		}
		return tx.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingSeriesScan{}).Error
	})
}

// IsScanning 当前是否在扫描
func (s *EmbyMissingService) IsScanning() bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return s.scanning
}

func (s *EmbyMissingService) setProgress(p ScanProgress) {
	s.progMu.Lock()
	s.progress = p
	s.progMu.Unlock()
}

func (s *EmbyMissingService) getProgress() ScanProgress {
	s.progMu.Lock()
	defer s.progMu.Unlock()
	return s.progress
}

func (s *EmbyMissingService) setScanningFlag(scanning bool) {
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Update("scanning", scanning).Error
}

func (s *EmbyMissingService) finishScan(success bool, res ScanResult, scanErr error) {
	now := time.Now()
	updates := map[string]any{
		"scanning":     false,
		"last_scan_at": &now,
	}
	if success {
		updates["last_status"] = "success"
		updates["last_error"] = ""
		updates["last_series_count"] = res.SeriesCount
		updates["last_missing_count"] = res.MissingCount
	} else {
		updates["last_status"] = "failed"
		if scanErr != nil {
			updates["last_error"] = scanErr.Error()
		}
	}
	// 收尾进度态：成功置 done/100，失败标记 failed(保留已有计数供前端展示)
	prog := s.getProgress()
	if success {
		prog.Phase = "done"
		prog.Percent = 100
	} else {
		prog.Phase = "failed"
	}
	s.setProgress(prog)
	_, _ = s.getOrCreateSetting()
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Updates(updates).Error
}
