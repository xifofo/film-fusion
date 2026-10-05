package service

import (
	"errors"
	"time"

	"film-fusion/app/model"
)

var ErrAutomationScanRunning = errors.New("该自动化正在扫描")

func (s *AutomationService) sourceScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scanDueAutomationSources()
		case <-s.sourceWake:
			s.scanDueAutomationSources()
		}
	}
}

func (s *AutomationService) scanDueAutomationSources() {
	var sources []model.AutomationSource
	if err := s.db.Where("enabled = ?", true).Order("id ASC").Find(&sources).Error; err != nil {
		return
	}
	now := time.Now()
	for _, source := range sources {
		interval := time.Duration(source.IntervalSeconds) * time.Second
		if source.TriggerType == model.AutomationTriggerRSS {
			minutes := source.IntervalMinutes
			if minutes <= 0 {
				minutes = defaultAutomationRSSInterval
			}
			interval = time.Duration(minutes) * time.Minute
		} else if interval <= 0 {
			interval = time.Duration(defaultAutomationIntervalSeconds) * time.Second
		}
		if source.LastCheckedAt != nil && source.LastCheckedAt.Add(interval).After(now) {
			continue
		}
		if !s.beginAutomationScan(source.ID) {
			continue
		}
		select {
		case s.scanWorkers <- struct{}{}:
			s.scanWorkerWG.Add(1)
			go func(source model.AutomationSource) {
				defer func() {
					<-s.scanWorkers
					s.endAutomationScan(source.ID)
					s.scanWorkerWG.Done()
				}()
				if _, err := s.checkAutomationSource(s.ctx, source); err != nil && s.log != nil {
					s.log.Warnf("[AUTOMATION] 触发器检查失败 type=%s source=%d name=%s: %v", source.TriggerType, source.ID, source.Name, err)
				}
			}(source)
		default:
			s.endAutomationScan(source.ID)
			return
		}
	}
}

func (s *AutomationService) beginAutomationScan(sourceID uint) bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanning == nil {
		s.scanning = make(map[uint]struct{})
	}
	if _, exists := s.scanning[sourceID]; exists {
		return false
	}
	s.scanning[sourceID] = struct{}{}
	return true
}

func (s *AutomationService) endAutomationScan(sourceID uint) {
	s.scanMu.Lock()
	delete(s.scanning, sourceID)
	s.scanMu.Unlock()
}
