package observability

import (
	"strings"
)

// Preflight reason labels (stable, schema/metadata-driven; no tool-name switches).
const (
	PreflightReasonAllow         = "allow"
	PreflightReasonRequiredArgs  = "required_args"
	PreflightReasonArgTypes      = "arg_types"
	PreflightReasonCircuitOpen   = "circuit_open"
	PreflightReasonPathExistence = "path_existence"
	// PreflightReasonEmptyReplay is a soft deny that reuses prior empty-success
	// evidence without re-executing the tool (negative cache short-circuit).
	PreflightReasonEmptyReplay = "empty_replay"
	PreflightReasonUnknown     = "unknown"
)

// Outcome labels mirror toolresult dispositions but stay local to observability
// so packages can record without importing toolresult (cycle safety).
const (
	ToolOutcomeSuccess = "success"
	ToolOutcomeEmpty   = "empty"
	ToolOutcomePartial = "partial"
	ToolOutcomeFailed  = "failed"
	ToolOutcomeUnknown = "unknown"
)

// RecordToolPreflight counts schema-driven preflight decisions.
// reason should be one of the PreflightReason* values; empty reason on allow
// is normalized to "allow", and unknown deny reasons collapse to "unknown".
func RecordToolPreflight(reason string, allowed bool) {
	decision := "allow"
	if !allowed {
		decision = "deny"
	}
	IncrementCounter(MetricToolPreflightTotal, map[string]string{
		LabelReason:   normalizePreflightReason(reason, allowed),
		LabelDecision: decision,
	})
}

// RecordToolOutcome counts model-facing tool dispositions after diagnostics.
// error_code is only retained for failed outcomes (bounded runtime codes);
// other dispositions use error_code=none to keep label cardinality stable.
func RecordToolOutcome(outcome, errorCode string) {
	normalized := normalizeToolOutcome(outcome)
	code := "none"
	if normalized == ToolOutcomeFailed {
		code = normalizeErrorCode(errorCode)
	}
	IncrementCounter(MetricToolOutcomeTotal, map[string]string{
		LabelOutcome:   normalized,
		LabelErrorCode: code,
	})
}

// RecordToolDispositionReplay counts identical-batch advisories for empty/partial
// /failed evidence (failed includes STALE_CONTEXT replays). repeat is bucketed
// (1 / 2 / 3+) to avoid unbounded labels.
func RecordToolDispositionReplay(outcome string, repeatCount int) {
	normalized := normalizeToolOutcome(outcome)
	switch normalized {
	case ToolOutcomeEmpty, ToolOutcomePartial, ToolOutcomeFailed:
	default:
		// Success/unknown are not disposition-replay advisories.
		return
	}
	IncrementCounter(MetricToolDispositionReplayTotal, map[string]string{
		LabelOutcome: normalized,
		LabelRepeat:  repeatBucket(repeatCount),
	})
}

// RecordDoomLoop counts productized doom-loop harness signals.
// phase is "warning" (soft, always-on at threshold) or "terminated" (hard stop).
func RecordDoomLoop(phase string) {
	IncrementCounter(MetricDoomLoopTotal, map[string]string{
		LabelPhase: normalizeDoomLoopPhase(phase),
	})
}

func normalizeDoomLoopPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "warning", "warn":
		return "warning"
	case "terminated", "terminate", "stop", "hard_stop":
		return "terminated"
	default:
		return "unknown"
	}
}

func normalizePreflightReason(reason string, allowed bool) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	switch reason {
	case PreflightReasonRequiredArgs, PreflightReasonArgTypes, PreflightReasonCircuitOpen, PreflightReasonPathExistence, PreflightReasonEmptyReplay:
		return reason
	case PreflightReasonAllow, "allowed", "pass", "ok", "":
		if allowed {
			return PreflightReasonAllow
		}
		return PreflightReasonUnknown
	default:
		if allowed {
			return PreflightReasonAllow
		}
		if reason == "" {
			return PreflightReasonUnknown
		}
		// Bound cardinality: free-form reasons collapse rather than explode labels.
		return PreflightReasonUnknown
	}
}

func normalizeToolOutcome(outcome string) string {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case ToolOutcomeSuccess, "ok", "succeeded":
		return ToolOutcomeSuccess
	case ToolOutcomeEmpty, "empty_success", "no_output", "no_matches":
		return ToolOutcomeEmpty
	case ToolOutcomePartial, "partial_success", "partial_failure":
		return ToolOutcomePartial
	case ToolOutcomeFailed, "error", "failure":
		return ToolOutcomeFailed
	default:
		return ToolOutcomeUnknown
	}
}

func normalizeErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "unknown"
	}
	// Keep codes readable but prevent pathological free-text labels.
	if len(code) > 64 {
		code = code[:64]
	}
	return code
}

func repeatBucket(repeatCount int) string {
	switch {
	case repeatCount <= 1:
		return "1"
	case repeatCount == 2:
		return "2"
	default:
		return "3plus"
	}
}
