package plutofish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testSink struct {
	mu      sync.Mutex
	cursor  string
	events  []Event
	commits []string
	failID  string
	path    string
}

func (s *testSink) Cursor(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *testSink) Commit(_ context.Context, events []Event, cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		if event.ID == s.failID {
			return errors.New("deliberately sensitive storage error")
		}
	}
	// This mock persists the sink contract; production deduplication/transactions
	// are the responsibility of the database-backed Sink, not the transport.
	for _, event := range events {
		duplicate := false
		for _, prior := range s.events {
			duplicate = duplicate || event.ID == prior.ID
		}
		if !duplicate {
			s.events = append(s.events, event)
		}
	}
	if cursor != "" {
		s.cursor = cursor
	}
	s.commits = append(s.commits, cursor)
	if s.path != "" {
		data, _ := json.Marshal(struct {
			Cursor string
			Events []Event
		}{s.cursor, s.events})
		return os.WriteFile(s.path, data, 0600)
	}
	return nil
}

func makeClient(t *testing.T, server *httptest.Server, sink Sink) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: server.URL, APIKey: "mock-secret-do-not-log"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.http.CloseIdleConnections)
	return c
}

func testEvent(id, typ, status string) Event {
	return Event{ID: id, Type: typ, Status: status, SchemaVersion: 1, TransferID: "shared-transfer", WorkTitle: "电影雪", FileName: "雪.mkv", SizeBytes: 9223372036854775807}
}

func writePage(w http.ResponseWriter, items []Event, next string, more bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": next, "hasMore": more})
}

func eventFrame(event Event) string {
	data, _ := json.Marshal(event)
	return fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
}

func cursorFrame(cursor string) string {
	data, _ := json.Marshal(map[string]string{"cursor": cursor})
	return fmt.Sprintf("id: %s\nevent: cursor\ndata: %s\n\n", cursor, data)
}

func TestCatchUpMultiPageEmptyProgressAndEncodedOpaqueCursor(t *testing.T) {
	sink := &testSink{}
	var afters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-secret-do-not-log" || r.URL.Query().Get("limit") != "100" {
			t.Error("missing authentication or page limit")
		}
		if strings.Contains(r.URL.String(), "mock-secret") {
			t.Error("secret leaked to URL")
		}
		afters = append(afters, r.URL.Query().Get("after"))
		switch len(afters) {
		case 1:
			if r.URL.Query().Has("after") {
				t.Error("initial cursor must be omitted")
			}
			writePage(w, []Event{testEvent("event-一", "transfer.failed", "failed")}, "opaque:/ +汉&", true)
		case 2:
			writePage(w, []Event{}, "filtered-empty-progress", true)
		case 3:
			writePage(w, []Event{testEvent("event-二", "transfer.succeeded", "succeeded")}, "finished", false)
		default:
			t.Error("unexpected extra request")
		}
	}))
	defer server.Close()
	if err := makeClient(t, server, sink).catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afters, []string{"", "opaque:/ +汉&", "filtered-empty-progress"}) || sink.cursor != "finished" || len(sink.events) != 2 {
		t.Fatalf("incorrect pages: after=%q cursor=%q event count=%d", afters, sink.cursor, len(sink.events))
	}
	if sink.events[1].SizeBytes != 9223372036854775807 || sink.events[1].WorkTitle != "电影雪" {
		t.Fatal("int64 or UTF-8 event data lost")
	}
}

type byteReader struct{ data []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestSSEParserChunkedUTF8CRLFMultilineAndHeartbeat(t *testing.T) {
	input := "\ufeffretry: 5000\r\n: heartbeat\r\n\r\n" +
		"id: opaque-event\r\nevent: transfer.succeeded\r\ndata: {\r\ndata: \"title\":\"电影雪\"}\r\n\r\n" +
		"id: cursor-value\revent: cursor\rdata: {\"cursor\":\"cursor-value\"}\r\r" +
		"id: unfinished\nevent: ignored\ndata: partial"
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("one_byte_reads=%t", chunked), func(t *testing.T) {
			var reader io.Reader = strings.NewReader(input)
			if chunked {
				reader = &byteReader{data: []byte(input)}
			}
			var frames []sseFrame
			var retry time.Duration
			err := readSSE(reader, func(frame sseFrame) error { frames = append(frames, frame); return nil }, func(d time.Duration) { retry = d })
			if !errors.Is(err, io.EOF) || len(frames) != 2 || retry != 5*time.Second {
				t.Fatalf("frames=%d retry=%s err=%v", len(frames), retry, err)
			}
			if frames[0].data != "{\n\"title\":\"电影雪\"}" || frames[1].event != "cursor" {
				t.Fatalf("invalid frames: %#v", frames)
			}
		})
	}
}

