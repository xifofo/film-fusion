package service

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/directorymonitor"
	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const (
	defaultAutomationIntervalSeconds = 300
	defaultAutomationQuietSeconds    = 120
	defaultAutomationRSSInterval     = 5
	defaultAutomationWorkerCount     = 4
	defaultAutomationScanWorkers     = 2
	maxAutomationNameLength          = 120
	automationSecretMask             = "********"
)

type AutomationService struct {
	db          *gorm.DB
	log         *logger.Logger
	notifier    NotificationPublisher
	web115      *Web115Service
	httpClient  *http.Client
	rssClock    func() time.Time
	rssService  *RSSService
	moviePilot  automationMoviePilotRecognizer
	localMedia  automationLocalMediaRecognizer
	mpTransfer  automationMoviePilotTransferer
	cloud115    automation115Gateway
	monitor115  automation115MonitorLister
	organizer   AutomationOrganizer
	mediaStatus AutomationMediaStatusChecker
	emby        AutomationEmbyClient

	ctx           context.Context
	cancel        context.CancelFunc
	sourceWake    chan struct{}
	executionWake chan struct{}
	wg            sync.WaitGroup
	workerWG      sync.WaitGroup
	scanWorkerWG  sync.WaitGroup

	started           atomic.Bool
	workers           chan struct{}
	scanWorkers       chan struct{}
	executionCursorID uint
	scanMu            sync.Mutex
	scanning          map[uint]struct{}
	manualRunMu       sync.Mutex
	localMonitorMu    sync.Mutex
	localMonitor      *directorymonitor.Monitor
	sourceCursorID    uint
}

func NewAutomationService(log *logger.Logger, notifier NotificationPublisher, moviePilot *MoviePilotService) *AutomationService {
	ctx, cancel := context.WithCancel(context.Background())
	web115 := NewWeb115Service(log)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &AutomationService{
		db: database.GetDB(), log: log, notifier: notifier,
		web115: web115, httpClient: httpClient, moviePilot: moviePilot, mpTransfer: moviePilot,
		cloud115:   &defaultAutomation115Gateway{web115: web115, httpClient: httpClient},
		monitor115: &defaultAutomation115MonitorLister{web115: web115},
		ctx:        ctx, cancel: cancel,
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		workers:     make(chan struct{}, defaultAutomationWorkerCount),
		scanWorkers: make(chan struct{}, defaultAutomationScanWorkers), scanning: make(map[uint]struct{}),
	}
}

func serviceWeb115ForAutomation(log *logger.Logger) *Web115Service {
	return NewWeb115Service(log)
}

func (s *AutomationService) SetLocalMediaRecognition(local automationLocalMediaRecognizer) {
	if s != nil {
		s.localMedia = local
	}
}

func (s *AutomationService) Start() {
	if s == nil || s.db == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	s.db.Model(&model.AutomationNodeRun{}).
		Where("status = ?", model.AutomationNodeRunning).
		Updates(map[string]any{"status": model.AutomationNodePending, "started_at": nil})
	s.db.Model(&model.AutomationRun{}).
		Where("status = ?", model.AutomationRunRunning).
		Update("status", model.AutomationRunPending)
	s.wg.Add(3)
	go s.sourceScheduleLoop()
	go s.executionScheduleLoop()
	go s.automationHealthLoop()
	if s.log != nil {
		s.log.Info("自动化触发器调度器已启动")
	}
}

func (s *AutomationService) Stop() {
	if s == nil || !s.started.CompareAndSwap(true, false) {
		return
	}
	s.cancel()
	s.wg.Wait()
	s.scanWorkerWG.Wait()
	s.closeLocalMonitor()
	s.workerWG.Wait()
	if s.log != nil {
		s.log.Info("自动化触发器调度器已停止")
	}
}

func (s *AutomationService) Wake() {
	select {
	case s.sourceWake <- struct{}{}:
	default:
	}
	s.wakeExecution()
}

func (s *AutomationService) wakeExecution() {
	select {
	case s.executionWake <- struct{}{}:
	default:
	}
}

// RSS fetching is provided by the independent RSS service. Test instances may
// supply only a database/client; the cache still coordinates through the DB.
func (s *AutomationService) rssFeedService() *RSSService {
	if s.rssService != nil {
		return s.rssService
	}
	return &RSSService{db: s.db, httpClient: s.httpClient, clock: s.rssClock}
}

func (s *AutomationService) SetRSSService(rss *RSSService) { s.rssService = rss }
