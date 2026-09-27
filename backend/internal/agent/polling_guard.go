package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolexec"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Polling/control tools are doom-loop exempt (see semanticToolCallRepeatExempt),
// so identical wait/read polling never trips the semantic repeat advisory. This
// guard adds the missing soft brake for those calls (P1-7):
//   - It only annotates results; execution is never blocked.
//   - A streak is local to one run and polling intent (target ids / after_seq,
//     excluding scheduling-only arguments such as timeout_ms).
//   - Real work, ready results, errors and interruptions reset the streak.
//   - Each threshold emits a product event once per streak.
const (
	// PollingBackoffNoticeThreshold is the consecutive identical polling count
	// at which the loop starts injecting the backoff advisory (default; see
	// LoopReActConfig.MaxRepeatedPollCalls for the override).
	PollingBackoffNoticeThreshold = 3

	// PollingWaitBudgetNoticeThreshold is a soft notice threshold for the sum
	// of tool-reported waited_ms in the current polling streak of this run.
	// It is neither a context/token budget nor a session-wide waiting quota.
	// Requested or clamped timeout windows are not elapsed waiting time.
	PollingWaitBudgetNoticeThreshold = 5 * time.Minute

	// EventPollingBackoffObserved is emitted when a streak first crosses each
	// threshold (repeat count or actual waiting time).
	EventPollingBackoffObserved = "tool_loop.polling_backoff_observed"
)

// PollingBackoffObservation is the result of observing an executed tool batch.
type PollingBackoffObservation struct {
	Fingerprint string
	// Tools lists the polling tool names participating in the streak.
	Tools []string
	// RepeatCount counts consecutive observations of the same polling request
	// (scheduling-only arguments such as timeout_ms are excluded).
	RepeatCount int
	// CumulativeWait is the sum of actual waited_ms across the current streak.
	CumulativeWait time.Duration
	// WaitBudgetExceeded reports that CumulativeWait crossed
	// PollingWaitBudgetNoticeThreshold; it does not authorize ending a turn.
	WaitBudgetExceeded bool
	// EmitNotice is true on the first crossing of each threshold in a streak.
	EmitNotice bool
	// Advisory is the model-facing guidance; non-empty from the threshold on.
	Advisory string
}

// PollingBackoffTracker tracks consecutive identical polling/control batches.
// A nil tracker is valid and behaves as disabled.
type PollingBackoffTracker struct {
	// threshold <= 0 disables the guard.
	threshold       int
	lastFingerprint string
	repeatCount     int
	cumulativeWait  time.Duration
	noticeEmitted   bool
	budgetNotified  bool
}

// NewPollingBackoffTracker constructs a tracker. threshold == 0 falls back to
// PollingBackoffNoticeThreshold; a negative threshold disables the guard.
func NewPollingBackoffTracker(threshold int) *PollingBackoffTracker {
	if threshold == 0 {
		threshold = PollingBackoffNoticeThreshold
	}
	return &PollingBackoffTracker{threshold: threshold}
}

// Threshold reports the active notice threshold (0 when disabled).
func (t *PollingBackoffTracker) Threshold() int {
	if t == nil || t.threshold < 0 {
		return 0
	}
	return t.threshold
}

// RepeatCount reports the current consecutive polling repeat count.
func (t *PollingBackoffTracker) RepeatCount() int {
	if t == nil {
		return 0
	}
	return t.repeatCount
}

// CumulativeWait reports actual waiting observed in this run's current streak.
func (t *PollingBackoffTracker) CumulativeWait() time.Duration {
	if t == nil {
		return 0
	}
	return t.cumulativeWait
}

// ObserveToolResults runs after execution, before toolResultsToPayloads reduces
// the output for the model. Missing timing evidence contributes zero, never the
// requested timeout. A batch with progress, failure or real work ends the streak.
func (t *PollingBackoffTracker) ObserveToolResults(results []toolExecutionResult) PollingBackoffObservation {
	obs := PollingBackoffObservation{}
	if t == nil || t.threshold <= 0 {
		return obs
	}
	calls := make([]types.ToolCall, len(results))
	for i, result := range results {
		calls[i] = result.Call
	}
	fingerprint, tools := pollingBatchFingerprint(calls)
	if fingerprint == "" {
		t.reset()
		return obs
	}
	actualWait, reset := pollingBatchActualWait(results)
	if reset {
		t.reset()
		return obs
	}
	if fingerprint == t.lastFingerprint {
		t.repeatCount++
		t.cumulativeWait = addPollingWait(t.cumulativeWait, actualWait)
	} else {
		t.lastFingerprint = fingerprint
		t.repeatCount = 1
		t.cumulativeWait = actualWait
		t.noticeEmitted = false
		t.budgetNotified = false
	}
	obs.Fingerprint = fingerprint
	obs.Tools = tools
	obs.RepeatCount = t.repeatCount
	obs.CumulativeWait = t.cumulativeWait

	repeatCrossed := t.repeatCount >= t.threshold
	budgetExceeded := t.cumulativeWait >= PollingWaitBudgetNoticeThreshold
	obs.WaitBudgetExceeded = budgetExceeded
	if !repeatCrossed && !budgetExceeded {
		return obs
	}
	if budgetExceeded {
		obs.Advisory = pollingWaitBudgetAdvisory(tools, t.cumulativeWait, t.repeatCount)
	} else {
		obs.Advisory = pollingBackoffAdvisory(tools, t.repeatCount)
	}
	if budgetExceeded && !t.budgetNotified {
		obs.EmitNotice = true
		t.budgetNotified = true
	}
	if repeatCrossed && !t.noticeEmitted {
		obs.EmitNotice = true
		t.noticeEmitted = true
	}
	return obs
}

