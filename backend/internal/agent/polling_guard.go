package agent

import (
	"crypto/sha256"
	"fmt"
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
//   - A streak is counted per identical polling batch (tool + normalized args
//     digest, which covers target ids, after_seq and timeout_ms).
//   - Any non-exempt tool in the batch (real work) resets the streak.
//   - Threshold, advisory and product event fire once per streak.
const (
	// PollingBackoffNoticeThreshold is the consecutive identical polling count
	// at which the loop starts injecting the backoff advisory (default; see
	// LoopReActConfig.MaxRepeatedPollCalls for the override).
	PollingBackoffNoticeThreshold = 3

	// PollingWaitBudgetNoticeThreshold is the total blocking wait window
	// (sum of the polling timeout_ms / wait_ms arguments) after which the guard
	// escalates from "the same request repeated" to "this turn has spent this
	// long waiting". It covers the signature the repeat threshold alone misses:
	// the model answers the advisory by escalating timeout_ms (5m -> 7m -> 10m),
	// each escalation is a different request fingerprint, and the parent turn
	// stays visually frozen for tens of minutes without a single notice.
	PollingWaitBudgetNoticeThreshold = 5 * time.Minute

	// EventPollingBackoffObserved is emitted once per polling streak when the
	// threshold is crossed.
	EventPollingBackoffObserved = "tool_loop.polling_backoff_observed"
)

// PollingBackoffObservation is the per-turn result of ObserveToolBatch.
type PollingBackoffObservation struct {
	Fingerprint string
	// Tools lists the polling tool names participating in the streak.
	Tools []string
	// RepeatCount counts consecutive observations of the same polling request
	// (scheduling-only arguments such as timeout_ms are excluded).
	RepeatCount int
	// CumulativeWait is the blocking wait requested across the current streak.
	CumulativeWait time.Duration
	// WaitBudgetExceeded reports that CumulativeWait crossed
	// PollingWaitBudgetNoticeThreshold, including when each poll used a
	// different timeout_ms and therefore never repeated identically.
	WaitBudgetExceeded bool
	// EmitNotice is true exactly once per streak (crossing the threshold).
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

// NewPollingBackoffTracker constructs a tracker. threshold <= 0 falls back to
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

// CumulativeWait reports the blocking wait requested across the current streak.
func (t *PollingBackoffTracker) CumulativeWait() time.Duration {
	if t == nil {
		return 0
	}
	return t.cumulativeWait
}

// ObserveToolBatch updates consecutive-polling state for the model tool batch.
// Batches without polling tools, or mixing polling with real work, reset the
// streak so genuine progress is never nagged.
func (t *PollingBackoffTracker) ObserveToolBatch(calls []types.ToolCall) PollingBackoffObservation {
	obs := PollingBackoffObservation{}
	if t == nil || t.threshold <= 0 || len(calls) == 0 {
		return obs
	}
	fingerprint, tools := pollingBatchFingerprint(calls)
	if fingerprint == "" {
		t.reset()
		return obs
	}
	requestedWait := pollingBatchRequestedWait(calls)
	if fingerprint == t.lastFingerprint {
		t.repeatCount++
		t.cumulativeWait += requestedWait
	} else {
		t.lastFingerprint = fingerprint
		t.repeatCount = 1
		t.cumulativeWait = requestedWait
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
// excluded from the streak fingerprint because escalating timeout_ms is exactly
// the guidance the advisory gives; treating every escalation as a new request
// would let the model reset the brake indefinitely (5m -> 7m -> 10m ...).
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

// pollingWaitBudgetArgs are the scheduling keys whose unit is unambiguously
// milliseconds, stored lower-cased so provider casing variants (timeout_ms /
// timeoutMs / timeoutMS) are all read. A bare "timeout" key is excluded: it
// keeps its unit-agnostic meaning for the fingerprint but is never summed.
var pollingWaitBudgetArgs = map[string]struct{}{
	"timeout_ms": {},
	"timeoutms":  {},
	"wait_ms":    {},
	"waitms":     {},
	"poll_ms":    {},
	"pollms":     {},
}

// maxPollingWaitMillis bounds one poll's contribution so a malformed argument
// cannot overflow the accumulated duration.
const maxPollingWaitMillis = int64(24 * time.Hour / time.Millisecond)

func pollingBatchRequestedWait(calls []types.ToolCall) time.Duration {
	total := time.Duration(0)
	for _, call := range calls {
		// Alias spellings inside one call are never summed: take the largest
		// so a duplicated key cannot inflate the accumulated wait window.
		requested := int64(0)
		for key, raw := range call.Args {
			if _, ok := pollingWaitBudgetArgs[strings.ToLower(strings.TrimSpace(key))]; !ok {
				continue
			}
			millis, ok := pollingArgMillis(raw)
			if !ok || millis <= 0 {
				continue
			}
			if millis > maxPollingWaitMillis {
				millis = maxPollingWaitMillis
			}
			if millis > requested {
				requested = millis
			}
		}
		total += time.Duration(requested) * time.Millisecond
	}
	return total
}

// pollingJSONNumber mirrors encoding/json.Number for arguments decoded with
// UseNumber (the toolexec preflight path does this) without importing
// encoding/json here.
type pollingJSONNumber interface {
	Int64() (int64, error)
}

func pollingArgMillis(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case float32:
		return int64(value), true
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case pollingJSONNumber:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// pollingWaitBudgetAdvisory leads with the accumulated blocking wait instead of
// the repeat count: it is the case where the model keeps escalating timeout_ms
// and therefore never sees the identical-request advisory.
func pollingWaitBudgetAdvisory(tools []string, cumulativeWait time.Duration, repeatCount int) string {
	names := strings.Join(dedupeToolNames(tools), "/")
	if names == "" {
		names = "polling tool"
	}
	return fmt.Sprintf(
		"Runtime advisory: the polling/control request (%s) has now requested %s of blocking wait across %d consecutive polls with unchanged target ids / after_seq. Execution was not blocked, but waiting longer is not progress: pending work keeps running without this turn. Stop re-issuing wait_agent with a larger timeout_ms; use this turn for independent work or a status update, and wait again only after a terminal event or a ready output is observed.",
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
		"Runtime advisory: the same polling/control request (%s) has run %d consecutive times with identical arguments (target ids / after_seq / timeout_ms). Execution was not blocked. Follow next_action instead of re-polling unchanged: use wait_agent once with a larger timeout_ms when a specific child must finish, do other independent work first, or proceed and let the parent preflight digest surface new events.",
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
