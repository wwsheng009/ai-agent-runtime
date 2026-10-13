package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionSummaryParentRoundTrip pins the display-only child-row metadata:
// top-level sessions keep the field absent (older consoles unchanged), and a
// child row survives a JSON round trip byte-for-byte in meaning.
func TestSessionSummaryParentRoundTrip(t *testing.T) {
	plain, err := json.Marshal(SessionSummary{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	var plainDecoded SessionSummary
	if err := json.Unmarshal(plain, &plainDecoded); err != nil {
		t.Fatal(err)
	}
	if plainDecoded.Parent != nil {
		t.Fatalf("top-level session must not carry a parent field: %s", plain)
	}
	if strings.Contains(string(plain), `"parent"`) {
		t.Fatalf("parent must stay omitted for top-level sessions: %s", plain)
	}

	raw, err := json.Marshal(SessionSummary{
		SessionID: "subagent_a",
		Title:     "切片 5",
		State:     SessionStateRunning,
		Parent: &SessionParent{
			ParentSessionID: "s1",
			AgentID:         "task-a",
			AgentType:       "implement",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionSummary
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Parent == nil {
		t.Fatalf("parent lost in round trip: %s", raw)
	}
	if decoded.Parent.ParentSessionID != "s1" || decoded.Parent.AgentID != "task-a" || decoded.Parent.AgentType != "implement" {
		t.Fatalf("parent fields = %+v", *decoded.Parent)
	}
}