func (t *PollingBackoffTracker) reset() {
	t.lastFingerprint = ""
	t.repeatCount = 0
	t.cumulativeWait = 0
	t.noticeEmitted = false
	t.budgetNotified = false
}

// pollingSoftBrakeTool reports whether name participates in the polling
// soft-brake streak. The set is the doom-loop polling/control exemption set
// minus read-only supervision inspection: re-reading a supervision view is an
// observation, not a blocking wait, and those calls stay exempt from the
// anti-polling advisory their tool descriptions promise.
func pollingSoftBrakeTool(name string) bool {
	return semanticToolCallRepeatExempt(name) && !supervisionInspectTool(name)
}

// pollingBatchFingerprint hashes an all-polling batch. It returns an empty
// fingerprint when the batch contains real work (including supervision
// inspection) or no polling call at all, so mixed batches reset the streak
// instead of being counted as polling loops.
func pollingBatchFingerprint(calls []types.ToolCall) (string, []string) {
	batch := strings.Builder{}
	tools := make([]string, 0, len(calls))
	for _, call := range calls {
		name := strings.ToLower(strings.TrimSpace(call.Name))
		if name == "" || !pollingSoftBrakeTool(name) {
			return "", nil
		}
		digest := toolexec.ArgsDigest(name, pollingFingerprintArgs(call.Args))
		fmt.Fprintf(&batch, "%d:%s", len(digest), digest)
		tools = append(tools, name)
	}
	if batch.Len() == 0 {
		return "", nil
	}
	sum := sha256.Sum256([]byte(batch.String()))
	return fmt.Sprintf("%x", sum[:]), tools
}

// pollingTimingOnlyArgs are the scheduling arguments of polling tools: they
// change how long one observation blocks, not what is being observed. They are
// excluded from the streak fingerprint so escalating a timeout cannot reset the
// brake without a change of target or new evidence.
var pollingTimingOnlyArgs = map[string]struct{}{
	"timeout":    {},
	"timeout_ms": {},
	"timeoutms":  {},
	"wait_ms":    {},
	"waitms":     {},
	"poll_ms":    {},
	"pollms":     {},
}

func pollingFingerprintArgs(args map[string]interface{}) map[string]interface{} {
	if len(args) == 0 {
		return args
	}
	filtered := make(map[string]interface{}, len(args))
	for key, value := range args {
		if _, timingOnly := pollingTimingOnlyArgs[strings.ToLower(strings.TrimSpace(key))]; timingOnly {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

// maxPollingWaitMillis bounds malformed result values, not requested windows.
const maxPollingWaitMillis = int64(24 * time.Hour / time.Millisecond)

func pollingBatchActualWait(results []toolExecutionResult) (time.Duration, bool) {
	total := time.Duration(0)
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" || (result.Envelope != nil && result.Envelope.Error != "") {
			return 0, true
		}
		fields := pollingResultFields(result)
		if pollingResultEndsStreak(fields) {
			return 0, true
		}
		if millis, ok := pollingResultMillis(fields["waited_ms"]); ok {
			total = addPollingWait(total, time.Duration(millis)*time.Millisecond)
		}
	}
	return total, false
}

func addPollingWait(total, wait time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if wait > maxDuration-total {
		return maxDuration
	}
	return total + wait
}

// Read the raw result before model rendering/truncation. Broker results are
// structs; MCP results may carry JSON text. task_output reports waited_ms only
// in tool-authored metadata, retained by the gateway under tool_metadata.
// Prefer an explicit output value (including zero or invalid) over metadata.
// Never traverse arbitrary nested payloads or fall back to timeout arguments.
func pollingResultFields(result toolExecutionResult) map[string]interface{} {
	sources := []map[string]interface{}{
		pollingOutputFields(result.Output),
		pollingOutputFields(extractToolTextOutput(result.Output)),
	}
	if result.Envelope != nil {
		metadata := result.Envelope.Metadata
		toolMetadata, _ := metadata["tool_metadata"].(map[string]interface{})
		sources = append(sources, toolMetadata, metadata)
	}
	fields := make(map[string]interface{})
	for _, source := range sources {
		for key, value := range source {
			if _, exists := fields[key]; !exists {
				fields[key] = value
			}
		}
	}
	return fields
}

func pollingOutputFields(value interface{}) map[string]interface{} {
	if fields, ok := value.(map[string]interface{}); ok {
		return fields
	}
	var data []byte
	switch value := value.(type) {
	case string:
		data = []byte(value)
	case []byte:
		data = value
	case json.RawMessage:
		data = value
	default:
		var err error
		data, err = json.Marshal(value)
		if err != nil {
			return nil
		}
	}
	var fields map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil
	}
	return fields
}

