package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

type RSSGeneratorTokenInput struct {
	Name               string     `json:"name"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	RateLimitPerMinute int        `json:"rate_limit_per_minute"`
}

type RSSGeneratorTokenResult struct {
	Record  RSSGeneratorTokenView `json:"record"`
	Token   string                `json:"token"`
	RSSURL  string                `json:"rss_url"`
	AtomURL string                `json:"atom_url"`
}

type RSSGeneratorTokenView struct {
	ID                 uint       `json:"id"`
	FeedID             uint       `json:"feed_id"`
	Name               string     `json:"name"`
	Prefix             string     `json:"prefix"`
	RateLimitPerMinute int        `json:"rate_limit_per_minute"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type RSSGeneratorPublicAccess struct {
	Token model.RSSGeneratorFeedAccessToken
	Feed  model.RSSGeneratorFeedDefinition
	LAN   bool
}

func (s *RSSGeneratorService) ListTokens(feedID uint) ([]RSSGeneratorTokenView, error) {
	if _, err := s.loadFeed(feedID); err != nil {
		return nil, err
	}
	var records []model.RSSGeneratorFeedAccessToken
	if err := s.db.Where("feed_id = ?", feedID).Order("id DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	views := make([]RSSGeneratorTokenView, 0, len(records))
	for _, record := range records {
		views = append(views, rssGeneratorTokenView(record))
	}
	return views, nil
}

func (s *RSSGeneratorService) CreateToken(feedID uint, input RSSGeneratorTokenInput) (RSSGeneratorTokenResult, error) {
	feed, err := s.loadFeed(feedID)
	if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	name, rateLimit, err := validateRSSGeneratorTokenInput(input)
	if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	clear, hash, prefix, err := newRSSGeneratorAccessToken()
	if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	record := model.RSSGeneratorFeedAccessToken{
		FeedID: feedID, Name: name, TokenHash: hash, Prefix: prefix,
		RateLimitPerMinute: rateLimit, ExpiresAt: input.ExpiresAt,
	}
	if err := s.db.Create(&record).Error; err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	return s.tokenResult(feed, record, clear), nil
}

func (s *RSSGeneratorService) RotateToken(feedID, tokenID uint) (RSSGeneratorTokenResult, error) {
	feed, err := s.loadFeed(feedID)
	if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	var record model.RSSGeneratorFeedAccessToken
	if err := s.db.Where("id = ? AND feed_id = ?", tokenID, feedID).First(&record).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return RSSGeneratorTokenResult{}, ErrRSSGeneratorTokenHidden
	} else if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	now := time.Now()
	if record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
		return RSSGeneratorTokenResult{}, ErrRSSGeneratorTokenHidden
	}
	clear, hash, prefix, err := newRSSGeneratorAccessToken()
	if err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	updates := map[string]any{
		"token_hash": hash, "prefix": prefix, "last_used_at": nil, "updated_at": now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&record).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("feed_id = ? AND auth_context LIKE ?", feedID, fmt.Sprintf("token:%d:%%", tokenID)).Delete(&model.RSSGeneratorFeedCache{}).Error
	}); err != nil {
		return RSSGeneratorTokenResult{}, err
	}
	record.TokenHash, record.Prefix, record.RevokedAt, record.LastUsedAt, record.UpdatedAt = hash, prefix, nil, nil, now
	s.rateMu.Lock()
	delete(s.rateWindows, tokenID)
	s.rateMu.Unlock()
	return s.tokenResult(feed, record, clear), nil
}

func (s *RSSGeneratorService) RevokeToken(feedID, tokenID uint) error {
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RSSGeneratorFeedAccessToken{}).
			Where("id = ? AND feed_id = ? AND revoked_at IS NULL", tokenID, feedID).
			Updates(map[string]any{"revoked_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRSSGeneratorTokenHidden
		}
		if err := tx.Where("feed_id = ? AND auth_context LIKE ?", feedID, fmt.Sprintf("token:%d:%%", tokenID)).Delete(&model.RSSGeneratorFeedCache{}).Error; err != nil {
			return err
		}
		s.rateMu.Lock()
		delete(s.rateWindows, tokenID)
		s.rateMu.Unlock()
		return nil
	})
}

