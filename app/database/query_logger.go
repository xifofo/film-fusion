package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"film-fusion/app/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type queryLogger struct {
	log   *logger.Logger
	level gormlogger.LogLevel
	slow  time.Duration
	pool  *sql.DB
}

func newQueryLogger(log *logger.Logger, slow time.Duration) *queryLogger {
	return &queryLogger{log: log, level: gormlogger.Warn, slow: slow}
}
func (l *queryLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	copy := *l
	copy.level = level
	return &copy
}
func (l *queryLogger) Info(_ context.Context, message string, args ...interface{}) {
	if l.log != nil && l.level >= gormlogger.Info {
		l.log.Infof(message, args...)
	}
}
func (l *queryLogger) Warn(_ context.Context, message string, args ...interface{}) {
	if l.log != nil && l.level >= gormlogger.Warn {
		l.log.Warnf(message, args...)
	}
}
func (l *queryLogger) Error(_ context.Context, message string, args ...interface{}) {
	if l.log != nil && l.level >= gormlogger.Error {
		l.log.Errorf(message, args...)
	}
}
func (l *queryLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.log == nil || l.level == gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	failed := err != nil && !errors.Is(err, gorm.ErrRecordNotFound)
	if (!failed || l.level < gormlogger.Error) && (elapsed < l.slow || l.level < gormlogger.Warn) {
		return
	}
	sql, rows := fc()
	operation := "unknown"
	if fields := strings.Fields(sql); len(fields) > 0 {
		switch strings.ToUpper(fields[0]) {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "PRAGMA":
			operation = strings.ToUpper(fields[0])
		}
	}
	// Do not log SQL text or parameters: configuration and account rows contain
	// credentials. Include the application caller and pool wait counters instead.
	detail := fmt.Sprintf("operation=%s elapsed_ms=%d rows=%d caller=%s", operation, elapsed.Milliseconds(), rows, queryCaller())
	if l.pool != nil {
		stats := l.pool.Stats()
		detail += fmt.Sprintf(" pool_wait_count=%d pool_wait_ms=%d", stats.WaitCount, stats.WaitDuration.Milliseconds())
	}
	if failed {
		l.log.Errorf("database query failed: %s error=%v", detail, err)
		return
	}
	l.log.Warnf("slow database query: %s", detail)
}

func queryCaller() string {
	var pcs [24]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		frame, more := frames.Next()
		if strings.HasPrefix(frame.Function, "film-fusion/") && !strings.HasSuffix(frame.File, "/query_logger.go") {
			return fmt.Sprintf("%s:%d", frame.Function, frame.Line)
		}
		if !more {
			return "unknown"
		}
	}
}
