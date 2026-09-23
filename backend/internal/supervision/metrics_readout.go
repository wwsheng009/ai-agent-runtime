package supervision

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 2026-09-23 C0-E（§7.3 度量基线采集）读数模块。
//
// 目的：把 §7.3 五项指标的**读数口径**从验收用例的局部实现提升为生产代码的
// 单一来源 —— `/supervision/metrics` 的线上采样与验收用例调用同一函数，否则
// 「测试绿」与「线上读数」可以各说各话，基线数字与验收结论不可比。
//
// 覆盖：
//
//	指标 1  被 runtime 强制取消的 run 占比        ComputeRuntimeCancelMetrics
//	指标 2  decision_window_expired 兜底占比      ComputeRuntimeCancelMetrics
//	指标 3  成功完成 → 父 Agent 汇报的延迟 P95    ComputeReportLatencyMetrics
//	指标 4  单托管 turn 的 resume 次数（1h 预算）  宿主侧预算账本（见 MetricsSnapshot.Notes）
//	指标 5  误杀率（取消后 5 分钟内本可完成）       MisKillCandidate 候选清单 + 人工复核
//
// 指标 3/4/5 的数据来源边界写在各自的类型注释里：能被 durable 行复算的自动算，
// 算不了的（人工复核、内存态预算）显式标注为「需人工 / 需宿主读数」，不做假数字。

// CancelSource 取值。这些字符串同时是 supervision_execution_runs.cancel_source
// 的写入值（execution_supervisor.go 的 enforceRunCancel 与兜底分支），集中定义
// 一次，避免「读数白名单」与「写入端」各改一半。
const (
	// CancelSourceDecisionWindowExpired 是 escalate-first 的兜底分支：决策宽限期
	// 到期仍无 extend/cancel 决策时由 runtime 强制取消（I8）。
	CancelSourceDecisionWindowExpired = "decision_window_expired"
	// CancelSourceProgressStalled 是无进展软阈值直接取消（escalate_first=false
	// 的 legacy 分支）。
	CancelSourceProgressStalled = "progress_stalled"
	// CancelSourceExecutionDeadline 是硬执行截止（声明预算 + 延长后的总期限）。
	CancelSourceExecutionDeadline = "execution_deadline"
	// CancelSourceExecutionTimedOut 是执行超时判定。
	CancelSourceExecutionTimedOut = "execution_timed_out"
)

// runtimeForcedCancelSources 是「被 runtime 强制取消」的口径白名单：这些来源由
// watchdog / 执行看门狗自己判定；父会话或操作者的主动取消（operator_cancel /
// parent_cancel / user_cancel …）不计入分子 —— §7.3 指标 1 问的是「runtime 判了
// 多少次终态」，不是「用户喊停多少次」。
var runtimeForcedCancelSources = map[string]struct{}{
	CancelSourceDecisionWindowExpired: {},
	CancelSourceProgressStalled:       {},
	CancelSourceExecutionDeadline:     {},
	CancelSourceExecutionTimedOut:     {},
}

// IsRuntimeForcedCancelSource reports whether a CancelSource value counts as a
// runtime-forced cancel in the §7.3 metric-1 numerator.
func IsRuntimeForcedCancelSource(source string) bool {
	_, ok := runtimeForcedCancelSources[strings.TrimSpace(source)]
	return ok
}

// RuntimeCancelMetrics is the §7.3 metric-1/2 readout over one sampling window.
type RuntimeCancelMetrics struct {
	// Sessions is how many distinct sessions the readout walked (not how many
	// had rows).
	Sessions int `json:"sessions"`
	// Total is the denominator: every run whose accounting instant falls in the
	// window (see RunAccountingInstant), cancelled or not.
	Total                 int            `json:"total"`
	ForcedCancel          int            `json:"forced_cancel"`
	DecisionWindowExpired int            `json:"decision_window_expired"`
	BySource              map[string]int `json:"by_source,omitempty"`
	// TruncatedSessions lists sessions whose page came back full and whose
	// oldest row was still inside the window: that session may hold more
	// in-window rows than this readout saw (the store caps one page at 100
	// rows). The flag is reported instead of silently under-counting.
	TruncatedSessions []string `json:"truncated_sessions,omitempty"`
}

// ForcedCancelRatio is §7.3 metric 1.
func (m RuntimeCancelMetrics) ForcedCancelRatio() float64 {
	return ratio(m.ForcedCancel, m.Total)
}

