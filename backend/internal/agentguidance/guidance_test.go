package agentguidance

import (
	"strings"
	"testing"
)

func TestWaitTimeoutArgTextDocumentsBoundsAndEcho(t *testing.T) {
	got := WaitTimeoutArgText(30000, 10000, 120000)
	for _, want := range []string{
		"host default (30000ms)",
		"agents.minWaitTimeoutMs (10000ms)",
		"agents.maxWaitTimeoutMs (120000ms)",
		"agents.waitTimeoutMode=error",
		"agents.waitTimeoutMode=clamp",
		"wait_timeout_requested_ms",
		"wait_timeout_clamped",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("wait timeout text must mention %q, got: %s", want, got)
		}
	}
}

func TestEventsWaitArgTextKeepsNonBlockingSemantics(t *testing.T) {
	got := EventsWaitArgText(10000, 120000)
	for _, want := range []string{
		"non-blocking read",
		"agents.minWaitTimeoutMs (10000ms)",
		"agents.maxWaitTimeoutMs (120000ms)",
		"agents.waitTimeoutMode=error",
		"agents.waitTimeoutMode=clamp",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent_events wait text must mention %q, got: %s", want, got)
		}
	}
	if strings.Contains(got, "host default") {
		t.Fatalf("wait_ms=0 must not be described as a default request, got: %s", got)
	}
}

func TestWaitDisciplineTextJoinsBothRules(t *testing.T) {
	got := WaitDisciplineText()
	for _, rule := range []string{WaitBudgetRule, WaitEscalationRule, WaitLedgerBoundaryRule} {
		if !strings.Contains(got, rule) {
			t.Fatalf("discipline text must contain %q, got: %s", rule, got)
		}
	}
	if strings.Contains(got, "next_action=finalize") {
		t.Fatal("child settlement must not instruct the parent to finalize")
	}
	for _, rule := range []string{
		"next_action=continue_parent_work",
		"Child completion never requires the parent to finalize or stop using tools",
		"pending_count>0 you must not finalize",
	} {
		if !strings.Contains(got, rule) {
			t.Fatalf("missing parent/child lifecycle boundary %q", rule)
		}
	}
}