// ResolveFeedAccess resolves a public feed ID first, then either accepts a
// trusted LAN request without a token or validates the feed-specific query
// token for every other network.
func (s *RSSGeneratorService) ResolveFeedAccess(ctx context.Context, publicID, clear string, allowLAN bool) (RSSGeneratorPublicAccess, error) {
	if !rssGeneratorPublicIDPattern.MatchString(publicID) {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorTokenHidden
	}
	var feed model.RSSGeneratorFeedDefinition
	if err := s.db.WithContext(ctx).Where("public_id = ? AND enabled = ?", publicID, true).First(&feed).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorTokenHidden
	} else if err != nil {
		return RSSGeneratorPublicAccess{}, err
	}
	if allowLAN {
		return RSSGeneratorPublicAccess{Feed: feed, LAN: true}, nil
	}
	if len(clear) < 24 || len(clear) > 128 || !strings.HasPrefix(clear, "ffrss_") {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorTokenHidden
	}
	hash := hashRSSGeneratorToken(clear)
	var token model.RSSGeneratorFeedAccessToken
	if err := s.db.WithContext(ctx).Where("token_hash = ? AND feed_id = ?", hash, feed.ID).First(&token).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorTokenHidden
	} else if err != nil {
		return RSSGeneratorPublicAccess{}, err
	}
	now := time.Now()
	if token.RevokedAt != nil || (token.ExpiresAt != nil && !token.ExpiresAt.After(now)) {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorTokenHidden
	}
	if !s.allowRSSGeneratorToken(token.ID, token.RateLimitPerMinute, now) {
		return RSSGeneratorPublicAccess{}, ErrRSSGeneratorRateLimited
	}
	if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) >= time.Minute {
		_ = s.db.Model(&model.RSSGeneratorFeedAccessToken{}).Where("id = ?", token.ID).Update("last_used_at", now).Error
		token.LastUsedAt = &now
	}
	return RSSGeneratorPublicAccess{Token: token, Feed: feed}, nil
}

func (s *RSSGeneratorService) allowRSSGeneratorToken(tokenID uint, limit int, now time.Time) bool {
	if limit <= 0 {
		limit = 60
	}
	minute := now.Unix() / 60
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	window := s.rateWindows[tokenID]
	if window.minute != minute {
		window = rssGeneratorRateWindow{minute: minute}
	}
	if window.count >= limit {
		return false
	}
	window.count++
	s.rateWindows[tokenID] = window
	if len(s.rateWindows) > 10000 {
		for id, value := range s.rateWindows {
			if value.minute < minute-1 {
				delete(s.rateWindows, id)
			}
		}
	}
	return true
}

func validateRSSGeneratorTokenInput(input RSSGeneratorTokenInput) (string, int, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 120 {
		return "", 0, errors.New("token name 长度必须为 1-120")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return "", 0, errors.New("expires_at 必须晚于当前时间")
	}
	rateLimit := input.RateLimitPerMinute
	if rateLimit == 0 {
		rateLimit = 60
	}
	if rateLimit < 1 || rateLimit > 10000 {
		return "", 0, errors.New("rate_limit_per_minute 必须在 1-10000 之间")
	}
	return name, rateLimit, nil
}

func newRSSGeneratorAccessToken() (clear, hash, prefix string, err error) {
	random, err := rssGeneratorRandomString(32)
	if err != nil {
		return "", "", "", err
	}
	clear = "ffrss_" + random
	hash = hashRSSGeneratorToken(clear)
	prefix = clear[:min(len(clear), 16)]
	return clear, hash, prefix, nil
}

func hashRSSGeneratorToken(clear string) string {
	digest := sha256.Sum256([]byte(clear))
	return hex.EncodeToString(digest[:])
}

func (s *RSSGeneratorService) tokenResult(feed model.RSSGeneratorFeedDefinition, record model.RSSGeneratorFeedAccessToken, clear string) RSSGeneratorTokenResult {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	query := "?token=" + url.QueryEscape(clear)
	rssPath := "/rss/" + feed.PublicID + ".xml" + query
	atomPath := "/rss/" + feed.PublicID + ".atom" + query
	if base != "" {
		rssPath = base + rssPath
		atomPath = base + atomPath
	}
	return RSSGeneratorTokenResult{Record: rssGeneratorTokenView(record), Token: clear, RSSURL: rssPath, AtomURL: atomPath}
}

func rssGeneratorTokenView(record model.RSSGeneratorFeedAccessToken) RSSGeneratorTokenView {
	status := "active"
	if record.RevokedAt != nil {
		status = "revoked"
	} else if record.ExpiresAt != nil && !record.ExpiresAt.After(time.Now()) {
		status = "expired"
	}
	return RSSGeneratorTokenView{
		ID: record.ID, FeedID: record.FeedID, Name: record.Name, Prefix: record.Prefix,
		RateLimitPerMinute: record.RateLimitPerMinute, ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, LastUsedAt: record.LastUsedAt, Status: status,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func (access RSSGeneratorPublicAccess) authContext() string {
	if access.LAN {
		return "lan"
	}
	return fmt.Sprintf("token:%d:%s", access.Token.ID, access.Token.TokenHash)
}
