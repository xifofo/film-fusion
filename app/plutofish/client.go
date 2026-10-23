package plutofish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const DefaultBaseURL = "https://plutofish.shop"

// Sink commits durable processing/review work, event deduplication and an
// optional cursor in one transaction. Empty cursor means keep the old cursor.
// A successful return means events are safe to replay, including after a crash.
type Sink interface {
	Cursor(context.Context) (string, error)
	Commit(context.Context, []Event, string) error
}

type Config struct {
	BaseURL      string
	APIKey       string
	Mode         string // "sse" (default) or "poll"
	PollInterval time.Duration
	IdleTimeout  time.Duration
}

type Status struct {
	State           string     `json:"state"`
	Mode            string     `json:"mode"`
	LastEventAt     *time.Time `json:"lastEventAt,omitempty"`
	LastConnectedAt *time.Time `json:"lastConnectedAt,omitempty"`
	ReconnectAt     *time.Time `json:"reconnectAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	HTTPStatus      int        `json:"httpStatus,omitempty"`
}

// ClientError contains only safe, fixed messages. Response bodies, request URLs,
// Authorization headers and underlying network errors are never exposed.
type ClientError struct {
	Code       string
	Message    string
	HTTPStatus int
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ClientError) Error() string { return e.Message }

type Client struct {
	cfg      Config
	sink     Sink
	http     *http.Client
	running  atomic.Bool
	mu       sync.RWMutex
	status   Status
	retryMin time.Duration
	sleep    func(context.Context, time.Duration) error
	jitter   func() float64
}

// NormalizeBaseURL returns the canonical service scope and rejects unsafe URL
// forms. Plain HTTP is allowed only for literal loopback mock servers.
func NormalizeBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", configError("invalid service base URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return "", configError("service URL requires HTTPS; HTTP is allowed only for loopback mocks")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

func NewClient(cfg Config, sink Sink) (*Client, error) {
	base, err := NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, configError("a durable event sink is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, configError("a valid local API key is required")
	}
	cfg.BaseURL = base
	if cfg.Mode == "" {
		cfg.Mode = "sse"
	}
	if cfg.Mode != "sse" && cfg.Mode != "poll" {
		return nil, configError("mode must be sse or poll")
	}
	if cfg.PollInterval < 10*time.Second {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 60 * time.Second
	}
	if cfg.IdleTimeout <= 15*time.Second {
		return nil, configError("SSE idle timeout must exceed the 15-second heartbeat interval")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		cfg: cfg, sink: sink, retryMin: 5 * time.Second,
		http:   &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		status: Status{State: "stopped", Mode: cfg.Mode}, sleep: sleepContext, jitter: rand.Float64,
	}, nil
}

func (c *Client) Snapshot() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// Run owns the one catch-up/stream (or polling) loop until cancellation or a
// configuration/authentication error. Repeated concurrent Run calls are refused.
func (c *Client) Run(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return configError("consumer is already running")
	}
	defer c.running.Store(false)
	defer c.http.CloseIdleConnections()
	attempt := 0
	for {
		if ctx.Err() != nil {
			c.setState("stopped", nil)
			return nil
		}
		c.setState("catching_up", nil)
		err := c.catchUp(ctx)
		if err == nil && c.cfg.Mode == "poll" {
			attempt = 0
			c.setState("polling", nil)
			delay := c.cfg.PollInterval + time.Duration(float64(c.cfg.PollInterval)*0.2*c.jitter())
			if c.sleep(ctx, delay) != nil {
				c.setState("stopped", nil)
				return nil
			}
			continue
		}
		if err == nil {
			started := time.Now()
			err = c.stream(ctx)
			if time.Since(started) >= time.Minute {
				attempt = 0
			}
		}
		if ctx.Err() != nil {
			c.setState("stopped", nil)
			return nil
		}
		var clientErr *ClientError
		if !errors.As(err, &clientErr) {
			clientErr = &ClientError{Code: "connection", Message: "notification connection ended; retrying from committed cursor", Retryable: true}
		}
		if !clientErr.Retryable {
			c.setState("blocked", clientErr)
			return clientErr
		}
		delay := c.backoff(attempt, clientErr.RetryAfter)
		if attempt < 8 {
			attempt++
		}
		c.setState("backoff", clientErr)
		c.mu.Lock()
		next := time.Now().Add(delay)
		c.status.ReconnectAt = &next
		c.mu.Unlock()
		if c.sleep(ctx, delay) != nil {
			c.setState("stopped", nil)
			return nil
		}
	}
}

func (c *Client) catchUp(ctx context.Context) error {
	for {
		cursor, err := c.sink.Cursor(ctx)
		if err != nil {
			return sinkError()
		}
		pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		u, _ := url.Parse(c.cfg.BaseURL + "/api/v1/events")
		q := u.Query()
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("after", cursor)
		}
		u.RawQuery = q.Encode()
		req, err := c.request(pageCtx, u.String(), "application/json")
		if err != nil {
			cancel()
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			return networkError()
		}
		if resp.StatusCode != http.StatusOK {
			err = responseError(resp)
			_ = resp.Body.Close()
			cancel()
			return err
		}
		var page struct {
			Items      []Event `json:"items"`
			NextCursor string  `json:"nextCursor"`
			HasMore    bool    `json:"hasMore"`
		}
		// Read a bounded JSON page, never the unbounded SSE response.
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			return networkError()
		}
		if len(data) > 16<<20 || !utf8.Valid(data) || json.Unmarshal(data, &page) != nil || page.NextCursor == "" || len(page.Items) > 100 {
			return protocolError("invalid HTTP notification page; committed cursor retained")
		}
		for _, e := range page.Items {
			if e.ID == "" {
				return protocolError("notification has no event ID; committed cursor retained")
			}
		}
		if page.HasMore && page.NextCursor == cursor {
			return protocolError("HTTP notification page made no cursor progress")
		}
		if err := c.sink.Commit(ctx, page.Items, page.NextCursor); err != nil {
			return sinkError()
		}
		if len(page.Items) > 0 {
			c.receivedEvent()
		}
		if !page.HasMore {
			return nil
		}
	}
}

func (c *Client) stream(ctx context.Context) error {
	cursor, err := c.sink.Cursor(ctx)
	if err != nil {
		return sinkError()
	}
	req, err := c.request(ctx, c.cfg.BaseURL+"/api/v1/events/stream", "text/event-stream")
	if err != nil {
		return err
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return networkError()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return protocolError("notification stream did not return text/event-stream")
	}
	c.setState("connected", nil)
	c.mu.Lock()
	now := time.Now()
	c.status.LastConnectedAt = &now
	c.mu.Unlock()
	err = readSSE(idleBody{ReadCloser: resp.Body, timeout: c.cfg.IdleTimeout}, func(frame sseFrame) error {
		if !utf8.ValidString(frame.data) {
			return protocolError("notification frame is not valid UTF-8")
		}
		if frame.event == "cursor" {
			var progress struct {
				Cursor string `json:"cursor"`
			}
			if json.Unmarshal([]byte(frame.data), &progress) != nil || progress.Cursor == "" || frame.id != progress.Cursor {
				return protocolError("invalid SSE cursor frame; committed cursor retained")
			}
			if err := c.sink.Commit(ctx, nil, progress.Cursor); err != nil {
				return sinkError()
			}
			return nil
		}
		var event Event
		if json.Unmarshal([]byte(frame.data), &event) != nil || event.ID == "" || event.ID != frame.id || event.Type == "" || frame.event != event.Type {
			return protocolError("invalid SSE notification frame; committed cursor retained")
		}
		if err := c.sink.Commit(ctx, []Event{event}, ""); err != nil {
			return sinkError()
		}
		c.receivedEvent()
		return nil
	}, func(retry time.Duration) {
		c.retryMin = max(5*time.Second, min(retry, 120*time.Second))
	})
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return err
	}
	return networkError()
}

func (c *Client) request(ctx context.Context, endpoint, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, configError("invalid notification endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", accept)
	return req, nil
}

func (c *Client) receivedEvent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.status.LastEventAt = &now
}

func (c *Client) setState(state string, err *ClientError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = state
	c.status.ReconnectAt = nil
	if err != nil {
		c.status.LastError, c.status.ErrorCode, c.status.HTTPStatus = err.Message, err.Code, err.HTTPStatus
	} else if state == "connected" || state == "polling" {
		c.status.LastError, c.status.ErrorCode, c.status.HTTPStatus = "", "", 0
	}
}

func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	base := min(c.retryMin*time.Duration(1<<min(attempt, 8)), 120*time.Second)
	delay := time.Duration(float64(base) * (0.8 + 0.4*c.jitter()))
	delay = max(c.retryMin, min(delay, 120*time.Second))
	return max(delay, retryAfter)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func configError(message string) *ClientError {
	return &ClientError{Code: "configuration", Message: message}
}

func networkError() *ClientError {
	return &ClientError{Code: "connection", Message: "notification connection interrupted; retrying from committed cursor", Retryable: true}
}

func sinkError() *ClientError {
	return &ClientError{Code: "storage", Message: "local event persistence failed; committed cursor retained", Retryable: true}
}

func protocolError(message string) *ClientError {
	return &ClientError{Code: "protocol", Message: message}
}

func responseError(resp *http.Response) *ClientError {
	err := &ClientError{Code: "http", HTTPStatus: resp.StatusCode, Message: fmt.Sprintf("notification endpoint returned HTTP %d", resp.StatusCode)}
	switch resp.StatusCode {
	case 401:
		err.Code, err.Message = "authentication", "HTTP 401: check API key expiry, revocation or disabled account; automatic retries stopped"
	case 422:
		err.Code, err.Message = "cursor", "HTTP 422: check the configured user and committed cursor; progress was retained and automatic retries stopped"
	case 403:
		err.Code, err.Message = "permission", "HTTP 403: check events:read permission and account access; automatic retries stopped"
	case 408, 425, 429:
		err.Retryable = true
	default:
		err.Retryable = resp.StatusCode >= 500 && resp.StatusCode <= 599
	}
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		err.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return err
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if target, err := http.ParseTime(value); err == nil && target.After(now) {
		return target.Sub(now)
	}
	return 0
}