func TestStreamCommitsOnlyDurableProgressAndPreservesUnknownPayload(t *testing.T) {
	sink := &testSink{cursor: "http-end"}
	failed := testEvent("failed-event", "transfer.failed", "failed")
	succeeded := testEvent("success-event", "transfer.succeeded", "succeeded")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events/stream" || r.Header.Get("Last-Event-ID") != "http-end" || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("SSE must resume from committed HTTP cursor")
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, ": heartbeat\n\nretry: 5000\n"+eventFrame(failed)+eventFrame(failed)+eventFrame(succeeded))
		_, _ = io.WriteString(w, "id: future\nevent: transfer.future\ndata: {\"id\":\"future\",\"type\":\"transfer.future\",\"schemaVersion\":2,\"futureField\":\"keep me\"}\n\n")
		_, _ = io.WriteString(w, cursorFrame("end-progress"))
	}))
	defer server.Close()
	client := makeClient(t, server, sink)
	err := client.stream(context.Background())
	var ce *ClientError
	if !errors.As(err, &ce) || !ce.Retryable {
		t.Fatalf("finite stream should reconnect, got %v", err)
	}
	if sink.cursor != "end-progress" || len(sink.events) != 3 || sink.events[1].Status != "succeeded" || sink.events[1].TargetFileID != "" {
		t.Fatalf("lost fail-to-success or missing-location event: %#v", sink.events)
	}
	if !strings.Contains(string(sink.events[2].Raw), "futureField") || sink.events[2].ReviewReason() == "" {
		t.Fatal("unknown schema was not preserved for review")
	}
	if !reflect.DeepEqual(sink.commits, []string{"", "", "", "", "end-progress"}) {
		t.Fatalf("business event advanced cursor prematurely: %q", sink.commits)
	}
	if client.Snapshot().LastEventAt == nil {
		t.Fatal("missing event observability")
	}
}

func TestPersistenceFailureStopsBeforeLaterEventsAndCursor(t *testing.T) {
	sink := &testSink{cursor: "committed", failID: "fail"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, eventFrame(testEvent("good", "transfer.succeeded", "succeeded"))+eventFrame(testEvent("fail", "transfer.succeeded", "succeeded"))+eventFrame(testEvent("later", "transfer.succeeded", "succeeded"))+cursorFrame("must-not-commit"))
	}))
	defer server.Close()
	err := makeClient(t, server, sink).stream(context.Background())
	var ce *ClientError
	if !errors.As(err, &ce) || ce.Code != "storage" || !ce.Retryable || sink.cursor != "committed" || len(sink.events) != 1 {
		t.Fatalf("failure skipped progress: err=%v cursor=%s count=%d", err, sink.cursor, len(sink.events))
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatal("underlying storage error leaked")
	}
}

func TestHTTPPersistenceFailureDoesNotCommitPageCursor(t *testing.T) {
	sink := &testSink{cursor: "committed", failID: "fail"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePage(w, []Event{testEvent("good", "transfer.succeeded", "succeeded"), testEvent("fail", "transfer.succeeded", "succeeded")}, "new", true)
	}))
	defer server.Close()
	if err := makeClient(t, server, sink).catchUp(context.Background()); err == nil || sink.cursor != "committed" || len(sink.events) != 0 {
		t.Fatal("failed HTTP transaction advanced progress")
	}
}

func TestRestartLoadsDurableCursorAndReplaysDuplicateSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sink.json")
	first := &testSink{path: path}
	var afters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		afters = append(afters, r.URL.Query().Get("after"))
		writePage(w, []Event{testEvent("same-event", "transfer.succeeded", "succeeded")}, "durable-progress", false)
	}))
	defer server.Close()
	if err := makeClient(t, server, first).catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Cursor string
		Events []Event
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	second := &testSink{path: path, cursor: saved.Cursor, events: saved.Events}
	if err := makeClient(t, server, second).catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afters, []string{"", "durable-progress"}) || len(second.events) != 1 {
		t.Fatalf("restart lost progress or dedup: afters=%q count=%d", afters, len(second.events))
	}
}

func TestAuthenticationCursorAndConfigurationResponsesStop(t *testing.T) {
	for _, status := range []int{401, 422, 403, 400, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "mock-secret-do-not-log response body")
			}))
			defer server.Close()
			sink := &testSink{cursor: "keep"}
			client := makeClient(t, server, sink)
			client.sleep = func(context.Context, time.Duration) error { t.Fatal("fatal response must not retry"); return nil }
			err := client.Run(context.Background())
			if err == nil || calls.Load() != 1 || sink.cursor != "keep" || client.Snapshot().State != "blocked" || strings.Contains(err.Error(), "mock-secret") {
				t.Fatalf("response not safely stopped: %v %#v", err, client.Snapshot())
			}
		})
	}
}

