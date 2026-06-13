// Package embyplayback tracks current Emby playback sessions observed by the proxy.
package embyplayback

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"film-fusion/app/store/embyproxylog"
)

const staleSessionTTL = 12 * time.Hour

type Event struct {
	ItemID        string
	MediaSourceID string
	PlaySessionID string
	RemoteIP      string
	UserAgent     string
	Timestamp     time.Time
}

type Session struct {
	Key                 string
	ItemID              string
	MediaSourceID       string
	PlaySessionID       string
	RemoteIP            string
	UserAgent           string
	MediaPath           string
	Match302ID          uint
	AssignmentID        uint
	AssignedStorageID   uint
	AssignedStorageName string
	ActualStorageID     uint
	ActualStorageName   string
	AccountType         string
	Status              string
	FallbackReason      string
	StartedAt           time.Time
	LastEventAt         time.Time
	LastRequestAt       time.Time
}

type Store struct {
	mu       sync.RWMutex
	sessions map[string]Session
}

func NewStore() *Store {
	return &Store{
		sessions: map[string]Session{},
	}
}

func (s *Store) MarkActive(event Event) {
	event = normalizeEvent(event)
	if event.ItemID == "" {
		return
	}
	key := playbackKey(event.ItemID, event.MediaSourceID, event.RemoteIP, event.UserAgent)
	if key == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(event.Timestamp)

	if event.MediaSourceID != "" {
		if oldKey, ok := s.findSessionKeyLocked(event.ItemID, "", event.RemoteIP, event.UserAgent); ok && oldKey != key {
			existing := s.sessions[oldKey]
			delete(s.sessions, oldKey)
			existing.Key = key
			existing.MediaSourceID = event.MediaSourceID
			s.sessions[key] = existing
		}
	}

	session := s.sessions[key]
	if session.Key == "" {
		session = Session{
			Key:       key,
			StartedAt: event.Timestamp,
		}
	}
	session.ItemID = firstNonEmpty(event.ItemID, session.ItemID)
	session.MediaSourceID = firstNonEmpty(event.MediaSourceID, session.MediaSourceID)
	session.PlaySessionID = firstNonEmpty(event.PlaySessionID, session.PlaySessionID)
	session.RemoteIP = firstNonEmpty(event.RemoteIP, session.RemoteIP)
	session.UserAgent = firstNonEmpty(event.UserAgent, session.UserAgent)
	session.LastEventAt = event.Timestamp
	if session.StartedAt.IsZero() {
		session.StartedAt = event.Timestamp
	}
	s.sessions[key] = session
}

func (s *Store) MarkStopped(event Event) {
	event = normalizeEvent(event)
	if event.ItemID == "" && event.PlaySessionID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(event.Timestamp)

	keys := s.matchingSessionKeysLocked(event.ItemID, event.MediaSourceID, event.RemoteIP, event.UserAgent, event.PlaySessionID)
	for _, key := range keys {
		delete(s.sessions, key)
	}
}

func (s *Store) AttachRedirect(entry embyproxylog.Entry) {
	if entry.ItemID == "" || entry.ActualStorageID == 0 {
		return
	}
	now := entry.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	key := entry.PlaybackKey()
	if key == "" {
		key = playbackKey(entry.ItemID, entry.MediaSourceID, entry.RemoteIP, entry.UserAgent)
	}
	if key == "" {
		return
	}

	if entry.MediaSourceID != "" {
		if oldKey, ok := s.findSessionKeyLocked(entry.ItemID, "", entry.RemoteIP, entry.UserAgent); ok && oldKey != key {
			existing := s.sessions[oldKey]
			delete(s.sessions, oldKey)
			existing.Key = key
			existing.MediaSourceID = entry.MediaSourceID
			s.sessions[key] = existing
		}
	}

	session := s.sessions[key]
	if session.Key == "" {
		session = Session{
			Key:       key,
			StartedAt: now,
		}
	}
	session.ItemID = firstNonEmpty(entry.ItemID, session.ItemID)
	session.MediaSourceID = firstNonEmpty(entry.MediaSourceID, session.MediaSourceID)
	session.RemoteIP = firstNonEmpty(entry.RemoteIP, session.RemoteIP)
	session.UserAgent = firstNonEmpty(entry.UserAgent, session.UserAgent)
	session.MediaPath = firstNonEmpty(entry.MediaPath, session.MediaPath)
	session.Match302ID = entry.Match302ID
	session.AssignmentID = entry.AssignmentID
	session.AssignedStorageID = entry.AssignedStorageID
	session.AssignedStorageName = entry.AssignedStorageName
	session.ActualStorageID = entry.ActualStorageID
	session.ActualStorageName = entry.ActualStorageName
	session.AccountType = entry.AccountType
	session.Status = entry.BalanceStatus
	session.FallbackReason = entry.FallbackReason
	session.LastRequestAt = now
	if session.LastEventAt.IsZero() {
		session.LastEventAt = now
	}
	s.sessions[key] = session
}

