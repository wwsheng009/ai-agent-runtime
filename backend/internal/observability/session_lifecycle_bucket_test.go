package observability

import "testing"

func TestFailCategoryForSessionLifecycleCodes(t *testing.T) {
	for _, code := range []string{"SESSION_NOT_FOUND", "AGENT_SESSION_NOT_FOUND", "session_not_found"} {
		if got := failCategoryForCode(code); got != "session_lifecycle" {
			t.Fatalf("failCategoryForCode(%q) = %q, want session_lifecycle", code, got)
		}
	}
}