func TestRateLimitAndServiceRetryAfter(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "137")
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(401)
			}))
			defer server.Close()
			client := makeClient(t, server, &testSink{})
			var waits []time.Duration
			client.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			if err := client.Run(context.Background()); err == nil || calls.Load() != 2 || len(waits) != 1 || waits[0] < 137*time.Second {
				t.Fatalf("Retry-After ignored: calls=%d waits=%v err=%v", calls.Load(), waits, err)
			}
		})
	}
}

func TestBackoffBoundsAndDateRetryAfter(t *testing.T) {
	c := &Client{retryMin: 5 * time.Second, jitter: func() float64 { return 0 }}
	for attempt := range 20 {
		d := c.backoff(attempt, 0)
		if d < 5*time.Second || d > 120*time.Second {
			t.Fatalf("invalid backoff %s", d)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	if got := parseRetryAfter(now.Add(35*time.Second).Format(http.TimeFormat), now); got != 35*time.Second {
		t.Fatalf("date Retry-After=%s", got)
	}
}

func TestCancellationClosesStreamAndPreventsConcurrentRun(t *testing.T) {
	connected, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			writePage(w, []Event{}, "start", false)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		close(connected)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	client := makeClient(t, server, &testSink{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not connect")
	}
	if err := client.Run(ctx); err == nil {
		t.Fatal("concurrent consumer was allowed")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil || client.Snapshot().State != "stopped" {
			t.Fatalf("unclean shutdown: %v %#v", err, client.Snapshot())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation failed to interrupt stream read")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("server stream remained open")
	}
}

func TestPollingWaitsAndDoesNotOpenSSE(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			t.Error("polling opened SSE")
		}
		calls.Add(1)
		writePage(w, []Event{}, "poll-progress", false)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "mock", Mode: "poll", PollInterval: time.Second}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.sleep = func(_ context.Context, d time.Duration) error {
		if d < 10*time.Second {
			t.Error("poll delay below ten seconds")
		}
		cancel()
		return ctx.Err()
	}
	if err := client.Run(ctx); err != nil || calls.Load() != 1 {
		t.Fatalf("incorrect polling lifecycle: %v calls=%d", err, calls.Load())
	}
}

func TestRejectsUnsafeURLAndRedirects(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?key=secret", "file:///tmp/events"} {
		if _, err := NormalizeBaseURL(raw); err == nil {
			t.Fatalf("unsafe base URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://EXAMPLE.com/base/", "http://127.0.0.1:1234", "http://[::1]:1234"} {
		if _, err := NormalizeBaseURL(raw); err != nil {
			t.Fatalf("safe base URL rejected: %s", raw)
		}
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	err := makeClient(t, server, &testSink{}).Run(context.Background())
	if err == nil || redirected.Load() != 0 {
		t.Fatal("redirect leaked authenticated request")
	}
}

func TestInvalidSSEFramesAndHTMLNeverAdvanceProgress(t *testing.T) {
	cases := map[string]string{
		"mismatched ID":   "id: wrong\nevent: transfer.succeeded\ndata: {\"id\":\"event\",\"type\":\"transfer.succeeded\"}\n\n",
		"invalid JSON":    "id: event\nevent: transfer.succeeded\ndata: {\n\n",
		"cursor mismatch": "id: wrong\nevent: cursor\ndata: {\"cursor\":\"different\"}\n\n",
		"invalid UTF8":    "id: event\nevent: transfer.succeeded\ndata: \xff\n\n",
		"HTML page":       "<html>error</html>",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if name == "HTML page" {
					w.Header().Set("Content-Type", "text/html")
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			sink := &testSink{cursor: "keep"}
			err := makeClient(t, server, sink).stream(context.Background())
			var ce *ClientError
			if !errors.As(err, &ce) || ce.Code != "protocol" || sink.cursor != "keep" || len(sink.events) != 0 {
				t.Fatalf("invalid frame advanced state: %v", err)
			}
		})
	}
}

func TestIdleReadClosesSilentConnection(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := idleBody{ReadCloser: reader, timeout: 30 * time.Millisecond}
	done := make(chan error, 1)
	go func() { _, err := body.Read(make([]byte, 1)); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("idle reader was not interrupted")
		}
	case <-time.After(time.Second):
		t.Fatal("idle timeout failed")
	}
}
