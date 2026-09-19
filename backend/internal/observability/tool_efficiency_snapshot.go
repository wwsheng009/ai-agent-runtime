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

// ToolFailureSnapshot aggregates tool_failure_total independently from provider
// failures. Its series retains the exact bounded dimensions used for diagnosis.
type ToolFailureSnapshot struct {
	Total          float64            `json:"total"`
	ByToolName     map[string]float64 `json:"by_tool_name"`
	ByErrorCode    map[string]float64 `json:"by_error_code"`
	ByFailureClass map[string]float64 `json:"by_failure_class"`
	ByRetryable    map[string]float64 `json:"by_retryable"`
	Series         []LabeledCount     `json:"series,omitempty"`
}

// ToolReplaySnapshot aggregates tool_disposition_replay_total.
type ToolReplaySnapshot struct {
	Total     float64            `json:"total"`
	ByOutcome map[string]float64 `json:"by_outcome"`
	ByRepeat  map[string]float64 `json:"by_repeat"`
	Series    []LabeledCount     `json:"series,omitempty"`
}

// ArtifactFlowSnapshot aggregates the artifact cascade counters
// (tool_output_archive_total / tool_output_truncation_total /
// tool_pointer_notice_total / tool_artifact_deref_total /
// tool_artifact_deref_miss_total) plus derived signals per plan §11.2.
type ArtifactFlowSnapshot struct {
	Archives      ArtifactArchivesSnapshot    `json:"archives"`
	Truncations   ArtifactTruncationsSnapshot `json:"truncations"`
	PointerNotice map[string]float64          `json:"pointer_notice"`
	Deref         ArtifactDerefSnapshot       `json:"deref"`
	// L1L4GapRatio is truncation_total{layer=l4_render} relative to total
	// truncations — the layer competition signal H-1/P0-1 should eliminate.
	L1L4GapRatio float64 `json:"l1_l4_gap_ratio"`
}

// ArtifactArchivesSnapshot groups archive decisions by layer/disposition.
type ArtifactArchivesSnapshot struct {
	Total         float64            `json:"total"`
	ByLayer       map[string]float64 `json:"by_layer"`
	ByDisposition map[string]float64 `json:"by_disposition"`
	Series        []LabeledCount     `json:"series,omitempty"`
}

// ArtifactTruncationsSnapshot groups truncation events by layer/dimension.
type ArtifactTruncationsSnapshot struct {
	Total       float64            `json:"total"`
	ByLayer     map[string]float64 `json:"by_layer"`
	ByTruncated map[string]float64 `json:"by_truncated_by"`
	Series      []LabeledCount     `json:"series,omitempty"`
}

// ArtifactDerefSnapshot aggregates artifact_read success/miss distribution.
type ArtifactDerefSnapshot struct {
	Total         float64            `json:"total"`
	FollowupRatio float64            `json:"followup_ratio"`
	MissByReason  map[string]float64 `json:"miss_by_reason"`
}

