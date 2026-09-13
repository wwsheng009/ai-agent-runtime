package supervision

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WakeBudgetClass groups wake reasons into independent auto-wake budgets
// (doc 6.5 rule 4, plan P1-6 方案 1). Before this split every critical event
// shared one budget, so a burst of child failures could starve an approval
// notification that the parent must answer to unblock a child.
//
// Classes are budget buckets, not severity levels: approval-class wakes are
// still coalesced by the durable dedup key, they simply do not consume the
// failure/other budget.
type WakeBudgetClass string

const (
	// WakeBudgetClassApproval covers events that block a child until the
	// parent decides (approval requests, questions, permission prompts).
	// The class has no hard cap by default: dropping or delaying it stalls
	// the child indefinitely.
	WakeBudgetClassApproval WakeBudgetClass = "approval"
	// WakeBudgetClassFailure covers abnormal child terminations and
	// execution failures/timouts. Bounded by MaxAutoWakePerWindow.
	WakeBudgetClassFailure WakeBudgetClass = "failure"
	// WakeBudgetClassOther is the catch-all for critical lifecycle events
	// that are neither approvals nor failures; it shares the bounded budget.
	WakeBudgetClassOther WakeBudgetClass = "other"
)

// Wake reasons mirrored from the projection-layer event types. Hosts may emit
// their own event types; WakeBudgetClassOf falls back to substring matching
// so unknown-but-descriptive reasons still land in the right bucket.
const (
	WakeReasonApprovalRequired = "approval_required"
	WakeReasonQuestionAsked    = "question_asked"
	WakeReasonExecutionFailed  = "execution_failed"
	WakeReasonExecutionTimeout = "execution_timeout"
	WakeReasonLifecycleFailed  = "lifecycle_failed"
)

// WakeBudgetClassOf maps a wake reason (normally the durable notification
// event type) to its budget class. Matching is case-insensitive and
// substring-based so host-specific event types such as "agent_failed" or
// "child_stalled" are classified without a central registry.
func WakeBudgetClassOf(reason string) WakeBudgetClass {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	if normalized == "" {
		return WakeBudgetClassOther
	}
	if wakeReasonContainsAny(normalized,
		"approval", "approve", "question", "input_required", "permission", "authorize", "confirm",
	) {
		return WakeBudgetClassApproval
	}
	if wakeReasonContainsAny(normalized,
		"fail", "failure", "error", "timeout", "timed_out", "stalled", "stall", "lost", "unhealthy", "panic",
	) {
		return WakeBudgetClassFailure
	}
	return WakeBudgetClassOther
}

// Bounded reports whether the class consumes the shared auto-wake budget
// (MaxAutoWakePerWindow). Approval-class wakes are governed by their own
// budget instead.
func (c WakeBudgetClass) Bounded() bool {
	return c != WakeBudgetClassApproval
}

func wakeReasonContainsAny(reason string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}

// WakeBudgetState is the per-root-scope budget snapshot used by tests and by
// host diagnostics (P0-4 `/debug`). It is a read-only projection; mutating it
// has no effect on the scheduler.
type WakeBudgetState struct {
	RootScopeID string          `json:"root_scope_id,omitempty"`
	BudgetClass WakeBudgetClass `json:"budget_class,omitempty"`
	Limit       int             `json:"limit,omitempty"`
	Used        int             `json:"used,omitempty"`
	Window      time.Duration   `json:"window,omitempty"`
	WindowStart time.Time       `json:"window_start,omitempty"`
	// Unlimited is true when the class has no hard cap.
	Unlimited bool `json:"unlimited,omitempty"`
}

// FormatWakeBudgetLine renders the per-class auto-wake ledger as one compact
// line for the model-visible preflight digest (plan P1-6 follow-up). The line
// closes the last silent-deferral gap of the wake path: a class at its limit
// keeps the next wake durable instead of delivering it, and before this line
// the parent model could only discover that through host diagnostics
// (`/debug supervision list`) after wondering why a child stopped reporting.
//
// Rows keep the caller's class order so the API preflight, the HTTP projection
// and the CLI `/debug` view render the same ledger. An empty slice renders
// nothing, which keeps the digest byte-identical for hosts without a wired
// scheduler.
func FormatWakeBudgetLine(states []WakeBudgetState) string {
	if len(states) == 0 {
		return ""
	}
	rows := make([]string, 0, len(states))
	for _, state := range states {
		class := strings.TrimSpace(string(state.BudgetClass))
		if class == "" {
			class = string(WakeBudgetClassOther)
		}
		limit := strconv.Itoa(state.Limit)
		if state.Unlimited {
			limit = "unlimited"
		}
		rows = append(rows, fmt.Sprintf("%s=%d/%s", class, state.Used, limit))
	}
	return "wake_budget: " + strings.Join(rows, " ") + " (exhausted classes defer wakes, not drop them)"
}