// DecisionWindowExpiredRatio is §7.3 metric 2 (acceptance: < 10%).
func (m RuntimeCancelMetrics) DecisionWindowExpiredRatio() float64 {
	return ratio(m.DecisionWindowExpired, m.Total)
}

func ratio(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// RuntimeCancelMetricsOptions scopes one readout.
type RuntimeCancelMetricsOptions struct {
	// SessionIDs are the parent/child sessions to walk. Callers pass the
	// sessions they own; the store read is per session by design (the same
	// scope rule the model-facing tools enforce).
	SessionIDs []string
	// PerSessionLimit caps one page; the store clamps it to 100.
	PerSessionLimit int
	// Since/Until bound the window (inclusive). Zero means unbounded.
	Since time.Time
	Until time.Time
}

// RunAccountingInstant is the instant a run is counted at: its terminal
// timestamp when it has one, otherwise its creation time. Cancel metrics ask
// "what did runtime terminate in this window", so a cancelled run counts in the
// window where the cancel landed, while a still-live run counts where it
// started.
func RunAccountingInstant(run ExecutionRun) time.Time {
	if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
		return run.FinishedAt.UTC()
	}
	return run.CreatedAt.UTC()
}

// ComputeRuntimeCancelMetrics reads the window and computes §7.3 metrics 1–2.
// It is the production read path: one page per session via
// ListExecutionRunsBySession (the store's own read interface), no ad-hoc SQL.
func ComputeRuntimeCancelMetrics(ctx context.Context, store ExecutionRunStore, opts RuntimeCancelMetricsOptions) (RuntimeCancelMetrics, error) {
	runs, truncated, sessions, err := ReadExecutionRunsInWindow(ctx, store, opts)
	if err != nil {
		return RuntimeCancelMetrics{}, err
	}
	metrics := ComputeRuntimeCancelMetricsFromRuns(runs, opts)
	metrics.TruncatedSessions = truncated
	metrics.Sessions = sessions
	return metrics, nil
}

// ReadExecutionRunsInWindow returns every run of the given sessions whose
// accounting instant falls inside the window, plus the sessions whose page was
// full while their oldest row was still in-window (possible under-count) and
// how many sessions were walked.
func ReadExecutionRunsInWindow(ctx context.Context, store ExecutionRunStore, opts RuntimeCancelMetricsOptions) ([]ExecutionRun, []string, int, error) {
	if store == nil {
		return nil, nil, 0, nil
	}
	sessions := uniqueTrimmedStrings(opts.SessionIDs)
	limit := opts.PerSessionLimit
	if limit <= 0 || limit > executionRunPageLimit {
		limit = executionRunPageLimit
	}
	rows := make([]ExecutionRun, 0, len(sessions))
	truncated := make([]string, 0, 1)
	for _, sessionID := range sessions {
		page, err := store.ListExecutionRunsBySession(ctx, sessionID, limit)
		if err != nil {
			return nil, nil, len(sessions), fmt.Errorf("read execution runs for session %s: %w", sessionID, err)
		}
		if len(page) == 0 {
			continue
		}
		// The store returns newest first, so the last row is the oldest: a full
		// page whose oldest row is still inside the window may hide more rows.
		if len(page) >= limit && runInWindow(page[len(page)-1], opts) {
			truncated = append(truncated, sessionID)
		}
		for _, run := range page {
			if !runInWindow(run, opts) {
				continue
			}
			rows = append(rows, run)
		}
	}
	return rows, truncated, len(sessions), nil
}

// executionRunPageLimit mirrors the store's hard page cap for
// ListExecutionRunsBySession (limit > 100 falls back to 10 there, so asking for
// more would silently shrink the page).
const executionRunPageLimit = 100

// executionRunWindowDefaultLimit caps one store-wide window read when the caller
// does not pick a limit.
const executionRunWindowDefaultLimit = 200

// defaultMisKillCandidateLimit caps the §7.3 metric-5 review list: the list is
// meant for human sampling, not for dumping every forced cancel of the window.
const defaultMisKillCandidateLimit = 20

