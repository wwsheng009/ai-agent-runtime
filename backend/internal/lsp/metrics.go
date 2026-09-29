package lsp

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// 请求级读数（docs/plan/lsp-observability-and-analysis-plan-20260929.md §3.3）。
//
// 只记录标量：触发方式 / 结果 / 耗时 / 诊断条数 / 追加字节。源码正文、
// 诊断文本与参数一律不进入读数；P50/P95 由固定容量环形样本复算，容量外的
// 最旧样本被覆盖（读数以 latency_samples 明示样本量，不冒充全量）。
const (
	// metricsRecentCap 保留的最近请求明细条数（web「最近事件」表的数据源）。
	metricsRecentCap = 64
	// metricsLatencyCap 延迟分位样本上限。
	metricsLatencyCap = 512
)

// RequestRecord 是单次后写请求的低敏事实（metrics 与 web /events 共用同一形状）。
type RequestRecord struct {
	Time           time.Time `json:"time"`
	Trigger        string    `json:"trigger"`
	Server         string    `json:"server,omitempty"`
	Outcome        string    `json:"outcome"`
	DurationMS     int64     `json:"duration_ms"`
	DiagCount      int       `json:"diag_count"`
	AppendedBytes  int       `json:"appended_bytes"`
	OmittedItems   int       `json:"omitted_items,omitempty"`
	OmittedByChars int       `json:"omitted_by_chars,omitempty"`
}

// MetricsSnapshot 是读数快照：比率分母口径固定为 requests，除零返回 0。
type MetricsSnapshot struct {
	Requests         int             `json:"requests"`
	InlineAttempts   int             `json:"inline_attempts"`
	ToolRequests     int             `json:"tool_requests"`
	Injected         int             `json:"injected"`
	Clean            int             `json:"clean"`
	NoServer         int             `json:"no_server"`
	Degraded         int             `json:"degraded"`
	DiagHit          int             `json:"diag_hit"`
	DiagHitRatio     float64         `json:"diag_hit_ratio"`
	FallbackRatio    float64         `json:"fallback_ratio"`
	WaitLatencyP50MS int64           `json:"wait_latency_p50_ms"`
	WaitLatencyP95MS int64           `json:"wait_latency_p95_ms"`
	LatencySamples   int             `json:"latency_samples"`
	DiagCount        int64           `json:"diag_count"`
	AppendedBytes    int64           `json:"appended_bytes"`
	ByOutcome        map[string]int  `json:"by_outcome,omitempty"`
	LastRequestAt    *time.Time      `json:"last_request_at,omitempty"`
	RecentRequests   []RequestRecord `json:"recent_requests"`
}

// Metrics 累加请求级读数；并发由 mu 保护。
type Metrics struct {
	mu            sync.Mutex
	requests      int
	inline        int
	tool          int
	injected      int
	clean         int
	noServer      int
	degraded      int
	diagHit       int
	byOutcome     map[string]int
	latency       []int64
	diagCount     int64
	appendedBytes int64
	lastAt        time.Time
	recent        []RequestRecord
}

// NewMetrics 创建空读数。
func NewMetrics() *Metrics {
	return &Metrics{byOutcome: map[string]int{}}
}

// Observe 记录一次请求（由 Bridge 调用；测试可直接驱动）。
func (m *Metrics) Observe(record RequestRecord) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	switch record.Trigger {
	case "inline":
		m.inline++
	case "tool":
		m.tool++
	}
	if m.byOutcome == nil {
		m.byOutcome = map[string]int{}
	}
	m.byOutcome[record.Outcome]++
	switch {
	case record.Outcome == "injected":
		m.injected++
		if record.DiagCount > 0 {
			m.diagHit++
		}
	case record.Outcome == "clean":
		m.clean++
	case record.Outcome == "no_server":
		m.noServer++
	case strings.HasPrefix(record.Outcome, "degraded"):
		m.degraded++
	}
	m.diagCount += int64(record.DiagCount)
	m.appendedBytes += int64(record.AppendedBytes)
	m.latency = append(m.latency, record.DurationMS)
	if len(m.latency) > metricsLatencyCap {
		m.latency = m.latency[len(m.latency)-metricsLatencyCap:]
	}
	m.recent = append(m.recent, record)
	if len(m.recent) > metricsRecentCap {
		m.recent = m.recent[len(m.recent)-metricsRecentCap:]
	}
	if !record.Time.IsZero() {
		m.lastAt = record.Time
	}
}

// Snapshot 返回只读快照；RecentRequests 按时间倒序（最新在前，便于表格渲染）。
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := MetricsSnapshot{
		Requests:         m.requests,
		InlineAttempts:   m.inline,
		ToolRequests:     m.tool,
		Injected:         m.injected,
		Clean:            m.clean,
		NoServer:         m.noServer,
		Degraded:         m.degraded,
		DiagHit:          m.diagHit,
		DiagHitRatio:     ratio(m.diagHit, m.injected),
		FallbackRatio:    ratio(m.degraded, m.requests),
		WaitLatencyP50MS: percentile(m.latency, 0.50),
		WaitLatencyP95MS: percentile(m.latency, 0.95),
		LatencySamples:   len(m.latency),
		DiagCount:        m.diagCount,
		AppendedBytes:    m.appendedBytes,
		ByOutcome:        make(map[string]int, len(m.byOutcome)),
		RecentRequests:   make([]RequestRecord, 0, len(m.recent)),
	}
	for outcome, count := range m.byOutcome {
		snap.ByOutcome[outcome] = count
	}
	for i := len(m.recent) - 1; i >= 0; i-- {
		snap.RecentRequests = append(snap.RecentRequests, m.recent[i])
	}
	if !m.lastAt.IsZero() {
		last := m.lastAt
		snap.LastRequestAt = &last
	}
	return snap
}

func ratio(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// percentile 返回上取整分位（P50 用 ceil(0.5*n)，样本为空返回 0）。
func percentile(samples []int64, p float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
