package schedule

import "sync/atomic"

// metrics 收集调度运行时的累计计数（P6 观测收敛）。全部原子操作，无锁读。
//
// 口径（与主方案 §11/§12 对齐）：
//   - DeliveredTotal：投递成功次数（delegate 成功 或 内嵌成功，按任务数计）
//   - DeliveryFailedTotal：投递失败次数（delegate 失败且未走内嵌，按组计）
//   - EmbeddedTotal：内嵌执行成功次数
//   - EmbeddedSkippedTotal：内嵌被租约仲裁拒绝次数（会话被宿主持有）
//   - RetriesScheduled：退避重排次数（markFailed 调用数）
//   - DeadLetterTotal：达到 MaxDeliveryAttempts 的任务数（转 inactive 留痕）
type metrics struct {
	deliveredTotal       atomic.Int64
	deliveryFailedTotal  atomic.Int64
	embeddedTotal        atomic.Int64
	embeddedSkippedTotal atomic.Int64
	retriesScheduled     atomic.Int64
	deadLetterTotal      atomic.Int64
}

// MetricsSnapshot 是 metrics 的只读快照（JSON/运维端点直接序列化）。
type MetricsSnapshot struct {
	DeliveredTotal       int64 `json:"deliveredTotal"`
	DeliveryFailedTotal  int64 `json:"deliveryFailedTotal"`
	EmbeddedTotal        int64 `json:"embeddedTotal"`
	EmbeddedSkippedTotal int64 `json:"embeddedSkippedTotal"`
	RetriesScheduled     int64 `json:"retriesScheduled"`
	DeadLetterTotal      int64 `json:"deadLetterTotal"`
}

// Snapshot 返回当前计数快照。
func (m *metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		DeliveredTotal:       m.deliveredTotal.Load(),
		DeliveryFailedTotal:  m.deliveryFailedTotal.Load(),
		EmbeddedTotal:        m.embeddedTotal.Load(),
		EmbeddedSkippedTotal: m.embeddedSkippedTotal.Load(),
		RetriesScheduled:     m.retriesScheduled.Load(),
		DeadLetterTotal:      m.deadLetterTotal.Load(),
	}
}
