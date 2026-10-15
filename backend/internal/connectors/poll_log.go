package connectors

import (
	"context"
	"fmt"
)

// 轮询期日志汇（2026-10-15 微信真机复盘）：provider 的断线/会话过期等运行期
// 错误此前只写进程日志（provider 自己的 logf），连接器页面的运行日志环看不到
// 「已启动轮询」之后的任何异常——页面全绿、bot 实际失联（微信 errcode=-14
// 持续 20 分钟无人察觉，只因为错误没进日志环）。宿主在启动轮询时经 ctx 注入
// 日志汇，provider 用 PollingLogf 汇报，统一进日志环与「最近错误」。
//
// 设计约束：
//   - ctx 承载（不是 provider 字段）：同一 provider 实例可服务多个连接器，
//     按次注入天然按连接器隔离；
//   - 无汇时静默 no-op：provider 单测与独立运行不受影响（进程日志仍由各自的
//     logf 承担，不改变既有行为）。

// PollLogSink 接收 provider 轮询期的日志：level 取 LogLevel* 词表，summary 须
// 已脱敏（不含令牌/凭据明文）。
type PollLogSink func(level, summary string)

// pollLogSinkKey 是日志汇的 ctx 键（非导出：仅本包经 WithPollLogSink 注入）。
type pollLogSinkKey struct{}

// WithPollLogSink 把日志汇注入 ctx（宿主专用；ctx/sink 为 nil 时原样返回，幂等）。
func WithPollLogSink(ctx context.Context, sink PollLogSink) context.Context {
	if ctx == nil || sink == nil {
		return ctx
	}
	return context.WithValue(ctx, pollLogSinkKey{}, sink)
}

// PollingLogf 供 provider 在轮询/长连接循环中汇报运行期事件：注入过日志汇时经
// 汇写连接器日志环，否则静默 no-op。format/args 语义同 fmt.Sprintf。
func PollingLogf(ctx context.Context, level, format string, args ...any) {
	if ctx == nil || format == "" {
		return
	}
	sink, _ := ctx.Value(pollLogSinkKey{}).(PollLogSink)
	if sink == nil {
		return
	}
	summary := format
	if len(args) > 0 {
		summary = fmt.Sprintf(format, args...)
	}
	sink(level, summary)
}