// ComputeRuntimeCancelMetricsFromRuns is the pure projection used by both the
// store-backed readout and callers that already hold the rows.
func ComputeRuntimeCancelMetricsFromRuns(runs []ExecutionRun, opts RuntimeCancelMetricsOptions) RuntimeCancelMetrics {
	metrics := RuntimeCancelMetrics{BySource: map[string]int{}}
	for _, run := range runs {
		if !runInWindow(run, opts) {
			continue
		}
		metrics.Total++
		source := strings.TrimSpace(run.CancelSource)
		if source == "" {
			continue
		}
		metrics.BySource[source]++
		if source == CancelSourceDecisionWindowExpired {
			metrics.DecisionWindowExpired++
		}
		if IsRuntimeForcedCancelSource(source) {
			metrics.ForcedCancel++
		}
	}
	return metrics
}

func runInWindow(run ExecutionRun, opts RuntimeCancelMetricsOptions) bool {
	instant := RunAccountingInstant(run)
	if instant.IsZero() {
		return false
	}
	if !opts.Since.IsZero() && instant.Before(opts.Since.UTC()) {
		return false
	}
	if !opts.Until.IsZero() && instant.After(opts.Until.UTC()) {
		return false
	}
	return true
}

// ReportLatencyMetrics is the §7.3 metric-3 readout: the delay between a child
// run reaching a successful terminal state and the parent mailbox receiving its
// completion entry (supervision_completion_outbox.delivered_at).
type ReportLatencyMetrics struct {
	Samples int `json:"samples"`
	// P50Millis/P95Millis/MaxMillis are milliseconds so the payload is stable
	// across JSON encoders (time.Duration would serialise as nanoseconds).
	P50Millis int64 `json:"p50_ms,omitempty"`
	P95Millis int64 `json:"p95_ms,omitempty"`
	MaxMillis int64 `json:"max_ms,omitempty"`
	// Unavailable is true when the store cannot list delivered outbox rows, so
	// the metric is unknown rather than zero (never report a fake 0ms).
	Unavailable bool   `json:"unavailable,omitempty"`
	Note        string `json:"note,omitempty"`
}

// DeliveredOutboxLister lists delivered completion-outbox entries. It is a
// separate optional interface (not part of ExecutionRunStore) so existing
// stores and test fakes stay valid; a store that cannot answer it makes metric
// 3 report Unavailable instead of failing the whole snapshot.
type DeliveredOutboxLister interface {
	ListDeliveredOutboxSince(ctx context.Context, since time.Time, limit int) ([]CompletionOutboxEntry, error)
}

// ComputeReportLatencyMetrics is a pure projection over already-read rows:
// successful terminal runs joined with their delivered completion entries by
// run id. Entries without a delivered timestamp or without a matching
// successful run are skipped — they cannot be attributed to a success report.
func ComputeReportLatencyMetrics(entries []CompletionOutboxEntry, runs []ExecutionRun) ReportLatencyMetrics {
	byRunID := make(map[string]ExecutionRun, len(runs))
	for _, run := range runs {
		byRunID[strings.TrimSpace(run.RunID)] = run
	}
	samples := make([]time.Duration, 0, len(entries))
	for _, entry := range entries {
		if entry.DeliveredAt == nil || entry.DeliveredAt.IsZero() {
			continue
		}
		run, ok := byRunID[strings.TrimSpace(entry.RunID)]
		if !ok || !runSuccessTerminal(run.Status) {
			continue
		}
		if run.FinishedAt == nil || run.FinishedAt.IsZero() {
			continue
		}
		latency := entry.DeliveredAt.UTC().Sub(run.FinishedAt.UTC())
		if latency < 0 {
			latency = 0
		}
		samples = append(samples, latency)
	}
	metrics := ReportLatencyMetrics{Samples: len(samples)}
	if len(samples) == 0 {
		return metrics
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	metrics.P50Millis = percentileMillis(samples, 0.50)
	metrics.P95Millis = percentileMillis(samples, 0.95)
	metrics.MaxMillis = samples[len(samples)-1].Milliseconds()
	return metrics
}

func runSuccessTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case RunStatusSucceeded, RunStatusCompleted, RunStatusCompletedWithFailures:
		return true
	}
	return false
}

// percentileMillis returns the nearest-rank percentile in milliseconds.
func percentileMillis(sorted []time.Duration, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(float64(len(sorted)) * p)
	if float64(rank) < float64(len(sorted))*p {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1].Milliseconds()
}

// MisKillCandidate is one runtime-forced cancel offered to the §7.3 metric-5
// human review ("would the child have finished within 5 minutes?"). The list is
// auto-generated; the verdict stays human, because "本可完成" needs the child's
// own progress evidence.
type MisKillCandidate struct {
	RunID          string `json:"run_id"`
	SessionID      string `json:"session_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	Status         string `json:"status,omitempty"`
	CancelSource   string `json:"cancel_source,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
	ExtensionCount int    `json:"extension_count,omitempty"`
	ProgressSeq    int64  `json:"progress_seq,omitempty"`
}

