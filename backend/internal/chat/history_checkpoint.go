package chat

import (
	"sync/atomic"
	"time"
)

// DefaultSessionCheckpointInterval 是长 turn 中途落库的默认节流间隔：turn 内
// 至多每 15s 把已提交的 durable 历史增量写回会话存储，使权威历史（SQLite /
// 会话列表 / resume）在长 turn 运行期间持续前进，而不是等 turn 结束才落地。
//
// 该常量与 CheckpointWindow 是「长 turn 中途落库」这条链路的唯一策略来源：
// aicli chat actor（SessionActor.checkpointSessionHistory）与 runtime HTTP
// agent-chat（internal/api/skills 的 agentChatHistoryCheckpointer）共用同一份
// 窗口实现，避免两条入口各自演化出不同的及时性语义。
const DefaultSessionCheckpointInterval = 15 * time.Second

// resolveSessionCheckpointInterval 解析配置的 checkpoint 间隔：0 使用默认值，
// 负数原样返回（调用方据此禁用中途落库），正数原样使用。
func resolveSessionCheckpointInterval(configured time.Duration) time.Duration {
	if configured == 0 {
		return DefaultSessionCheckpointInterval
	}
	return configured
}

// CheckpointWindow 是「长 turn 中途落库」的节流窗口（并发安全）。
//
// 语义：
//   - Reserve 在一个窗口内只放行一次写入；放行后返回 undo，调用方写入失败时
//     调用它回退窗口，让下一个提交点立即重试而不是再等一个窗口；
//   - interval <= 0 表示显式禁用（Reserve 永远返回 false），此时 turn 结束的
//     post-turn sync 仍是最终一致性保证；
//   - 窗口只在「真正尝试写入」时前进，未占用窗口的提交点不产生副作用；
//   - undo 幂等且只回退到占用前的时间戳，不会覆盖更晚的窗口。
type CheckpointWindow struct {
	interval time.Duration
	last     atomic.Int64
}

// NewCheckpointWindow 按间隔构造节流窗口；interval<=0 表示禁用中途落库。
func NewCheckpointWindow(interval time.Duration) *CheckpointWindow {
	return &CheckpointWindow{interval: interval}
}

// Enabled 报告中途落库是否启用。
func (w *CheckpointWindow) Enabled() bool {
	return w != nil && w.interval > 0
}

// Interval 返回节流间隔（nil 或未配置时为 0）。
func (w *CheckpointWindow) Interval() time.Duration {
	if w == nil {
		return 0
	}
	return w.interval
}

// Reserve 尝试占用当前节流窗口。ok=false 表示窗口内已放过一次写入，调用方应
// 跳过本次落库；undo 仅在 ok=true 时非 nil。
func (w *CheckpointWindow) Reserve() (undo func(), ok bool) {
	if !w.Enabled() {
		return nil, false
	}
	return reserveCheckpointWindow(&w.last, w.interval)
}

// Reset 清空节流窗口（turn 边界复用与测试使用）。
func (w *CheckpointWindow) Reset() {
	if w == nil {
		return
	}
	w.last.Store(0)
}

// reserveCheckpointWindow 是节流窗口的唯一实现：last 保存上一次被占用的时间
// （UnixNano，0 表示尚未占用）。调用方在写入失败时执行 undo 回退时间戳。
func reserveCheckpointWindow(last *atomic.Int64, interval time.Duration) (undo func(), ok bool) {
	if last == nil || interval <= 0 {
		return nil, false
	}
	now := time.Now().UnixNano()
	prev := last.Load()
	if prev != 0 && now-prev < int64(interval) {
		// 窗口内已写过一次：跳过，turn 收尾仍会做最终落库。
		return nil, false
	}
	if !last.CompareAndSwap(prev, now) {
		// 另一个提交点已占用本窗口，跳过本次写入。
		return nil, false
	}
	return func() { last.Store(prev) }, true
}
