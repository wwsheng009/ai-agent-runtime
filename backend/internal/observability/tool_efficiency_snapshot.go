package observability

import (
	"sort"
	"strings"
	"time"
)

// LabeledCount is one counter series cell (stable label set + value).
type LabeledCount struct {
	Labels map[string]string `json:"labels"`
	Count  float64           `json:"count"`
}

// ToolPreflightSnapshot aggregates tool_preflight_total.
type ToolPreflightSnapshot struct {
	Total      float64            `json:"total"`
	Allow      float64            `json:"allow"`
	Deny       float64            `json:"deny"`
	AllowRate  float64            `json:"allow_rate"`
	ByReason   map[string]float64 `json:"by_reason"`
	ByDecision map[string]float64 `json:"by_decision"`
	Series     []LabeledCount     `json:"series,omitempty"`
}

// ToolOutcomeSnapshot aggregates tool_outcome_total.
// SuccessRate is success/total; NonFailRate treats empty/partial as non-fail evidence.
type ToolOutcomeSnapshot struct {
	Total       float64            `json:"total"`
	ByOutcome   map[string]float64 `json:"by_outcome"`
	ByErrorCode map[string]float64 `json:"by_error_code"`
	SuccessRate float64            `json:"success_rate"`
	NonFailRate float64            `json:"non_fail_rate"`
	Series      []LabeledCount     `json:"series,omitempty"`
}

// ToolReplaySnapshot aggregates tool_disposition_replay_total.
type ToolReplaySnapshot struct {
	Total     float64            `json:"total"`
	ByOutcome map[string]float64 `json:"by_outcome"`
	ByRepeat  map[string]float64 `json:"by_repeat"`
	Series    []LabeledCount     `json:"series,omitempty"`
}

// ToolEfficiencySnapshot is a structured, low-cardinality view of tool-loop
// telemetry for runtime status / offline report alignment.
// Labels stay generic (reason/outcome/error_code/repeat) — never tool names.
type ToolEfficiencySnapshot struct {
	CapturedAt         time.Time             `json:"captured_at"`
	Preflight          ToolPreflightSnapshot `json:"preflight"`
	Outcomes           ToolOutcomeSnapshot   `json:"outcomes"`
	DispositionReplays ToolReplaySnapshot    `json:"disposition_replays"`
	// FailCategories maps runtime error codes into coarse offline-report style buckets.
	FailCategories map[string]float64 `json:"fail_categories"`
	// InefficiencyFlags are generic signals derived from rates (no tool-name branches).
	InefficiencyFlags []string `json:"inefficiency_flags"`
}

// SnapshotToolEfficiency returns a snapshot from GlobalMetrics.
func SnapshotToolEfficiency() ToolEfficiencySnapshot {
	return GlobalMetrics.SnapshotToolEfficiency()
}

// SnapshotToolEfficiency aggregates tool-efficiency counters from this registry.
func (r *Registry) SnapshotToolEfficiency() ToolEfficiencySnapshot {
	snap := ToolEfficiencySnapshot{
		CapturedAt: time.Now().UTC(),
		Preflight: ToolPreflightSnapshot{
			ByReason:   map[string]float64{},
			ByDecision: map[string]float64{},
		},
		Outcomes: ToolOutcomeSnapshot{
			ByOutcome:   map[string]float64{},
			ByErrorCode: map[string]float64{},
		},
		DispositionReplays: ToolReplaySnapshot{
			ByOutcome: map[string]float64{},
			ByRepeat:  map[string]float64{},
		},
		FailCategories:    map[string]float64{},
		InefficiencyFlags: []string{},
	}
	if r == nil {
		return snap
	}

	grouped := r.SnapshotCounters()
	snap.Preflight = aggregatePreflight(grouped[MetricToolPreflightTotal])
	snap.Outcomes = aggregateOutcomes(grouped[MetricToolOutcomeTotal])
	snap.DispositionReplays = aggregateReplays(grouped[MetricToolDispositionReplayTotal])
	snap.FailCategories = deriveFailCategories(snap.Outcomes.ByErrorCode)
	snap.InefficiencyFlags = deriveInefficiencyFlags(snap)
	return snap
}

func aggregatePreflight(values []MetricValue) ToolPreflightSnapshot {
	out := ToolPreflightSnapshot{
		ByReason:   map[string]float64{},
		ByDecision: map[string]float64{},
		Series:     make([]LabeledCount, 0, len(values)),
	}
	for _, v := range values {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		reason := labelOr(labels, LabelReason, PreflightReasonUnknown)
		decision := labelOr(labels, LabelDecision, "unknown")
		out.Total += count
		out.ByReason[reason] += count
		out.ByDecision[decision] += count
		switch decision {
		case "allow":
			out.Allow += count
		case "deny":
			out.Deny += count
		}
		out.Series = append(out.Series, LabeledCount{Labels: labels, Count: count})
	}
	if out.Total > 0 {
		out.AllowRate = out.Allow / out.Total
	}
	sortLabeledCounts(out.Series)
	return out
}

