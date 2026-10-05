package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// ListBlacklist 列出黑名单
func (s *EmbyMissingService) ListBlacklist() ([]model.EmbyMissingBlacklist, error) {
	var out []model.EmbyMissingBlacklist
	if err := s.db.Order("updated_at desc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// AddBlacklist 加入黑名单并从当前快照中移除该剧。
func (s *EmbyMissingService) AddBlacklist(seriesID, seriesName, remark string) (*model.EmbyMissingBlacklist, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, fmt.Errorf("剧集ID不能为空")
	}
	seriesName = strings.TrimSpace(seriesName)
	remark = strings.TrimSpace(remark)

	var existing model.EmbyMissingBlacklist
	err := s.db.Where("series_id = ?", seriesID).First(&existing).Error
	if err == nil {
		existing.SeriesName = seriesName
		existing.Remark = remark
		if err := s.db.Save(&existing).Error; err != nil {
			return nil, err
		}
		s.removeSeriesFromSnapshot(seriesID)
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	b := model.EmbyMissingBlacklist{SeriesID: seriesID, SeriesName: seriesName, Remark: remark}
	if err := s.db.Create(&b).Error; err != nil {
		return nil, err
	}
	s.removeSeriesFromSnapshot(seriesID)
	return &b, nil
}

// RemoveBlacklist 移除黑名单(下次扫描会重新纳入)。
// 同时删除该剧的「最近检查时间」记录，确保下次扫描不会因增量窗口而跳过它。
func (s *EmbyMissingService) RemoveBlacklist(id uint) error {
	var b model.EmbyMissingBlacklist
	if err := s.db.First(&b, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := s.db.Delete(&model.EmbyMissingBlacklist{}, id).Error; err != nil {
		return err
	}
	if sid := strings.TrimSpace(b.SeriesID); sid != "" {
		_ = s.db.Where("series_id = ?", sid).Delete(&model.EmbyMissingSeriesScan{}).Error
	}
	return nil
}

// removeSeriesFromSnapshot 加入黑名单时移除该剧的缺集快照行与最近检查记录。
func (s *EmbyMissingService) removeSeriesFromSnapshot(seriesID string) {
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingSeriesScan{}).Error
}

// pruneStaleBlacklist 清理黑名单中 Emby 已不存在的剧集。
// 仅当 Emby 明确返回"查不到该条目"时才删除对应黑名单项；接口报错(状态未知)或上下文取消时一律保留，避免误删。
// 各条目的存在性校验经传入的信号量 sem 并发执行；失效项统一在收尾后批量删除(避免并发写 SQLite)。
// 返回仍然有效(Emby 中仍存在)的剧集ID集合，供本次缺集扫描跳过。
func (s *EmbyMissingService) pruneStaleBlacklist(ctx context.Context, bl []model.EmbyMissingBlacklist, sem chan struct{}) map[string]bool {
	blackset := make(map[string]bool, len(bl))
	staleNames := make(map[string]string)
	var (
		mu       sync.Mutex
		staleIDs []string
		wg       sync.WaitGroup
	)
	for _, b := range bl {
		seriesID := strings.TrimSpace(b.SeriesID)
		if seriesID == "" {
			continue
		}
		// 上下文取消：剩余项一律按"有效"保留，避免误删
		if ctx.Err() != nil {
			mu.Lock()
			blackset[seriesID] = true
			mu.Unlock()
			continue
		}
		seriesName := b.SeriesName
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 获取信号量(尊重 ctx 取消)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				blackset[seriesID] = true
				mu.Unlock()
				return
			}
			exists, err := s.emby.ItemExists(seriesID)
			<-sem

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// 查询失败，状态未知，保守保留
				s.log.Warnf("[emby-missing] 校验黑名单剧集是否存在失败，保留: series_id=%s err=%v", seriesID, err)
				blackset[seriesID] = true
				return
			}
			if exists {
				blackset[seriesID] = true
				return
			}
			// Emby 已不存在该剧集，登记为待删除(收尾批量删)
			staleIDs = append(staleIDs, seriesID)
			staleNames[seriesID] = seriesName
		}()
	}
	wg.Wait()

	if len(staleIDs) > 0 {
		if err := s.db.Where("series_id IN ?", staleIDs).Delete(&model.EmbyMissingBlacklist{}).Error; err != nil {
			// 批量删除失败：保守把这些项仍按"有效"保留，避免本次扫描重新纳入
			s.log.Warnf("[emby-missing] 批量清理失效黑名单失败，保留: ids=%v err=%v", staleIDs, err)
			for _, id := range staleIDs {
				blackset[id] = true
			}
		} else {
			for _, id := range staleIDs {
				s.log.Infof("[emby-missing] 已清理 Emby 中已不存在的黑名单剧集: series_id=%s name=%s", id, staleNames[id])
			}
		}
	}
	return blackset
}