// Ready/terminal evidence and interrupted/error returns end the waiting streak;
// they must retain their own next_action, even on a threshold-crossing batch.
func pollingResultEndsStreak(fields map[string]interface{}) bool {
	for _, key := range []string{"terminal", "summary_ready", "interrupted", "isError"} {
		if value, _ := fields[key].(bool); value {
			return true
		}
	}
	if ok, exists := fields["ok"].(bool); exists && !ok {
		return true
	}
	if count, ok := pollingResultMillis(fields["ready_count"]); ok && count > 0 {
		return true
	}
	for _, key := range []string{"ready_ids", "terminal_delta"} {
		switch values := fields[key].(type) {
		case []string:
			if len(values) > 0 {
				return true
			}
		case []interface{}:
			if len(values) > 0 {
				return true
			}
		}
	}
	status, _ := fields["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready", "idle", "blocked", "waiting_approval", "waiting_input",
		"completed", "done", "closed", "terminated", "failed", "partially_completed",
		"canceled", "cancelled", "interrupted":
		return true
	}
	if outcome, _ := fields["outcome"].(string); outcome == "failed" {
		return true
	}
	if condition, _ := fields["wait_condition"].(string); condition == "output" || condition == "exit" {
		return true
	}
	next, _ := fields["next_action"].(string)
	next = strings.ToLower(strings.TrimSpace(strings.SplitN(next, ":", 2)[0]))
	return next == "finalize" || next == "consume_mailbox_events" ||
		next == "steer_pending" || next == "target_not_found" ||
		strings.HasPrefix(next, "consume_ready_outputs")
}

func pollingResultMillis(raw interface{}) (int64, bool) {
	var millis int64
	switch value := raw.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false
		}
		return int64(math.Min(value, float64(maxPollingWaitMillis))), true
	case float32:
		return pollingResultMillis(float64(value))
	case int:
		millis = int64(value)
	case int32:
		millis = int64(value)
	case int64:
		millis = value
	case uint:
		return pollingResultMillis(uint64(value))
	case uint32:
		millis = int64(value)
	case uint64:
		return int64(min(value, uint64(maxPollingWaitMillis))), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		millis = parsed
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		millis = parsed
	default:
		return 0, false
	}
	if millis < 0 {
		return 0, false
	}
	return min(millis, maxPollingWaitMillis), true
}

// pollingWaitBudgetAdvisory uses the same actual-wait observation as the event.
func pollingWaitBudgetAdvisory(tools []string, cumulativeWait time.Duration, repeatCount int) string {
	names := strings.Join(dedupeToolNames(tools), "/")
	if names == "" {
		names = "polling tool"
	}
	return fmt.Sprintf(
		"Runtime advisory: the polling/control request (%s) has accumulated %s of actual waiting (sum of tool-result waited_ms) across %d consecutive polling batches with unchanged target ids / after_seq. Execution was not blocked. This soft notice applies only to the current waiting streak in this run, not a context/token budget or a session-wide quota. Follow the result's next_action, consume ready outputs and do available independent work before another bounded wait. Increasing timeout_ms alone is not progress. This notice does not authorize finalizing while required work remains pending.",
		names, formatPollingWaitWindow(cumulativeWait), repeatCount,
	)
}

func formatPollingWaitWindow(wait time.Duration) string {
	if wait <= 0 {
		return "0s"
	}
	return wait.Round(time.Second).String()
}

// pollingBackoffAdvisory names the repeated polling request and points at the
// result-level next_action instead of another unchanged poll.
func pollingBackoffAdvisory(tools []string, repeatCount int) string {
	names := strings.Join(dedupeToolNames(tools), "/")
	if names == "" {
		names = "polling tool"
	}
	return fmt.Sprintf(
		"Runtime advisory: the same polling/control request (%s) has run %d consecutive times with unchanged target ids / after_seq (timing-only changes do not reset the streak). Execution was not blocked. This soft notice applies only to the current waiting streak in this run, not a context/token budget or a session-wide quota. Follow the result's next_action, consume ready outputs and do available independent work before another bounded wait instead of re-polling unchanged. This notice does not authorize finalizing while required work remains pending.",
		names, repeatCount,
	)
}

func dedupeToolNames(tools []string) []string {
	seen := make(map[string]struct{}, len(tools))
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		out = append(out, tool)
	}
	return out
}