func (s *Store) Snapshot() []Session {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	out := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		out = append(out, session)
	}
	return out
}

func (s *Store) ActiveCountsByStorageExcept(excludeKey string) map[uint]int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	counts := map[uint]int{}
	seen := map[string]bool{}
	for key, session := range s.sessions {
		if key == excludeKey || session.ActualStorageID == 0 {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		counts[session.ActualStorageID]++
	}
	return counts
}

func (s *Store) Clear() {
	s.mu.Lock()
	s.sessions = map[string]Session{}
	s.mu.Unlock()
}

func (s *Store) findSessionKeyLocked(itemID, mediaSourceID, remoteIP, userAgent string) (string, bool) {
	keys := s.matchingSessionKeysLocked(itemID, mediaSourceID, remoteIP, userAgent, "")
	if len(keys) == 0 {
		return "", false
	}
	return keys[0], true
}

func (s *Store) matchingSessionKeysLocked(itemID, mediaSourceID, remoteIP, userAgent, playSessionID string) []string {
	itemID = strings.TrimSpace(itemID)
	mediaSourceID = strings.TrimSpace(mediaSourceID)
	remoteIP = strings.TrimSpace(remoteIP)
	userAgent = strings.TrimSpace(userAgent)
	playSessionID = strings.TrimSpace(playSessionID)

	out := make([]string, 0, 2)
	for key, session := range s.sessions {
		if itemID != "" && session.ItemID != itemID {
			continue
		}
		if mediaSourceID != "" && session.MediaSourceID != "" && session.MediaSourceID != mediaSourceID {
			continue
		}
		if playSessionID != "" && session.PlaySessionID != "" && session.PlaySessionID != playSessionID {
			continue
		}
		if remoteIP != "" && session.RemoteIP != "" && session.RemoteIP != remoteIP {
			continue
		}
		if userAgent != "" && session.UserAgent != "" && session.UserAgent != userAgent {
			continue
		}
		out = append(out, key)
	}
	return out
}

func (s *Store) pruneLocked(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	for key, session := range s.sessions {
		lastSeen := session.LastEventAt
		if session.LastRequestAt.After(lastSeen) {
			lastSeen = session.LastRequestAt
		}
		if lastSeen.IsZero() {
			lastSeen = session.StartedAt
		}
		if !lastSeen.IsZero() && now.Sub(lastSeen) > staleSessionTTL {
			delete(s.sessions, key)
		}
	}
}

func normalizeEvent(event Event) Event {
	event.ItemID = strings.TrimSpace(event.ItemID)
	event.MediaSourceID = strings.TrimSpace(event.MediaSourceID)
	event.PlaySessionID = strings.TrimSpace(event.PlaySessionID)
	event.RemoteIP = strings.TrimSpace(event.RemoteIP)
	event.UserAgent = strings.TrimSpace(event.UserAgent)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	return event
}

func playbackKey(itemID, mediaSourceID, remoteIP, userAgent string) string {
	itemID = strings.TrimSpace(itemID)
	mediaSourceID = strings.TrimSpace(mediaSourceID)
	remoteIP = strings.TrimSpace(remoteIP)
	userAgent = strings.TrimSpace(userAgent)
	if itemID == "" && mediaSourceID == "" && remoteIP == "" && userAgent == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%s", itemID, mediaSourceID, remoteIP, userAgent)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
