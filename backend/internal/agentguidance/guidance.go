// Package agentguidance holds the collaboration discipline the model reads on
// more than one surface: the system-prompt guidance rendered by
// internal/prompt and the collaboration tool descriptions in
// internal/toolbroker.
//
// The two surfaces drifted before (plan P2-11 方案 2): the prompt told the parent
// to prefer the longest affordable wait while the wait_agent schema still
// advertised a bare "optional wait timeout in milliseconds", so the model could
// not know the default, the configured bounds, or that an out-of-range window is
// rejected instead of clamped. Every sentence that must read identically on both
// surfaces lives here; callers reference the constants instead of re-typing the
// text.
package agentguidance

import (
	"fmt"
	"strings"
)

const (
	// WaitBudgetRule is the wait discipline shared by the parent guidance and
	// the wait_agent tool description.
	WaitBudgetRule = "When you must wait, wait for all children you still need in one call and prefer the longest timeout you can afford; if the wait times out, follow next_action, use any ready outputs, and only wait again after the remaining independent work is done."

	// WaitEscalationRule states that timing-only changes are not progress. The
	// polling guard ignores timing-only arguments when it counts repeated
	// polls, so a parent that keeps raising timeout_ms without doing anything
	// else still trips the backoff advisory and the cumulative wait budget.
	WaitEscalationRule = "Raising the wait timeout between repeated waits is not progress: timing-only changes do not count as new work, so raise the timeout once and use the wait to finish independent work instead of waiting again."

	// WaitResultEchoNote documents the fields that make a bounded wait
	// auditable, so a shortened observation is never silent.
	WaitResultEchoNote = "The host normalizes every wait window: the result echoes wait_timeout_requested_ms and sets wait_timeout_clamped=true when the effective window differs from the request, so a shortened wait is never silent."

	// EventsWaitNonBlockingNote documents the read_agent_events divergence:
	// wait_ms=0 means "do not block", not "use the default".
	EventsWaitNonBlockingNote = "Omit it (or pass 0) for a non-blocking read: wait_ms=0 keeps the immediate-return semantics and is not treated as a request for the default window."
)

// WaitTimeoutArgText renders the shared wait_agent timeout_ms description from
// the same bounds the host enforces (agentcontrol.WaitTimeoutPolicy), so the
// schema can never advertise a window the runtime would reject or silently
// shorten.
func WaitTimeoutArgText(defaultMs, minMs, maxMs int) string {
	return fmt.Sprintf(
		"Optional wait timeout in milliseconds. Omitted or <=0 means the host default (%dms). Values are bounded by agents.minWaitTimeoutMs (%dms) and agents.maxWaitTimeoutMs (%dms): agents.waitTimeoutMode=error rejects an out-of-range timeout_ms with an actionable message, agents.waitTimeoutMode=clamp pins it to the nearest bound. %s",
		defaultMs, minMs, maxMs, WaitResultEchoNote,
	)
}

// EventsWaitArgText renders the read_agent_events wait_ms description. It is a
// separate function because wait_ms=0 means "non-blocking read" rather than
// "use the default", so the wait_agent text cannot be reused verbatim.
func EventsWaitArgText(minMs, maxMs int) string {
	return fmt.Sprintf(
		"Optional wait timeout in milliseconds while waiting for new events. %s Positive values are bounded by agents.minWaitTimeoutMs (%dms) and agents.maxWaitTimeoutMs (%dms): agents.waitTimeoutMode=error rejects an out-of-range wait_ms with an actionable message, agents.waitTimeoutMode=clamp pins it to the nearest bound.",
		EventsWaitNonBlockingNote, minMs, maxMs,
	)
}

// WaitDisciplineText joins the shared wait rules into the single block appended
// to the wait_agent tool description (all host variants).
func WaitDisciplineText() string {
	return strings.Join([]string{WaitBudgetRule, WaitEscalationRule}, " ")
}