func aggregateOutcomes(values []MetricValue) ToolOutcomeSnapshot {
	out := ToolOutcomeSnapshot{
		ByOutcome:   map[string]float64{},
		ByErrorCode: map[string]float64{},
		Series:      make([]LabeledCount, 0, len(values)),
	}
	for _, v := range values {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		outcome := labelOr(labels, LabelOutcome, ToolOutcomeUnknown)
		code := labelOr(labels, LabelErrorCode, "none")
		out.Total += count
		out.ByOutcome[outcome] += count
		if outcome == ToolOutcomeFailed {
			if code == "" || code == "none" {
				code = "unknown"
			}
			out.ByErrorCode[code] += count
		}
		out.Series = append(out.Series, LabeledCount{Labels: labels, Count: count})
	}
	if out.Total > 0 {
		success := out.ByOutcome[ToolOutcomeSuccess]
		nonFail := success + out.ByOutcome[ToolOutcomeEmpty] + out.ByOutcome[ToolOutcomePartial]
		out.SuccessRate = success / out.Total
		out.NonFailRate = nonFail / out.Total
	}
	sortLabeledCounts(out.Series)
	return out
}

func aggregateReplays(values []MetricValue) ToolReplaySnapshot {
	out := ToolReplaySnapshot{
		ByOutcome: map[string]float64{},
		ByRepeat:  map[string]float64{},
		Series:    make([]LabeledCount, 0, len(values)),
	}
	for _, v := range values {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		outcome := labelOr(labels, LabelOutcome, ToolOutcomeUnknown)
		repeat := labelOr(labels, LabelRepeat, "1")
		out.Total += count
		out.ByOutcome[outcome] += count
		out.ByRepeat[repeat] += count
		out.Series = append(out.Series, LabeledCount{Labels: labels, Count: count})
	}
	sortLabeledCounts(out.Series)
	return out
}

// deriveFailCategories maps error_code counters into coarse buckets used by
// offline efficiency reports (shell_compat / path_missing / arg_schema / ...).
// Mapping is code-driven only — never tool-name based.
func deriveFailCategories(byErrorCode map[string]float64) map[string]float64 {
	cats := map[string]float64{}
	for code, count := range byErrorCode {
		if count == 0 {
			continue
		}
		cats[failCategoryForCode(code)] += count
	}
	return cats
}

func failCategoryForCode(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "TOOL_SHELL_COMPAT":
		return "shell_compat"
	case "TOOL_PATH_NOT_FOUND":
		return "path_missing"
	case "TOOL_INVALID_ARGS":
		return "arg_schema"
	case "TOOL_TIMEOUT", "TURN_DEADLINE_EXCEEDED":
		return "timeout"
	case "STALE_CONTEXT", "TOOL_STALE_CONTEXT":
		return "stale_context"
	case "SPAWN_DEPTH_LIMIT", "AGENT_SPAWN_DEPTH_LIMIT":
		return "spawn_depth"
	case "TOOL_EXECUTION", "PROCESS_START_FAILED", "PROCESS_HEALTHCHECK_FAILED", "TOOL_BROKER_FAILURE":
		return "execution"
	case "TOOL_NOT_FOUND", "TOOL_NOT_REGISTERED":
		return "not_found"
	case "", "NONE", "UNKNOWN":
		return "other_error"
	default:
		return "other_error"
	}
}

func deriveInefficiencyFlags(snap ToolEfficiencySnapshot) []string {
	flags := make([]string, 0, 8)
	const minSamples = 10.0

	if snap.Preflight.Total >= minSamples && snap.Preflight.Deny/snap.Preflight.Total >= 0.10 {
		flags = append(flags, "high_preflight_deny_rate")
	}
	if snap.Outcomes.Total >= minSamples {
		failed := snap.Outcomes.ByOutcome[ToolOutcomeFailed]
		if failed/snap.Outcomes.Total >= 0.10 {
			flags = append(flags, "high_failed_outcome_rate")
		}
		if empty := snap.Outcomes.ByOutcome[ToolOutcomeEmpty]; empty/snap.Outcomes.Total >= 0.20 {
			flags = append(flags, "elevated_empty_outcomes")
		}
		if partial := snap.Outcomes.ByOutcome[ToolOutcomePartial]; partial/snap.Outcomes.Total >= 0.10 {
			flags = append(flags, "elevated_partial_outcomes")
		}
	}
	if snap.DispositionReplays.Total > 0 {
		flags = append(flags, "disposition_replays_present")
		if snap.DispositionReplays.ByRepeat["3plus"] > 0 {
			flags = append(flags, "repeated_empty_partial_3plus")
		}
	}
	if snap.FailCategories["shell_compat"] > 0 {
		flags = append(flags, "shell_compat_failures")
	}
	if snap.FailCategories["path_missing"] > 0 {
		flags = append(flags, "path_missing_failures")
	}
	if snap.FailCategories["stale_context"] > 0 {
		flags = append(flags, "stale_context_failures")
	}
	if snap.FailCategories["spawn_depth"] > 0 {
		flags = append(flags, "spawn_depth_failures")
	}
	if snap.Preflight.ByReason[PreflightReasonPathExistence] > 0 {
		flags = append(flags, "path_existence_preflight_denies")
	}
	if snap.Preflight.ByReason[PreflightReasonCircuitOpen] > 0 {
		flags = append(flags, "circuit_open_preflight_denies")
	}
	sort.Strings(flags)
	return flags
}

func labelOr(labels map[string]string, key, fallback string) string {
	if labels == nil {
		return fallback
	}
	if v := strings.TrimSpace(labels[key]); v != "" {
		return v
	}
	return fallback
}

func sortLabeledCounts(series []LabeledCount) {
	sort.SliceStable(series, func(i, j int) bool {
		if series[i].Count != series[j].Count {
			return series[i].Count > series[j].Count
		}
		return labelsKey(series[i].Labels) < labelsKey(series[j].Labels)
	})
}

func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(';')
	}
	return b.String()
}