// MisKillCandidatesFromRuns lists the runtime-forced cancels of the window,
// newest first, capped at limit.
func MisKillCandidatesFromRuns(runs []ExecutionRun, opts RuntimeCancelMetricsOptions, limit int) []MisKillCandidate {
	candidates := make([]MisKillCandidate, 0, len(runs))
	for _, run := range runs {
		if !runInWindow(run, opts) || !IsRuntimeForcedCancelSource(run.CancelSource) {
			continue
		}
		candidate := MisKillCandidate{
			RunID:          strings.TrimSpace(run.RunID),
			SessionID:      strings.TrimSpace(run.SessionID),
			TurnID:         strings.TrimSpace(run.TurnID),
			Status:         strings.TrimSpace(run.Status),
			CancelSource:   strings.TrimSpace(run.CancelSource),
			ExtensionCount: run.ExtensionCount,
			ProgressSeq:    run.ProgressSeq,
		}
		if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
			candidate.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].FinishedAt > candidates[j].FinishedAt
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// ParseMetricsWindowValue parses a metrics window parameter: an RFC3339
// timestamp, a duration ("24h"), or a day count ("7d"); an empty string means
// unbounded (zero time). The HTTP endpoint and the offline sampler share this
// one parser, so "last week" cannot mean two different windows depending on
// which entry produced the baseline.
func ParseMetricsWindowValue(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(days))
		if err != nil || value <= 0 {
			return time.Time{}, fmt.Errorf("invalid day count %q", raw)
		}
		return now.Add(-time.Duration(value) * 24 * time.Hour), nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return time.Time{}, fmt.Errorf("expected RFC3339 or a lookback duration, got %q", raw)
	}
	return now.Add(-duration), nil
}

// MetricsSnapshotOptions scopes one C0-E snapshot.
type MetricsSnapshotOptions struct {
	// RootSessionID narrows the window read to one root scope; empty reads the
	// whole store (deployment baseline).
	RootSessionID string
	// SessionIDs is the fallback read path for stores without a window lister.
	SessionIDs      []string
	PerSessionLimit int
	// Since/Until bound the window (inclusive). Zero means unbounded.
	Since time.Time
	Until time.Time
	// WindowLimit caps the window read (default 200).
	WindowLimit int
	// OutboxPageLimit caps the delivered-outbox read for metric 3.
	OutboxPageLimit int
	// MaxCandidates caps the metric-5 review list (default 20).
	MaxCandidates int
}

