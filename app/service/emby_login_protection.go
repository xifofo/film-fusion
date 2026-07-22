package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
)

const maxEmbyLoginBodyBytes = 64 << 10

type loginFailureBucket struct {
	failures     []time.Time
	blockedUntil time.Time
}

// EmbyLoginAttempt 是从登录请求中提取的最小安全上下文，不包含密码。
type EmbyLoginAttempt struct {
	IP       string
	Username string
}

type EmbyLoginBlock struct {
	Scope        string    `json:"scope"`
	IP           string    `json:"ip"`
	Username     string    `json:"username,omitempty"`
	FailureCount int       `json:"failure_count"`
	BlockedUntil time.Time `json:"blocked_until"`
}

type EmbyLoginSecurityEvent struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	IP        string    `json:"ip"`
	Username  string    `json:"username,omitempty"`
	Scope     string    `json:"scope,omitempty"`
}

type EmbyLoginSecuritySnapshot struct {
	Enabled      bool                     `json:"enabled"`
	BlockedCount int                      `json:"blocked_count"`
	Blocks       []EmbyLoginBlock         `json:"blocks"`
	RecentEvents []EmbyLoginSecurityEvent `json:"recent_events"`
}

// EmbyLoginProtection 对 Emby 登录失败做进程内滑动窗口计数和临时封禁。
type EmbyLoginProtection struct {
	cfg              *config.Config
	logger           *logger.Logger
	settingsProvider func() config.LoginSecurityConfig
	logLabel         string
	alertSource      string
	notifier         SecurityAlertNotifier
	now              func() time.Time

	mu     sync.Mutex
	pairs  map[string]*loginFailureBucket
	ips    map[string]*loginFailureBucket
	events []EmbyLoginSecurityEvent
	nextID uint64
}

func NewEmbyLoginProtection(cfg *config.Config, log *logger.Logger, notifiers ...SecurityAlertNotifier) *EmbyLoginProtection {
	return newLoginProtection(cfg, log, "EMBY SECURITY", "emby", firstNotifier(notifiers), func() config.LoginSecurityConfig {
		if cfg == nil {
			return config.LoginSecurityConfig{}
		}
		return cfg.Emby.Security
	})
}

// NewAppLoginProtection 创建 FilmFusion 管理后台登录保护器。
func NewAppLoginProtection(cfg *config.Config, log *logger.Logger, notifiers ...SecurityAlertNotifier) *EmbyLoginProtection {
	return newLoginProtection(cfg, log, "APP SECURITY", "filmfusion", firstNotifier(notifiers), func() config.LoginSecurityConfig {
		if cfg == nil {
			return config.LoginSecurityConfig{}
		}
		return cfg.Server.Security
	})
}

func firstNotifier(notifiers []SecurityAlertNotifier) SecurityAlertNotifier {
	if len(notifiers) == 0 {
		return nil
	}
	return notifiers[0]
}

func newLoginProtection(cfg *config.Config, log *logger.Logger, label, alertSource string, notifier SecurityAlertNotifier, settingsProvider func() config.LoginSecurityConfig) *EmbyLoginProtection {
	return &EmbyLoginProtection{
		cfg: cfg, logger: log, settingsProvider: settingsProvider, logLabel: label,
		alertSource: alertSource, notifier: notifier, now: time.Now,
		pairs: make(map[string]*loginFailureBucket), ips: make(map[string]*loginFailureBucket),
		events: make([]EmbyLoginSecurityEvent, 0, 500),
	}
}

func isEmbyLoginRequest(req *http.Request) bool {
	if req == nil || req.Method != http.MethodPost {
		return false
	}
	path := strings.TrimSuffix(req.URL.Path, "/")
	if len(path) >= len("/emby") && strings.EqualFold(path[:len("/emby")], "/emby") {
		path = path[len("/emby"):]
	}
	return strings.EqualFold(path, "/Users/AuthenticateByName")
}

