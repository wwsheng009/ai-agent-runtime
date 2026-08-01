package renderengine

import (
	"strings"
	"testing"
)

func TestHandoffPlanANSIIsCursorNeutralAndBounded(t *testing.T) {
	plan := NewHandoffPlan(20, 13, []string{"one", "two"})
	got := plan.ANSI()
	for _, want := range []string{"\x1b[s", "\x1b[1;13r", "\x1b[13;1H", "\r\none", "\r\ntwo", "\x1b[20r", "\x1b[u"} {
		if !strings.Contains(got, want) {
			t.Fatalf("handoff ANSI %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "T") {
		t.Fatalf("handoff must not use CSI scroll-down: %q", got)
	}
}

func TestHandoffPlanCopiesRows(t *testing.T) {
	rows := []string{"before"}
	plan := NewHandoffPlan(10, 4, rows)
	rows[0] = "after"
	if got := plan.Rows()[0]; got != "before" {
		t.Fatalf("plan retained caller mutation: %q", got)
	}
}
