package connectors

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 运行日志级别（LogEntry.Level）。
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// 运行日志方向（LogEntry.Direction）：
//   - in：入站消息处理路径（接收/白名单拒绝/去重跳过/入队/处理失败）；
//   - out：出站回复路径（投递成功/投递失败）；
//   - system：生命周期与运行归属（启动/停机/归属闸跳过）。
const (
	LogDirectionIn     = "in"
	LogDirectionOut    = "out"
	LogDirectionSystem = "system"
)

// 日志环与查询边界常量。
const (
	// DefaultLogBufferSize 是每连接器日志环的默认容量（Deps.LogBufferSize 可覆盖）。
	DefaultLogBufferSize = 200
	// DefaultLogQueryLimit 是 Service.Logs(limit<=0) 的默认返回条数。
	DefaultLogQueryLimit = 50
	// MaxLogQueryLimit 是 Service.Logs 单次返回条数上限（更大的 limit 收敛到该值）。
	MaxLogQueryLimit = 200
	// logSummaryLimit 是 LogEntry.Summary 的最大字符（rune）数，超长直接截断（不加省略号）。
	logSummaryLimit = 200
)

// LogEntry 是一条连接器运行日志。
//
// 存储约定（docs/bots/04-migration-roadmap.md FR-12）：
//   - 仅存于进程内（每连接器环形缓冲，重启即清零，不落盘）；
//   - Summary 由调用方拼装，须保证不含密钥/凭据明文（provider 侧已对错误文本脱敏）；
//     本包只做 trim 与 ~200 字符截断，不做二次脱敏；
//   - Chat 为平台会话标识，连接器级系统事件可为空。
type LogEntry struct {
	Time        time.Time `json:"time"`
	Level       string    `json:"level"`     // debug|info|warn|error
	Direction   string    `json:"direction"` // in|out|system
	ConnectorID string    `json:"connector_id"`
	Chat        string    `json:"chat,omitempty"`
	Summary     string    `json:"summary"`
}

// ConnectorStats 是每连接器的累计运行计数与最近错误（进程内，重启清零）。
type ConnectorStats struct {
	// Inbound 是入站消息成功入队数（白名单拒绝/队列满不计）。
	Inbound int `json:"inbound"`
	// Outbound 是入站消息处理完成后成功投递的回复数。
	Outbound int `json:"outbound"`
	// Errors 是 error 级事件数（后端失败、投递失败、队列满等）。
	Errors int `json:"errors"`
	// DedupSkipped 是重复投递被幂等账本跳过的次数。
	DedupSkipped int `json:"dedup_skipped"`
	// QueueFull 是会话队列满被拒绝的次数（宿主应对平台回 429）。
	QueueFull int `json:"queue_full"`
	// LastError/LastErrorAt 是最近一次 error 级事件的内容与时间（零值 = 尚无错误）。
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at"`
	// LastActivityAt 是最近一次入/出站方向日志的时间（含被拒绝/跳过的入站事件）。
	LastActivityAt time.Time `json:"last_activity_at"`
}

// logCounters 是单条日志附带的计数增量（每个维度至多计一次）。
type logCounters struct {
	inbound      bool
	outbound     bool
	dedupSkipped bool
	queueFull    bool
}

// connectorLog 是单个连接器的日志环 + 计数。
//
// 并发约定：本结构使用独立 mutex，与 Service.mu 不存在锁序——Service 只在释放
// 自身锁之后调用本结构，且持有本锁期间绝不回调 Service/Store。
//
// 生命周期说明：日志环只存在于进程内（Service 内存）。连接器配置删除
// （Store.DeleteConfig）不清理对应日志环：每 id 上限 LogBufferSize 条，进程内
// 可容忍，且能保留删除前的排查线索；进程重启即整体清零。
type connectorLog struct {
	mu    sync.Mutex
	ring  []LogEntry
	next  int
	full  bool
	stats ConnectorStats
}

func newConnectorLog(capacity int) *connectorLog {
	if capacity <= 0 {
		capacity = DefaultLogBufferSize
	}
	return &connectorLog{ring: make([]LogEntry, capacity)}
}

// add 追加一条日志并应用计数增量；环满时覆盖最旧条目。
func (l *connectorLog) add(entry LogEntry, counters logCounters) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring[l.next] = entry
	l.next = (l.next + 1) % len(l.ring)
	if l.next == 0 {
		l.full = true
	}
	if counters.inbound {
		l.stats.Inbound++
	}
	if counters.outbound {
		l.stats.Outbound++
	}
	if counters.dedupSkipped {
		l.stats.DedupSkipped++
	}
	if counters.queueFull {
		l.stats.QueueFull++
	}
	if entry.Direction == LogDirectionIn || entry.Direction == LogDirectionOut {
		l.stats.LastActivityAt = entry.Time
	}
	// LastError/LastErrorAt 仅在 error 级别更新（warn 只留日志，不污染最近错误）。
	if entry.Level == LogLevelError {
		l.stats.Errors++
		l.stats.LastError = entry.Summary
		l.stats.LastErrorAt = entry.Time
	}
}

// snapshot 返回最新 limit 条（倒序，最新在前）；limit 由调用方按环容量收敛。
func (l *connectorLog) snapshot(limit int) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := l.next
	if l.full {
		size = len(l.ring)
	}
	if size == 0 {
		return []LogEntry{}
	}
	if limit > size {
		limit = size
	}
	out := make([]LogEntry, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (l.next - 1 - i + len(l.ring)) % len(l.ring)
		out = append(out, l.ring[idx])
	}
	return out
}

func (l *connectorLog) statsSnapshot() ConnectorStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// recordLog 把一条日志写入对应连接器的日志环（环按需惰性创建）。
// 并发安全：仅在 logMu 内取环（可能建环），环内数据由环自身 mutex 保护；
// 全程不获取 Service.mu，避免与业务锁形成锁序。
func (s *Service) recordLog(entry LogEntry, counters logCounters) {
	if s == nil {
		return
	}
	id := strings.TrimSpace(entry.ConnectorID)
	if id == "" {
		return
	}
	entry.ConnectorID = id
	entry.Chat = strings.TrimSpace(entry.Chat)
	entry.Summary = truncateLogSummary(entry.Summary)
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	s.logMu.Lock()
	if s.logBuffers == nil {
		s.logBuffers = make(map[string]*connectorLog)
	}
	ring := s.logBuffers[id]
	if ring == nil {
		ring = newConnectorLog(s.logBufferSize())
		s.logBuffers[id] = ring
	}
	s.logMu.Unlock()
	ring.add(entry, counters)
}

func (s *Service) logBufferSize() int {
	if s.deps.LogBufferSize > 0 {
		return s.deps.LogBufferSize
	}
	return DefaultLogBufferSize
}

func truncateLogSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) <= logSummaryLimit {
		return summary
	}
	return string([]rune(summary)[:logSummaryLimit])
}
