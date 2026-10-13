package agent

import (
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// EnvReportSubagents enables appending live subagent rows to the session list.
// Off by default: most consoles only need the parent row, and a busy host can
// run many children at once (docs/gateway/07-console.md).
const EnvReportSubagents = "AICLI_GATEWAY_REPORT_SUBAGENTS"

// SubagentSessionMax bounds how many child rows one session list carries, so a
// wide fan-out cannot flood the console (or the SESSION_UPDATE payload).
const SubagentSessionMax = 8

// ReportSubagentsEnabled resolves the switch from the environment (getenv may be
// nil → os.Getenv). Any of 1/true/on/yes enables it; anything else keeps the
// historical parent-only session list.
func ReportSubagentsEnabled(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvReportSubagents))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// SubagentSessionSummary builds the display-only console row for one live
// subagent task: a child session linked to its parent (wire.SessionParent) with
// the coarse running state and no attention of its own. Both hosts build these
// rows through this helper so the wire shape cannot drift.
func SubagentSessionSummary(parentSessionID, childSessionID, agentID, agentType, title string, startedAt time.Time) wire.SessionSummary {
	summary := wire.SessionSummary{
		SessionID: strings.TrimSpace(childSessionID),
		Title:     strings.TrimSpace(title),
		State:     wire.SessionStateRunning,
		Parent: &wire.SessionParent{
			ParentSessionID: strings.TrimSpace(parentSessionID),
			AgentID:         strings.TrimSpace(agentID),
			AgentType:       strings.TrimSpace(agentType),
		},
	}
	if !startedAt.IsZero() {
		summary.StatusStartedAt = startedAt
		summary.UpdatedAt = startedAt
	}
	return summary
}
