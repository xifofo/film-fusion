package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *RSSGeneratorService) RenderPublic(ctx context.Context, access RSSGeneratorPublicAccess, format string, rawParams map[string]any) (RSSGeneratorRenderedFeed, error) {
	format, err := normalizeRSSGeneratorFormat(format)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	var definitions []RSSGeneratorParameterDefinition
	if err := json.Unmarshal([]byte(access.Feed.ParametersJSON), &definitions); err != nil {
		return RSSGeneratorRenderedFeed{}, errors.New("RSS 参数定义损坏")
	}
	schema, err := rssGeneratorParameterSchema(definitions)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	_, canonicalParams, err := normalizeRSSGeneratorParams(schema, rawParams)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	authContext := access.authContext()
	cacheKey := buildRSSGeneratorCacheKey(access.Feed, canonicalParams, format, authContext)
	now := time.Now()
	var cached model.RSSGeneratorFeedCache
	cacheErr := s.db.Where("cache_key = ?", cacheKey).First(&cached).Error
	if cacheErr == nil {
		if now.Before(cached.ExpiresAt) {
			return rssGeneratorCacheResult(cached, "hit"), nil
		}
		if now.Before(cached.StaleAt) {
			go func() {
				refreshTimeout := time.Duration(s.cfg.RequestTimeoutSeconds) * time.Second
				if refreshTimeout <= 0 {
					refreshTimeout = 45 * time.Second
				}
				refreshContext, cancel := context.WithTimeout(context.Background(), refreshTimeout)
				defer cancel()
				_, _ = s.refreshPublicSingleflight(refreshContext, access, format, rawParams, canonicalParams, authContext, cacheKey)
			}()
			return rssGeneratorCacheResult(cached, "stale"), nil
		}
	} else if !errors.Is(cacheErr, gorm.ErrRecordNotFound) {
		return RSSGeneratorRenderedFeed{}, cacheErr
	}
	return s.refreshPublicSingleflight(ctx, access, format, rawParams, canonicalParams, authContext, cacheKey)
}

func (s *RSSGeneratorService) refreshPublicSingleflight(ctx context.Context, access RSSGeneratorPublicAccess, format string, rawParams map[string]any, canonicalParams, authContext, cacheKey string) (RSSGeneratorRenderedFeed, error) {
	s.flightMu.Lock()
	if existing, ok := s.flights[cacheKey]; ok {
		s.flightMu.Unlock()
		select {
		case <-ctx.Done():
			return RSSGeneratorRenderedFeed{}, ctx.Err()
		case <-existing.done:
			return existing.result, existing.err
		}
	}
	flight := &rssGeneratorFlight{done: make(chan struct{})}
	s.flights[cacheKey] = flight
	s.flightMu.Unlock()

	flight.result, flight.err = s.refreshPublic(ctx, access, format, rawParams, canonicalParams, authContext, cacheKey)
	close(flight.done)
	s.flightMu.Lock()
	delete(s.flights, cacheKey)
	s.flightMu.Unlock()
	return flight.result, flight.err
}

func (s *RSSGeneratorService) refreshPublic(ctx context.Context, access RSSGeneratorPublicAccess, format string, rawParams map[string]any, canonicalParams, authContext, cacheKey string) (RSSGeneratorRenderedFeed, error) {
	secrets, err := s.decryptSecrets(&access.Feed)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	feed, err := s.generateWithWorker(ctx, rssGeneratorPreparedFeed{Record: access.Feed, Secrets: secrets}, rawParams)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	generatedAt := time.Now().UTC().Truncate(time.Second)
	rendered, err := RenderRSSGeneratorFeed(feed, format, generatedAt)
	if err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	cacheTTL := time.Duration(access.Feed.CacheTTLSeconds) * time.Second
	staleTTL := time.Duration(access.Feed.StaleTTLSeconds) * time.Second
	if cacheTTL <= 0 {
		cacheTTL = defaultRSSGeneratorCacheTTL * time.Second
	}
	if staleTTL < cacheTTL {
		staleTTL = defaultRSSGeneratorStaleTTL * time.Second
	}
	record := model.RSSGeneratorFeedCache{
		FeedID: access.Feed.ID, CacheKey: cacheKey, Format: format, AuthContext: authContext,
		NormalizedParams: canonicalParams, ContentType: rendered.ContentType, Body: rendered.Body,
		ETag: rendered.ETag, LastModified: rendered.LastModified, GeneratedAt: generatedAt,
		ExpiresAt: generatedAt.Add(cacheTTL), StaleAt: generatedAt.Add(staleTTL),
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cache_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"feed_id", "format", "auth_context", "normalized_params", "content_type", "body", "etag",
			"last_modified", "generated_at", "expires_at", "stale_at", "updated_at",
		}),
	}).Create(&record).Error; err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	rendered.CacheStatus = "miss"
	return rendered, nil
}

func rssGeneratorCacheResult(record model.RSSGeneratorFeedCache, status string) RSSGeneratorRenderedFeed {
	return RSSGeneratorRenderedFeed{
		Body: append([]byte(nil), record.Body...), ContentType: record.ContentType,
		ETag: record.ETag, LastModified: record.LastModified, CacheStatus: status,
	}
}

func normalizeRSSGeneratorFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "rss", "rss2", "xml":
		return "rss", nil
	case "atom":
		return "atom", nil
	default:
		return "", errors.New("RSS 输出格式只支持 rss 或 atom")
	}
}

func buildRSSGeneratorCacheKey(feed model.RSSGeneratorFeedDefinition, normalizedParams, format, authContext string) string {
	payload := strings.Join([]string{
		"v1", "feed:" + strconv.FormatUint(uint64(feed.ID), 10), "version:" + strconv.Itoa(feed.Version),
		"params:" + normalizedParams, "format:" + format, "auth:" + authContext,
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
