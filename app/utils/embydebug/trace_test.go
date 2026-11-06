package embydebug

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type captureLogger struct {
	lines []string
}

func (capture *captureLogger) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

func TestCopyRetainsTraceWithoutCancellingBackgroundWork(t *testing.T) {
	capture := &captureLogger{}
	parent, cancel := context.WithCancel(context.Background())
	ctx := Start(parent, capture)
	background := Copy(context.Background(), ctx)
	cancel()
	if ctx.Err() != context.Canceled || background.Err() != nil {
		t.Fatal("diagnostics changed request/background cancellation")
	}
	Logf(ctx, "identity", "user_id=%q", "user-1")
	Logf(background, "decision", "actual_storage=%d", 12)
	Logf(context.Background(), "unrelated", "must not be logged")
	Logf(Start(context.Background(), capture), "request", "new playback")
	if len(capture.lines) != 3 {
		t.Fatalf("unexpected diagnostic count: %d", len(capture.lines))
	}
	first := strings.Fields(capture.lines[0])[2]
	if !strings.Contains(capture.lines[1], first) || strings.Contains(capture.lines[2], first) {
		t.Fatal("playback trace lost or reused across requests")
	}
}
