// Package embydebug 为一次 Emby 播放的身份识别、绑定和账号决策关联 debug 日志。
// 调用方只传入状态码、内部 ID 和固定原因，不传凭据、完整 URL 或上游响应体。
package embydebug

import (
	"context"
	"crypto/rand"
)

// Logger 复用应用的 debug 级别过滤；诊断本身不改变全局日志级别。
type Logger interface {
	Debugf(string, ...interface{})
}

type traceKey struct{}

type trace struct {
	id  string
	log Logger
}

// Start 为本次播放创建独立编号，保留原 context 的取消和截止时间。
func Start(ctx context.Context, log Logger) context.Context {
	return context.WithValue(ctx, traceKey{}, trace{id: rand.Text(), log: log})
}

// Copy 只复制诊断关联，不复制源 context 的取消信号。
// 播放决策原先使用 Background 时，调用方仍须显式传入 Background 保持原生命周期。
func Copy(dst, src context.Context) context.Context {
	if value, ok := src.Value(traceKey{}).(trace); ok {
		return context.WithValue(dst, traceKey{}, value)
	}

	return dst
}

// Logf 记录可按 trace 搜索的阶段日志；非播放调用或未提供 logger 时不输出。
func Logf(ctx context.Context, stage, format string, args ...interface{}) {
	value, ok := ctx.Value(traceKey{}).(trace)
	if !ok || value.log == nil {
		return
	}

	values := append([]interface{}{value.id, stage}, args...)
	value.log.Debugf("[EMBY PLAYBACK] trace=%s stage=%s "+format, values...)
}
