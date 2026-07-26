package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Productized doom-loop harness surface (C4a).
//
// Policy (compatible defaults):
//   - Soft advisories always-on for identical semantic tool batches (repeat >= 2).
//   - Product warning event fires once at DoomLoopWarningThreshold (4).
//   - Hard stop remains opt-in via LoopReActConfig.MaxRepeatedToolCalls (>0).
//   - Polling/control tools are exempt so wait/list loops are not false positives.
//   - Parallel partial/empty batch replay is guided via disposition advisories, not
//     treated as a separate doom-loop class.
const (
	// DoomLoopWarningThreshold is the first productized warning emission point.
	// Matches the historical repeatedSemanticToolCallNoticeThreshold.
	DoomLoopWarningThreshold = 4

	// Event names: product surface (stable for hosts/telemetry).
	EventDoomLoopWarning     = "tool_loop.doom_loop_warning"
	EventDoomLoopTerminated  = "tool_loop.doom_loop_terminated"
	// Legacy names retained for existing subscribers/tests.
	EventRepeatedSemanticCallObserved = "tool_loop.repeated_semantic_call_observed"
)

// DoomLoopObservation is the per-turn result of ObserveSemanticToolBatch.
type DoomLoopObservation struct {
	Fingerprint      string
	RepeatCount      int
	ToolCallCount    int
	EmitWarning      bool
	ShouldStop       bool
	StopLimit        int
	Advisory         string
	// LimitReason is set when ShouldStop is true.
	LimitReason string
}

// DoomLoopTracker tracks consecutive identical semantic tool batches.
type DoomLoopTracker struct {
	lastFingerprint string
	repeatCount     int
	warningEmitted  bool
	// maxRepeated is the hard-stop threshold; <=0 disables hard stop.
	maxRepeated int
}

// NewDoomLoopTracker constructs a tracker. maxRepeated <=0 means warn-only.
func NewDoomLoopTracker(maxRepeated int) *DoomLoopTracker {
	return &DoomLoopTracker{maxRepeated: maxRepeated}
}

// RepeatCount returns the current consecutive repeat count for the last fingerprint.
func (t *DoomLoopTracker) RepeatCount() int {
	if t == nil {
		return 0
	}
	return t.repeatCount
}

// Fingerprint returns the last observed semantic fingerprint (may be empty).
func (t *DoomLoopTracker) Fingerprint() string {
	if t == nil {
		return ""
	}
	return t.lastFingerprint
}

// ObserveSemanticToolBatch updates consecutive-repeat state for the model tool batch.
// Exempt tools (wait/list/goal polling) clear the tracker and never count as doom-loop.
func (t *DoomLoopTracker) ObserveSemanticToolBatch(calls []types.ToolCall) DoomLoopObservation {
	obs := DoomLoopObservation{ToolCallCount: len(calls)}
	if t == nil {
		return obs
	}
	fingerprint := semanticToolCallFingerprint(calls)
	if fingerprint == "" {
		t.lastFingerprint = ""
		t.repeatCount = 0
		t.warningEmitted = false
		return obs
	}
	if fingerprint == t.lastFingerprint {
		t.repeatCount++
	} else {
		t.lastFingerprint = fingerprint
		t.repeatCount = 1
		t.warningEmitted = false
	}
	obs.Fingerprint = fingerprint
	obs.RepeatCount = t.repeatCount
	if t.repeatCount >= 2 {
		obs.Advisory = repeatedSemanticToolCallAdvisory(t.repeatCount)
	}
	// Emit product warning once when crossing the notice threshold.
	if t.repeatCount == DoomLoopWarningThreshold && !t.warningEmitted {
		obs.EmitWarning = true
		t.warningEmitted = true
	}
	if t.maxRepeated > 0 && t.repeatCount >= t.maxRepeated {
		obs.ShouldStop = true
		obs.StopLimit = t.maxRepeated
		obs.LimitReason = "repeated_tool_calls"
	}
	return obs
}

// WarningEventPayload builds the dual-name warning payload for runtime events.
func DoomLoopWarningPayload(traceID string, step int, obs DoomLoopObservation) map[string]interface{} {
	return map[string]interface{}{
		"trace_id":              traceID,
		"step":                  step,
		"tool_call_fingerprint": obs.Fingerprint,
		"repeat_count":          obs.RepeatCount,
		"tool_call_count":       obs.ToolCallCount,
		"warning_threshold":     DoomLoopWarningThreshold,
		"phase":                 "warning",
		"kind":                  "semantic_tool_repeat",
	}
}

// TerminationEventPayload builds the product termination payload.
func DoomLoopTerminationPayload(traceID string, step int, obs DoomLoopObservation) map[string]interface{} {
	return map[string]interface{}{
		"trace_id":              traceID,
		"step":                  step,
		"tool_call_fingerprint": obs.Fingerprint,
		"repeat_count":          obs.RepeatCount,
		"tool_call_count":       obs.ToolCallCount,
		"stop_limit":            obs.StopLimit,
		"limit_reason":          obs.LimitReason,
		"phase":                 "terminated",
		"kind":                  "semantic_tool_repeat",
	}
}

// semanticToolCallFingerprint hashes normalized tool name+args for the batch.
// Returns empty when any call is exempt (polling/control tools) or the batch is empty.
func semanticToolCallFingerprint(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	type semanticToolCall struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"arguments,omitempty"`
	}
	payload := make([]semanticToolCall, 0, len(calls))
	for _, call := range calls {
		name := strings.ToLower(strings.TrimSpace(call.Name))
		if name == "" || semanticToolCallRepeatExempt(name) {
			return ""
		}
		payload = append(payload, semanticToolCall{Name: name, Args: call.Args})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func semanticToolCallRepeatExempt(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "background_task", "wait_agent", "read_agent", "list_agents", "get_agents", "get_goal", "read_goal":
		return true
	default:
		return false
	}
}
