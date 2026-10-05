package organize

import (
	"context"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
)

// Service owns the organize pipeline shared by HTTP, automation and preview jobs.
type Service struct {
	db                  *gorm.DB
	operationCtx        context.Context
	logger              *logger.Logger
	sdk115Open          *sdk115.Client
	moviePilotSvc       *service.MoviePilotService
	tmdbSvc             *service.TMDBService
	web115Svc           *service.Web115Service
	download115Svc      *service.Download115Service
	dirCache            *service.Web115DirCache
	embyClient          *embyhelper.EmbyClient
	previewQueue        *service.OrganizePreviewQueue
	newOrganizeClient   func(string) (*driver.Pan115Client, error)
	readUpgradeSubtitle func(*model.CloudStorage, string, int64) ([]byte, error)
}

func NewService(log *logger.Logger, moviePilotSvc *service.MoviePilotService, tmdbSvc *service.TMDBService, download115Svc *service.Download115Service, embyClient *embyhelper.EmbyClient) *Service {
	return &Service{
		db:             database.GetDB(),
		logger:         log,
		sdk115Open:     sdk115.New(),
		moviePilotSvc:  moviePilotSvc,
		tmdbSvc:        tmdbSvc,
		web115Svc:      service.NewWeb115Service(log),
		download115Svc: download115Svc,
		dirCache:       service.NewWeb115DirCache(web115DirCacheTTL),
		embyClient:     embyClient,
	}
}

func (h *Service) SetPreviewQueue(queue *service.OrganizePreviewQueue) {
	h.previewQueue = queue
}

// WithContext creates an isolated execution scope; shared caches and clients
// remain reusable, while cancellation belongs to this request or background job.
func (h *Service) WithContext(ctx context.Context) *Service {
	if h == nil {
		return nil
	}
	scoped := *h
	scoped.operationCtx = ctx
	scoped.moviePilotSvc = h.moviePilotSvc.WithContext(ctx)
	scoped.embyClient = h.embyClient.WithContext(ctx)
	return &scoped
}

func (h *Service) requestContext() context.Context {
	if h != nil && h.operationCtx != nil {
		return h.operationCtx
	}
	return context.Background()
}

func (h *Service) store() *gorm.DB {
	if h != nil && h.db != nil {
		return h.db
	}
	return database.GetDB()
}

// web115DirCacheTTL 整理流程中「已存在目录」查找的进程内缓存 TTL。
// 10 分钟平衡「跳过 115 的重复列目录」与「避免误用陈旧目录结构」；
// 新建 / 刷新子目录时会同步更新缓存，日常不依赖 TTL 失效来纠正。
const web115DirCacheTTL = 10 * time.Minute