// MetricsSnapshot is the C0-E baseline payload: the same object backs the text
// and JSON projections, so a stored baseline and a later comparison never see
// divergent numbers.
type MetricsSnapshot struct {
	GeneratedAt   string `json:"generated_at"`
	WindowSince   string `json:"window_since,omitempty"`
	WindowUntil   string `json:"window_until,omitempty"`
	RootSessionID string `json:"root_session_id,omitempty"`
	// Cancel carries §7.3 metrics 1–2.
	Cancel RuntimeCancelMetrics `json:"cancel"`
	// ReportLatency carries §7.3 metric 3.
	ReportLatency ReportLatencyMetrics `json:"report_latency"`
	// MisKillCandidates is the §7.3 metric-5 review list (verdict stays human).
	MisKillCandidates []MisKillCandidate `json:"mis_kill_candidates,omitempty"`
	// WindowTruncated is true when the window read came back at its cap: the
	// window may hold more rows, so the numbers are a lower bound.
	WindowTruncated bool `json:"window_truncated,omitempty"`
	// ReadPath records which store path produced the rows (window lister vs
	// per-session walk) so a baseline is reproducible.
	ReadPath string   `json:"read_path,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// CollectMetricsSnapshot reads one window and returns the §7.3 metrics that can
// be recomputed from durable rows. It prefers the store's window lister
// (deployment baseline over every scope) and falls back to the per-session walk
// (hosts that know their session set). Metric 4 stays with the host's budget
// ledger: in the default memory budget mode there are no durable claim rows to
// recompute it from, so reporting a number here would be a guess.
func CollectMetricsSnapshot(ctx context.Context, store Store, opts MetricsSnapshotOptions) (MetricsSnapshot, error) {
	snapshot := MetricsSnapshot{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		RootSessionID: strings.TrimSpace(opts.RootSessionID),
	}
	if !opts.Since.IsZero() {
		snapshot.WindowSince = opts.Since.UTC().Format(time.RFC3339)
	}
	if !opts.Until.IsZero() {
		snapshot.WindowUntil = opts.Until.UTC().Format(time.RFC3339)
	}
	if store == nil {
		snapshot.Notes = append(snapshot.Notes, "supervision store 未接线：§7.3 指标 1–3、5 不可读")
		return snapshot, nil
	}
	windowLimit := opts.WindowLimit
	if windowLimit <= 0 {
		windowLimit = executionRunWindowDefaultLimit
	}
	readOpts := RuntimeCancelMetricsOptions{
		SessionIDs:      opts.SessionIDs,
		PerSessionLimit: opts.PerSessionLimit,
		Since:           opts.Since,
		Until:           opts.Until,
	}
	var (
		runs      []ExecutionRun
		truncated []string
	)
	switch lister := store.(type) {
	case ExecutionRunWindowLister:
		page, err := lister.ListExecutionRunsInWindow(ctx, ExecutionRunWindowFilter{
			RootSessionID: opts.RootSessionID,
			Since:         opts.Since,
			Until:         opts.Until,
			Limit:         windowLimit,
		})
		if err != nil {
			return snapshot, err
		}
		runs = page
		snapshot.ReadPath = "window"
		snapshot.WindowTruncated = len(page) >= windowLimit
	default:
		runStore, ok := store.(ExecutionRunStore)
		if !ok || runStore == nil {
			snapshot.Notes = append(snapshot.Notes, "执行 run 存储未接线：§7.3 指标 1–3、5 不可读")
			return snapshot, nil
		}
		page, sessionsTruncated, sessions, err := ReadExecutionRunsInWindow(ctx, runStore, readOpts)
		if err != nil {
			return snapshot, err
		}
		runs = page
		truncated = sessionsTruncated
		snapshot.Cancel.Sessions = sessions
		snapshot.ReadPath = "session"
	}
	snapshot.Cancel = ComputeRuntimeCancelMetricsFromRuns(runs, readOpts)
	snapshot.Cancel.TruncatedSessions = truncated
	if snapshot.Cancel.Sessions == 0 {
		snapshot.Cancel.Sessions = len(uniqueTrimmedStrings(opts.SessionIDs))
	}
	if lister, ok := store.(DeliveredOutboxLister); ok {
		outboxLimit := opts.OutboxPageLimit
		if outboxLimit <= 0 {
			outboxLimit = executionRunWindowDefaultLimit
		}
		entries, err := lister.ListDeliveredOutboxSince(ctx, opts.Since, outboxLimit)
		if err != nil {
			snapshot.Notes = append(snapshot.Notes, "读取已投递完成出件失败："+err.Error())
		} else {
			snapshot.ReportLatency = ComputeReportLatencyMetrics(entries, runs)
			if snapshot.ReportLatency.Samples == 0 {
				snapshot.ReportLatency.Note = "窗口内没有已投递的成功完成出件：指标 3 无样本"
			}
		}
	} else {
		snapshot.ReportLatency = ReportLatencyMetrics{
			Unavailable: true,
			Note:        "store 未实现 ListDeliveredOutboxSince：指标 3 不可读（不报假 0）",
		}
	}
	maxCandidates := opts.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = defaultMisKillCandidateLimit
	}
	snapshot.MisKillCandidates = MisKillCandidatesFromRuns(runs, readOpts, maxCandidates)
	if snapshot.WindowTruncated {
		snapshot.Notes = append(snapshot.Notes, fmt.Sprintf(
			"窗口读取达到上限 %d 行：指标 1–3、5 是下界（缩小窗口或提高 WindowLimit）", windowLimit))
	}
	snapshot.Notes = append(snapshot.Notes,
		"指标 4（单托管 turn 的 resume 次数）由宿主预算账本读数（/supervision/snapshot 的 wake_budget）；memory 预算模式下 durable claim 行不存在，无法离线复算。")
	snapshot.Notes = append(snapshot.Notes,
		"指标 5（误杀率）为人工复核：mis_kill_candidates 给出窗口内 runtime 强制取消的 run，判定标准是「取消后 5 分钟内子任务本可完成」。")
	return snapshot, nil
}