// ToolEfficiencySnapshot is a structured, low-cardinality view of tool-loop
// telemetry for runtime status / offline report alignment.
// Labels stay generic (reason/outcome/error_code/repeat) — never tool names.
type ToolEfficiencySnapshot struct {
	CapturedAt         time.Time             `json:"captured_at"`
	Preflight          ToolPreflightSnapshot `json:"preflight"`
	Outcomes           ToolOutcomeSnapshot   `json:"outcomes"`
	Failures           ToolFailureSnapshot   `json:"failures"`
	DispositionReplays ToolReplaySnapshot    `json:"disposition_replays"`
	// ArtifactFlow exposes the artifact cascade metrics (plan §11.2).
	ArtifactFlow ArtifactFlowSnapshot `json:"artifact_flow"`
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
		Failures: ToolFailureSnapshot{
			ByToolName:     map[string]float64{},
			ByErrorCode:    map[string]float64{},
			ByFailureClass: map[string]float64{},
			ByRetryable:    map[string]float64{},
		},
		DispositionReplays: ToolReplaySnapshot{
			ByOutcome: map[string]float64{},
			ByRepeat:  map[string]float64{},
		},
		ArtifactFlow: ArtifactFlowSnapshot{
			Archives: ArtifactArchivesSnapshot{
				ByLayer:       map[string]float64{},
				ByDisposition: map[string]float64{},
			},
			Truncations: ArtifactTruncationsSnapshot{
				ByLayer:     map[string]float64{},
				ByTruncated: map[string]float64{},
			},
			PointerNotice: map[string]float64{},
			Deref: ArtifactDerefSnapshot{
				MissByReason: map[string]float64{},
			},
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
	snap.Failures = aggregateFailures(grouped[MetricToolFailureTotal])
	snap.DispositionReplays = aggregateReplays(grouped[MetricToolDispositionReplayTotal])
	snap.ArtifactFlow = aggregateArtifactFlow(grouped)
	failureCodes := snap.Failures.ByErrorCode
	if len(failureCodes) == 0 {
		// Backward compatibility for callers/tests that still record only the
		// legacy outcome metric.
		failureCodes = snap.Outcomes.ByErrorCode
	}
	snap.FailCategories = deriveFailCategories(failureCodes)
	snap.InefficiencyFlags = deriveInefficiencyFlags(snap)
	return snap
}

// aggregateArtifactFlow reduces the artifact-cascade counters into the
// structured ArtifactFlowSnapshot (plan §11.2 快照扩展).
func aggregateArtifactFlow(grouped map[string][]MetricValue) ArtifactFlowSnapshot {
	out := ArtifactFlowSnapshot{
		Archives: ArtifactArchivesSnapshot{
			ByLayer:       map[string]float64{},
			ByDisposition: map[string]float64{},
		},
		Truncations: ArtifactTruncationsSnapshot{
			ByLayer:     map[string]float64{},
			ByTruncated: map[string]float64{},
		},
		PointerNotice: map[string]float64{},
		Deref: ArtifactDerefSnapshot{
			MissByReason: map[string]float64{},
		},
	}
	for _, v := range grouped[MetricToolOutputArchiveTotal] {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		layer := labelOr(labels, LabelLayer, "other")
		disposition := labelOr(labels, LabelDisposition, "other")
		out.Archives.Total += count
		out.Archives.ByLayer[layer] += count
		out.Archives.ByDisposition[disposition] += count
		out.Archives.Series = append(out.Archives.Series, LabeledCount{Labels: labels, Count: count})
	}
	sortLabeledCounts(out.Archives.Series)

	for _, v := range grouped[MetricToolOutputTruncationTotal] {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		layer := labelOr(labels, LabelLayer, "other")
		by := labelOr(labels, LabelTruncatedBy, "other")
		out.Truncations.Total += count
		out.Truncations.ByLayer[layer] += count
		out.Truncations.ByTruncated[by] += count
		out.Truncations.Series = append(out.Truncations.Series, LabeledCount{Labels: labels, Count: count})
	}
	sortLabeledCounts(out.Truncations.Series)
	if out.Truncations.Total > 0 {
		out.L1L4GapRatio = out.Truncations.ByLayer[TruncationLayerRender] / out.Truncations.Total
	}

	for _, v := range grouped[MetricToolPointerNoticeTotal] {
		count := v.Value
		if count == 0 {
			continue
		}
		kind := labelOr(cloneLabels(v.Labels), LabelKind, "other")
		out.PointerNotice[kind] += count
	}

	var followup float64
	for _, v := range grouped[MetricToolArtifactDerefTotal] {
		count := v.Value
		if count == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		page := labelOr(labels, LabelPage, DerefPageFirst)
		out.Deref.Total += count
		if page == DerefPageFollowup {
			followup += count
		}
	}
	if out.Deref.Total > 0 {
		out.Deref.FollowupRatio = followup / out.Deref.Total
	}
	for _, v := range grouped[MetricToolArtifactDerefMissTotal] {
		count := v.Value
		if count == 0 {
			continue
		}
		reason := labelOr(cloneLabels(v.Labels), LabelReason, "other")
		out.Deref.MissByReason[reason] += count
	}
	return out
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

func aggregateFailures(values []MetricValue) ToolFailureSnapshot {
	out := ToolFailureSnapshot{
		ByToolName:     map[string]float64{},
		ByErrorCode:    map[string]float64{},
		ByFailureClass: map[string]float64{},
		ByRetryable:    map[string]float64{},
		Series:         make([]LabeledCount, 0, len(values)),
	}
	for _, v := range values {
		if v.Value == 0 {
			continue
		}
		labels := cloneLabels(v.Labels)
		toolName := labelOr(labels, LabelToolName, "unknown")
		errorCode := labelOr(labels, LabelErrorCode, "unknown")
		failureClass := labelOr(labels, LabelFailureClass, "unknown")
		retryable := labelOr(labels, LabelRetryable, "false")
		out.Total += v.Value
		out.ByToolName[toolName] += v.Value
		out.ByErrorCode[errorCode] += v.Value
		out.ByFailureClass[failureClass] += v.Value
		out.ByRetryable[retryable] += v.Value
		out.Series = append(out.Series, LabeledCount{Labels: labels, Count: v.Value})
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
	case "SESSION_NOT_FOUND", "AGENT_SESSION_NOT_FOUND":
		// Host-side session lifecycle failures must not pollute path_missing
		// (pre-fix mislabel) or fall into other_error: keep them visible as
		// their own bucket for offline efficiency reports.
		return "session_lifecycle"
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

	// Artifact-flow derived signals (plan §11.2). Deref samples are naturally
	// sparser than outcome samples, so they use their own lower floor.
	const derefMinSamples = 5.0
	if flow := snap.ArtifactFlow; flow.Deref.Total >= derefMinSamples {
		if flow.Deref.FollowupRatio > 0.30 {
			flags = append(flags, "artifact_deref_heavy")
		}
	}
	if flow := snap.ArtifactFlow; flow.Archives.Total > 0 {
		skipped := flow.Archives.ByDisposition[ArchiveDispositionSkippedBelowThreshold] +
			flow.Archives.ByDisposition[ArchiveDispositionSkippedReadWindow] +
			flow.Archives.ByDisposition[ArchiveDispositionSkippedEmpty]
		if skipped/flow.Archives.Total > 0.80 {
			flags = append(flags, "archive_skipped_majority")
		}
	}
	if snap.ArtifactFlow.Truncations.Total >= minSamples && snap.ArtifactFlow.L1L4GapRatio > 0.05 {
		flags = append(flags, "l1_l4_gap_present")
	}

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
