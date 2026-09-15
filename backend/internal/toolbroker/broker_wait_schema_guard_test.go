package toolbroker

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentguidance"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// waitSchemaPropDescription reads one property description out of a tool
// definition so the guard below compares the text the model actually sees.
func waitSchemaPropDescription(t *testing.T, def types.ToolDefinition, property string) string {
	t.Helper()
	properties, ok := def.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: properties must be a map, got %#v", def.Name, def.Parameters["properties"])
	}
	spec, ok := properties[property].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: %s property missing, got %#v", def.Name, property, properties)
	}
	description, _ := spec["description"].(string)
	if strings.TrimSpace(description) == "" {
		t.Fatalf("%s: %s has an empty description", def.Name, property)
	}
	return description
}

// TestCollaborationToolSchemasShareWaitBoundsAndDiscipline is the runtime half
// of the drift guard for plan P2-11 方案 2+3: every collaboration tool
// definition a host can actually get must carry the shared wait discipline and
// the same wait-window contract the runtime enforces.
func TestCollaborationToolSchemasShareWaitBoundsAndDiscipline(t *testing.T) {
	wantTimeout := agentguidance.WaitTimeoutArgText(agentcontrol.DefaultWaitTimeoutMs, agentcontrol.MinWaitTimeoutMs, agentcontrol.MaxWaitTimeoutMs)
	wantWait := agentguidance.EventsWaitArgText(agentcontrol.MinWaitTimeoutMs, agentcontrol.MaxWaitTimeoutMs)

	brokers := map[string]*Broker{
		"agent-session-host": {AgentSessions: &fakeAgentSessionController{}},
		"bare-host":          {},
		"team-host":          {TeamStore: newTeamStore(t)},
	}
	exposed := 0
	teamExposed := 0
	for label, broker := range brokers {
		definitions := append(broker.Definitions(), broker.DefinitionsForContext(context.Background())...)
		seen := map[string]int{}
		for _, def := range definitions {
			switch def.Name {
			case ToolWaitAgent:
				seen[ToolWaitAgent]++
				if !strings.Contains(def.Description, agentguidance.WaitDisciplineText()) {
					t.Fatalf("%s: wait_agent must carry the shared wait discipline, got: %s", label, def.Description)
				}
				if !strings.Contains(def.Description, agentguidance.WaitEscalationRule) {
					t.Fatalf("%s: wait_agent must warn that timing-only changes are not progress, got: %s", label, def.Description)
				}
				if got := waitSchemaPropDescription(t, def, "timeout_ms"); got != wantTimeout {
					t.Fatalf("%s: wait_agent timeout_ms must describe the enforced bounds, got: %s", label, got)
				}
			case ToolReadAgentEvents:
				seen[ToolReadAgentEvents]++
				if got := waitSchemaPropDescription(t, def, "wait_ms"); got != wantWait {
					t.Fatalf("%s: read_agent_events wait_ms must describe the enforced bounds, got: %s", label, got)
				}
			case ToolWaitTeam:
				seen[ToolWaitTeam]++
				if !strings.Contains(def.Description, agentguidance.WaitDisciplineText()) {
					t.Fatalf("%s: wait_team must carry the shared wait discipline, got: %s", label, def.Description)
				}
				if got := waitSchemaPropDescription(t, def, "timeout_ms"); got != wantTimeout {
					t.Fatalf("%s: wait_team timeout_ms must describe the enforced bounds, got: %s", label, got)
				}
			}
		}
		if seen[ToolWaitAgent] > 0 && seen[ToolReadAgentEvents] > 0 {
			exposed++
		}
		if seen[ToolWaitTeam] > 0 {
			teamExposed++
		}
	}
	if exposed == 0 {
		t.Fatalf("no broker configuration exposed the collaboration wait tools; the runtime guard would be vacuous")
	}
	if teamExposed == 0 {
		t.Fatalf("no broker configuration exposed wait_team; the runtime guard would be vacuous")
	}

	if !strings.Contains(wantTimeout, "wait_timeout_clamped") {
		t.Fatalf("timeout_ms must document the clamp echo so a shortened wait is never silent: %s", wantTimeout)
	}
	if !strings.Contains(wantWait, "non-blocking read") {
		t.Fatalf("wait_ms=0 must stay documented as a non-blocking read, not a default request: %s", wantWait)
	}
}

// TestCollaborationToolSchemaSourceHasNoLegacyWaitText is the source half of the
// same guard. Some definition variants are only reachable for hosts this test
// cannot construct (mailbox-only parent vs child-session host), so the file
// itself is checked too: no definition may keep the pre-P2-11 bare wait text,
// and every wait_agent / read_agent_events entry must reference the shared
// helpers. Together with the shared constants this makes "synced in one variant
// only" impossible.
func TestCollaborationToolSchemaSourceHasNoLegacyWaitText(t *testing.T) {
	raw, err := os.ReadFile("broker.go")
	if err != nil {
		t.Fatalf("read broker.go: %v", err)
	}
	source := string(raw)
	for _, legacy := range []string{
		`"description": "Optional wait timeout in milliseconds."`,
		`"description": "Optional wait timeout while waiting for new events to arrive."`,
		`"description": "Optional wait timeout in milliseconds. Defaults to 30000."`,
	} {
		if strings.Contains(source, legacy) {
			t.Fatalf("legacy wait text still present: %s", legacy)
		}
	}

	lines := strings.Split(source, "\n")
	counts := map[string]int{}
	for i, line := range lines {
		switch {
		case strings.Contains(line, "Name:        ToolWaitAgent"):
			counts[ToolWaitAgent]++
			if !containsWithin(lines, i, 6, "agentguidance.WaitDisciplineText()") {
				t.Fatalf("wait_agent definition at broker.go:%d does not attach the shared discipline text", i+1)
			}
		case strings.Contains(line, "Name:        ToolReadAgentEvents"):
			counts[ToolReadAgentEvents]++
			if !containsWithin(lines, i, 24, "agentguidance.EventsWaitArgText(") {
				t.Fatalf("read_agent_events definition at broker.go:%d does not describe the enforced wait bounds", i+1)
			}
		case strings.Contains(line, "Name:        ToolWaitTeam"):
			counts[ToolWaitTeam]++
			if !containsWithin(lines, i, 24, "agentguidance.WaitTimeoutArgText(") {
				t.Fatalf("wait_team definition at broker.go:%d does not describe the enforced wait bounds", i+1)
			}
			if !containsWithin(lines, i, 6, "agentguidance.WaitDisciplineText()") {
				t.Fatalf("wait_team definition at broker.go:%d does not attach the shared discipline text", i+1)
			}
		}
	}
	if counts[ToolWaitAgent] < 2 || counts[ToolReadAgentEvents] < 2 || counts[ToolWaitTeam] < 1 {
		t.Fatalf("expected both host variants of each collaboration wait tool, got %v", counts)
	}
}

func containsWithin(lines []string, start, window int, needle string) bool {
	end := start + window
	if end > len(lines) {
		end = len(lines)
	}
	for _, line := range lines[start:end] {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