// Inspect 提取登录请求的 IP 和用户名，并在已封禁时返回 true。
func (p *EmbyLoginProtection) Inspect(req *http.Request) (*EmbyLoginAttempt, bool) {
	if p == nil || !p.settings().Enabled || !isEmbyLoginRequest(req) {
		return nil, false
	}
	return p.InspectCredentials(req, readEmbyLoginUsername(req))
}

// InspectCredentials 检查已由调用方解析出的登录用户名。
func (p *EmbyLoginProtection) InspectCredentials(req *http.Request, username string) (*EmbyLoginAttempt, bool) {
	if p == nil || !p.settings().Enabled || req == nil {
		return nil, false
	}
	attempt := &EmbyLoginAttempt{IP: p.clientIP(req), Username: strings.TrimSpace(username)}
	if attempt.IP == "" {
		attempt.IP = "unknown"
	}
	return attempt, p.isBlocked(attempt)
}

func readEmbyLoginUsername(req *http.Request) string {
	if req == nil || req.Body == nil {
		return ""
	}
	original := req.Body
	body, err := io.ReadAll(io.LimitReader(original, maxEmbyLoginBodyBytes+1))
	if err != nil {
		return ""
	}
	if len(body) > maxEmbyLoginBodyBytes {
		req.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
		return ""
	}
	_ = original.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Username string `json:"Username"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Username) != "" {
		return payload.Username
	}
	if values, err := url.ParseQuery(string(body)); err == nil {
		return values.Get("Username")
	}
	return ""
}

func pairKey(ip, username string) string {
	return ip + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func (p *EmbyLoginProtection) isBlocked(attempt *EmbyLoginAttempt) bool {
	now := p.now()
	settings := p.settings()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now, settings)
	if bucket := p.ips[attempt.IP]; bucket != nil && bucket.blockedUntil.After(now) {
		return true
	}
	if bucket := p.pairs[pairKey(attempt.IP, attempt.Username)]; bucket != nil && bucket.blockedUntil.After(now) {
		return true
	}
	return false
}

// ObserveResponse 根据 Emby 的真实响应更新失败计数。
func (p *EmbyLoginProtection) ObserveResponse(attempt *EmbyLoginAttempt, statusCode int) {
	if p == nil || attempt == nil || !p.settings().Enabled {
		return
	}
	if statusCode >= 200 && statusCode < 300 {
		p.clearPair(attempt)
		return
	}
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return
	}
	p.recordFailure(attempt)
}

func (p *EmbyLoginProtection) clearPair(attempt *EmbyLoginAttempt) {
	p.mu.Lock()
	delete(p.pairs, pairKey(attempt.IP, attempt.Username))
	p.mu.Unlock()
}

func (p *EmbyLoginProtection) recordFailure(attempt *EmbyLoginAttempt) {
	now := p.now()
	settings := p.settings()
	windowStart := now.Add(-time.Duration(settings.WindowMinutes) * time.Minute)
	blockedUntil := now.Add(time.Duration(settings.BlockMinutes) * time.Minute)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now, settings)
	pair := ensureLoginBucket(p.pairs, pairKey(attempt.IP, attempt.Username))
	ip := ensureLoginBucket(p.ips, attempt.IP)
	pair.failures = appendRecentFailure(pair.failures, windowStart, now)
	ip.failures = appendRecentFailure(ip.failures, windowStart, now)
	p.appendEventLocked(now, "failed", attempt, "")

	if len(pair.failures) >= settings.MaxFailuresPerAccountIP && !pair.blockedUntil.After(now) {
		pair.blockedUntil = blockedUntil
		p.appendEventLocked(now, "blocked", attempt, "account_ip")
		p.logBlocked(attempt, "account_ip", blockedUntil)
		p.notifyBlocked(attempt, "account_ip", len(pair.failures), blockedUntil, now)
	}
	if len(ip.failures) >= settings.MaxFailuresPerIP && !ip.blockedUntil.After(now) {
		ip.blockedUntil = blockedUntil
		p.appendEventLocked(now, "blocked", attempt, "ip")
		p.logBlocked(attempt, "ip", blockedUntil)
		p.notifyBlocked(attempt, "ip", len(ip.failures), blockedUntil, now)
	}
}

func ensureLoginBucket(buckets map[string]*loginFailureBucket, key string) *loginFailureBucket {
	if buckets[key] == nil {
		buckets[key] = &loginFailureBucket{}
	}
	return buckets[key]
}

func appendRecentFailure(failures []time.Time, since, now time.Time) []time.Time {
	kept := failures[:0]
	for _, failure := range failures {
		if !failure.Before(since) {
			kept = append(kept, failure)
		}
	}
	return append(kept, now)
}

func (p *EmbyLoginProtection) appendEventLocked(now time.Time, eventType string, attempt *EmbyLoginAttempt, scope string) {
	event := EmbyLoginSecurityEvent{
		ID: atomic.AddUint64(&p.nextID, 1), Timestamp: now, Type: eventType,
		IP: attempt.IP, Username: attempt.Username, Scope: scope,
	}
	p.events = append(p.events, event)
	if len(p.events) > 500 {
		copy(p.events, p.events[len(p.events)-500:])
		p.events = p.events[:500]
	}
}

func (p *EmbyLoginProtection) logBlocked(attempt *EmbyLoginAttempt, scope string, until time.Time) {
	if p.logger != nil {
		p.logger.Warnf("[%s] 登录已临时封禁 scope=%s ip=%s username=%q until=%s", p.logLabel, scope, attempt.IP, attempt.Username, until.Format(time.RFC3339))
	}
}

func (p *EmbyLoginProtection) notifyBlocked(attempt *EmbyLoginAttempt, scope string, failureCount int, until, now time.Time) {
	if p.notifier == nil {
		return
	}
	p.notifier.NotifySecurityAlert(SecurityAlert{
		Source: p.alertSource, IP: attempt.IP, Username: attempt.Username, Scope: scope,
		FailureCount: failureCount, BlockedUntil: until, TriggeredAt: now,
	})
}

func (p *EmbyLoginProtection) Snapshot() EmbyLoginSecuritySnapshot {
	settings := p.settings()
	snapshot := EmbyLoginSecuritySnapshot{Enabled: settings.Enabled, Blocks: []EmbyLoginBlock{}, RecentEvents: []EmbyLoginSecurityEvent{}}
	if p == nil {
		return snapshot
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now, settings)
	for key, bucket := range p.pairs {
		if !bucket.blockedUntil.After(now) {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		block := EmbyLoginBlock{Scope: "account_ip", IP: parts[0], FailureCount: len(bucket.failures), BlockedUntil: bucket.blockedUntil}
		if len(parts) == 2 {
			block.Username = parts[1]
		}
		snapshot.Blocks = append(snapshot.Blocks, block)
	}
	for ip, bucket := range p.ips {
		if bucket.blockedUntil.After(now) {
			snapshot.Blocks = append(snapshot.Blocks, EmbyLoginBlock{Scope: "ip", IP: ip, FailureCount: len(bucket.failures), BlockedUntil: bucket.blockedUntil})
		}
	}
	sort.Slice(snapshot.Blocks, func(i, j int) bool {
		if snapshot.Blocks[i].IP != snapshot.Blocks[j].IP {
			return snapshot.Blocks[i].IP < snapshot.Blocks[j].IP
		}
		if snapshot.Blocks[i].Scope != snapshot.Blocks[j].Scope {
			return snapshot.Blocks[i].Scope < snapshot.Blocks[j].Scope
		}
		return snapshot.Blocks[i].Username < snapshot.Blocks[j].Username
	})
	for i := len(p.events) - 1; i >= 0 && len(snapshot.RecentEvents) < 100; i-- {
		snapshot.RecentEvents = append(snapshot.RecentEvents, p.events[i])
	}
	snapshot.BlockedCount = len(snapshot.Blocks)
	return snapshot
}

func (p *EmbyLoginProtection) Unblock(scope, ip, username string) bool {
	if p == nil {
		return false
	}
	scope = strings.TrimSpace(scope)
	ip = strings.TrimSpace(ip)
	username = strings.TrimSpace(username)
	if ip == "" || (scope != "ip" && scope != "account_ip") {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	removed := false
	if scope == "ip" {
		if _, ok := p.ips[ip]; ok {
			delete(p.ips, ip)
			removed = true
		}
		prefix := ip + "\x00"
		for key := range p.pairs {
			if strings.HasPrefix(key, prefix) {
				delete(p.pairs, key)
				removed = true
			}
		}
	} else {
		key := pairKey(ip, username)
		if _, ok := p.pairs[key]; ok {
			delete(p.pairs, key)
			removed = true
		}
	}
	if removed {
		p.appendEventLocked(p.now(), "unblocked", &EmbyLoginAttempt{IP: ip, Username: username}, scope)
	}
	return removed
}

func (p *EmbyLoginProtection) pruneLocked(now time.Time, settings config.LoginSecurityConfig) {
	windowStart := now.Add(-time.Duration(settings.WindowMinutes) * time.Minute)
	prune := func(buckets map[string]*loginFailureBucket) {
		for key, bucket := range buckets {
			if !bucket.blockedUntil.IsZero() && !bucket.blockedUntil.After(now) {
				delete(buckets, key)
				continue
			}
			kept := bucket.failures[:0]
			for _, failure := range bucket.failures {
				if !failure.Before(windowStart) {
					kept = append(kept, failure)
				}
			}
			bucket.failures = kept
			if len(bucket.failures) == 0 && !bucket.blockedUntil.After(now) {
				delete(buckets, key)
			}
		}
	}
	prune(p.pairs)
	prune(p.ips)
}

func (p *EmbyLoginProtection) settings() config.LoginSecurityConfig {
	settings := config.LoginSecurityConfig{}
	if p != nil && p.settingsProvider != nil {
		settings = p.settingsProvider()
	}
	if settings.WindowMinutes <= 0 {
		settings.WindowMinutes = 10
	}
	if settings.MaxFailuresPerAccountIP <= 0 {
		settings.MaxFailuresPerAccountIP = 5
	}
	if settings.MaxFailuresPerIP <= 0 {
		settings.MaxFailuresPerIP = 20
	}
	if settings.BlockMinutes <= 0 {
		settings.BlockMinutes = 30
	}
	return settings
}

func (p *EmbyLoginProtection) clientIP(req *http.Request) string {
	remoteIP := remoteAddressIP(req.RemoteAddr)
	if remoteIP == "" || !ipInCIDRs(remoteIP, p.settings().TrustedProxyCIDRs) {
		return remoteIP
	}
	candidates := make([]string, 0, 4)
	for _, value := range strings.Split(req.Header.Get("X-Forwarded-For"), ",") {
		if ip := normalizeIP(value); ip != "" {
			candidates = append(candidates, ip)
		}
	}
	if len(candidates) == 0 {
		if ip := normalizeIP(req.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		return remoteIP
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if !ipInCIDRs(candidates[i], p.settings().TrustedProxyCIDRs) {
			return candidates[i]
		}
	}
	return candidates[0]
}

func remoteAddressIP(address string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err == nil {
		return normalizeIP(host)
	}
	if normalized := normalizeIP(address); normalized != "" {
		return normalized
	}
	return ""
}

func ipInCIDRs(ip string, cidrs []string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(addr) {
			return true
		}
		if trusted, err := netip.ParseAddr(raw); err == nil && trusted.Unmap() == addr {
			return true
		}
	}
	return false
}

func normalizeIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return addr.Unmap().String()
}
