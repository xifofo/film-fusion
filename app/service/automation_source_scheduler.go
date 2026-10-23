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
	// Keyset pages bound source metadata in memory. Rotate after each admitted
	// scan so large or constantly dirty roots cannot starve later monitors.
	now := time.Now()
	start := s.sourceCursorID
	for pass := 0; pass < 2; pass++ {
		cursor := start
		if pass == 1 {
			if start == 0 {
				return
			}
			cursor = 0
		}
		for {
			if s.ctx != nil && s.ctx.Err() != nil {
				return
			}
			var sources []model.AutomationSource
			query := s.db.Where("enabled = ? AND id > ?", true, cursor)
			if pass == 1 {
				query = query.Where("id <= ?", start)
			}
			if err := query.Order("id ASC").Limit(64).Find(&sources).Error; err != nil {
				return
			}
			if len(sources) == 0 {
				break
			}
			for _, source := range sources {
				cursor = source.ID
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
				if source.TriggerType == model.AutomationTriggerLocalDirectory {
					if !s.localSourceDue(source, now, interval) {
						continue
					}
				} else if source.LastCheckedAt != nil && source.LastCheckedAt.Add(interval).After(now) {
					continue
				}
				if !s.beginAutomationScan(source.ID) {
					continue
				}
				select {
				case s.scanWorkers <- struct{}{}:
					s.sourceCursorID = source.ID
					s.scanWorkerWG.Add(1)
					go func(source model.AutomationSource) {
						resumeLocal := false
						defer func() {
							<-s.scanWorkers
							s.endAutomationScan(source.ID)
							s.scanWorkerWG.Done()
							// Release the source and worker before waking the rotating
							// scheduler, otherwise it can consume the wake while busy.
							if resumeLocal {
								select {
								case s.sourceWake <- struct{}{}:
								default:
								}
							}
						}()
						// The source may have been edited/disabled between page selection
						// and acquiring its scan lock. Read the current configuration.
						if err := s.db.First(&source, source.ID).Error; err != nil || !source.Enabled {
							return
						}
						var result AutomationScanResult
						var err error
						if source.TriggerType == model.AutomationTriggerLocalDirectory {
							result, err = s.scanAutomationLocalWithInterval(s.ctx, source, int(interval/time.Second))
						} else {
							result, err = s.checkAutomationSource(s.ctx, source)
						}
						if err != nil && s.log != nil {
							s.log.Warnf("[AUTOMATION] 触发器检查失败 type=%s source=%d name=%s: %v", source.TriggerType, source.ID, source.Name, err)
						}
						resumeLocal = err == nil && result.LocalMonitor != nil && result.LocalMonitor.InProgress && !result.LocalMonitor.Backpressured
					}(source)
				default:
					s.endAutomationScan(source.ID)
					return
				}
			}
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
