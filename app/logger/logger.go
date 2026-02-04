package logger

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"film-fusion/app/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger 包装 zap.Logger
type Logger struct {
	*zap.Logger
	sugar      *zap.SugaredLogger
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// New 使用给定配置创建新的日志记录器实例
func New(cfg config.LogConfig) *Logger {
	// 设置日志级别
	level := zapcore.InfoLevel
	switch cfg.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	case "fatal":
		level = zapcore.FatalLevel
	}

	// 设置编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05"),
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 设置编码器
	var encoder zapcore.Encoder
	if cfg.Format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		// 为文本格式设置更友好的编码器配置
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// 设置输出
	var writeSyncer zapcore.WriteSyncer
	var core zapcore.Core

	switch cfg.Output {
	case "stdout":
		writeSyncer = zapcore.AddSync(os.Stdout)
		core = zapcore.NewCore(encoder, writeSyncer, level)
	case "file":
		// 确保日志目录存在
		logDir := filepath.Dir("data/logs/app.log")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			panic("创建日志目录失败: " + err.Error())
		}

		// 获取当前日期作为日志文件名的一部分
		currentDate := time.Now().Format("2006-01-02")
		logFileName := filepath.Join(logDir, currentDate+".log")

		// 配置 lumberjack 进行日志轮转
		lumberjackLogger := &lumberjack.Logger{
			Filename:   logFileName,
			MaxSize:    cfg.MaxSize,    // 兆字节
			MaxBackups: cfg.MaxBackups, // 备份数量
			MaxAge:     cfg.MaxAge,     // 天数
			Compress:   cfg.Compress,   // 压缩旧文件
		}

		fileWriter := zapcore.AddSync(lumberjackLogger)

		// 在调试模式下同时写入文件和标准输出
		if cfg.Level == "debug" {
			// 为控制台使用彩色编码器
			consoleEncoderConfig := encoderConfig
			consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
			consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)

			fileCore := zapcore.NewCore(encoder, fileWriter, level)
			consoleCore := zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level)
			core = zapcore.NewTee(fileCore, consoleCore)
		} else {
			core = zapcore.NewCore(encoder, fileWriter, level)
		}

		// 启动定时任务，每天凌晨检查是否需要创建新的日志文件
		ctx, cancel := context.WithCancel(context.Background())
		logger := &Logger{
			Logger:     zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)),
			cancelFunc: cancel,
		}
		logger.sugar = logger.Logger.Sugar()

		logger.wg.Add(1)
		go logger.dailyRotateRoutine(ctx, lumberjackLogger, logDir)

		return logger
	default:
		writeSyncer = zapcore.AddSync(os.Stdout)
		core = zapcore.NewCore(encoder, writeSyncer, level)
	}

	// 创建 logger
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &Logger{
		Logger: logger,
		sugar:  logger.Sugar(),
	}
}

// dailyRotateRoutine 每日日志轮转的后台任务
func (l *Logger) dailyRotateRoutine(ctx context.Context, lumberjackLogger *lumberjack.Logger, logDir string) {
	defer l.wg.Done()

	for {
		now := time.Now()
		nextDay := now.AddDate(0, 0, 1)
		nextDay = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), 0, 0, 0, 0, nextDay.Location())
		sleepDuration := nextDay.Sub(now)

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepDuration + time.Second): // 增加 1 秒缓冲确保跨过凌晨
			// 创建新的日志文件
			newDate := nextDay.Format("2006-01-02")
			newLogFileName := filepath.Join(logDir, newDate+".log")
			lumberjackLogger.Filename = newLogFileName
			// 强制关闭当前文件，以便下次写入时打开新文件
			_ = lumberjackLogger.Close()
		}
	}
}

// Close 关闭 logger 并等待后台任务完成
func (l *Logger) Close() error {
	if l.cancelFunc != nil {
		l.cancelFunc()
		l.wg.Wait()
	}
	return l.Logger.Sync()
}

// Sugar 返回 SugaredLogger 实例，提供更灵活的日志记录
func (l *Logger) Sugar() *zap.SugaredLogger {
	return l.sugar
}

// WithField 向日志记录器添加字段
func (l *Logger) WithField(key string, value interface{}) *zap.Logger {
	return l.Logger.With(zap.Any(key, value))
}

// WithFields 向日志记录器添加多个字段
func (l *Logger) WithFields(fields map[string]interface{}) *zap.Logger {
	zapFields := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}
	return l.Logger.With(zapFields...)
}

// WithError 向日志记录器添加错误字段
func (l *Logger) WithError(err error) *zap.Logger {
	return l.Logger.With(zap.Error(err))
}

// 便捷方法，使用 SugaredLogger 的格式化功能
func (l *Logger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

func (l *Logger) Infof(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

func (l *Logger) Warnf(template string, args ...interface{}) {
	l.sugar.Warnf(template, args...)
}

func (l *Logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

func (l *Logger) Fatalf(template string, args ...interface{}) {
	l.sugar.Fatalf(template, args...)
}

// 便捷方法，使用结构化日志
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	l.Logger.Debug(msg, fields...)
}

func (l *Logger) Info(msg string, fields ...zap.Field) {
	l.Logger.Info(msg, fields...)
}

func (l *Logger) Warn(msg string, fields ...zap.Field) {
	l.Logger.Warn(msg, fields...)
}

func (l *Logger) Error(msg string, fields ...zap.Field) {
	l.Logger.Error(msg, fields...)
}

func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	l.Logger.Fatal(msg, fields...)
}

// Sync 刷新缓冲区
func (l *Logger) Sync() error {
	return l.Logger.Sync()
}
