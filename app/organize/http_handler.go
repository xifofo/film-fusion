package organize

import (
	"context"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
)

// Handler is the HTTP adapter. Background jobs receive Service directly.
type Handler struct {
	*Service
	StopEvents <-chan struct{}
}
type apiResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func NewHandler(log *logger.Logger, mp *service.MoviePilotService, tmdb *service.TMDBService, downloads *service.Download115Service, emby *embyhelper.EmbyClient) *Handler {
	return &Handler{Service: NewService(log, mp, tmdb, downloads, emby)}
}

func (h *Handler) withContext(ctx context.Context) *Handler {
	scoped := *h
	scoped.Service = h.Service.WithContext(ctx)
	return &scoped
}
