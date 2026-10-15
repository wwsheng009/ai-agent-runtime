package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 本文件实现"交付账本"（S2，见 docs/automation-tasks/mesh-scheduler-federation-design.md §12.1）：
// 以 occurrence_key 为单位的跨进程投递去重。
//
// 背景：投递顺序是"先投递、后 commit"（runtime.drive）。leader 在两者之间
// 崩溃/失租时，新 leader 会重扫同一条 occurrence 并再次投递。接收侧（宿主 hub /
// HTTP 命令端点）在受理前先查账本、受理后落账，即可把重复窗口收敛掉。

// OccurrenceKey 是跨 leader 去重的交付键：schedule_id + 发生时刻。
//
// 时刻统一**截断到毫秒**（UTC）：store 的 instantLayout 就是毫秒精度，
// 投递路径上的 occurrence 可能来自"落库前的内存值"或"落库后扫描值"，
// 不截断会出现同一 occurrence 两个键的假阴性（2026-10-14 单测实测）。
func OccurrenceKey(scheduleID string, occurrence time.Time) string {
	return strings.TrimSpace(scheduleID) + "@" + occurrence.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
}

// ScheduleIDFromOccurrenceKey 从交付键反解 schedule_id（无分隔符时原样返回）。
func ScheduleIDFromOccurrenceKey(key string) string {
	key = strings.TrimSpace(key)
	if idx := strings.IndexByte(key, '@'); idx >= 0 {
		return key[:idx]
	}
	return key
}

// DeliveryEnvelope 是一次投递的完整上下文。
type DeliveryEnvelope struct {
	SessionID string
	Text      string
	// Occurrences 是本条消息覆盖的 occurrence 键（单条一项；批次多项）。
	// 空表示无账本语义（旧调用方 / 纯文本路径）。
	Occurrences []string
}

// EnvelopeDeliverer 是 Deliverer 的增强形态：携带 occurrence 键，接收侧可去重。
// 未实现时调用方退回 Deliver（无账本保护，语义与旧版一致）。
type EnvelopeDeliverer interface {
	DeliverEnvelope(ctx context.Context, env DeliveryEnvelope) error
}

// DeliverEnvelopeTo 是投递的统一入口：优先 EnvelopeDeliverer，否则退回 legacy Deliver。
func DeliverEnvelopeTo(ctx context.Context, d Deliverer, env DeliveryEnvelope) error {
	if d == nil {
		return fmt.Errorf("schedule: deliverer is required")
	}
	if ed, ok := d.(EnvelopeDeliverer); ok {
		return ed.DeliverEnvelope(ctx, env)
	}
	return d.Deliver(ctx, env.SessionID, env.Text)
}

// OccurrenceLedger 是交付账本的最小读写面（SQLiteStore 实现）。
type OccurrenceLedger interface {
	// AllOccurrencesDelivered 报告 keys 是否全部已送达（空列表恒 false：无键不做去重）。
	AllOccurrencesDelivered(keys []string) (bool, error)
	// MarkOccurrencesDelivered 幂等落账（重复写只刷新 updated_at）。
	MarkOccurrencesDelivered(keys []string, now time.Time) error
}

// GuardedDeliverer 把交付账本套在**接收侧**投递器外面（§12.1）：
//   - 全部 occurrence 已送达 → 直接回执 nil（不重复入 actor）；
//   - 否则先提交、成功后落账——顺序不可颠倒：先落账会让"落账后提交失败"
//     变成丢消息；先提交最多把"已受理未落账"留给下一次重试（有界重复）。
type GuardedDeliverer struct {
	Inner  Deliverer
	Ledger OccurrenceLedger
	Now    func() time.Time
	Logf   func(format string, args ...any)
}

// Deliver 是无键透传（旧的纯文本路径不做账本判断）。
func (g GuardedDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	if g.Inner == nil {
		return fmt.Errorf("schedule: guarded deliverer has no inner deliverer")
	}
	return g.Inner.Deliver(ctx, sessionID, text)
}

// DeliverEnvelope 按账本语义投递（见类型注释）。
func (g GuardedDeliverer) DeliverEnvelope(ctx context.Context, env DeliveryEnvelope) error {
	if g.Inner == nil {
		return fmt.Errorf("schedule: guarded deliverer has no inner deliverer")
	}
	keys := cleanOccurrenceKeys(env.Occurrences)
	if g.Ledger != nil && len(keys) > 0 {
		if delivered, err := g.Ledger.AllOccurrencesDelivered(keys); err == nil && delivered {
			return nil
		}
	}
	if err := DeliverEnvelopeTo(ctx, g.Inner, env); err != nil {
		return err
	}
	if g.Ledger != nil && len(keys) > 0 {
		now := time.Now().UTC()
		if g.Now != nil {
			now = g.Now()
		}
		if err := g.Ledger.MarkOccurrencesDelivered(keys, now); err != nil && g.Logf != nil {
			g.Logf("schedule: mark occurrences delivered failed: %v", err)
		}
	}
	return nil
}

// cleanOccurrenceKeys 去空白、去重、保序。
func cleanOccurrenceKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}
