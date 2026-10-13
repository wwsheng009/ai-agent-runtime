package agent

import (
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// EnvReportSubagents opts OUT of appending live subagent rows to the session
// list. Rows are ON by default (one per in-flight child task): the console groups
// them under their parent and folds them with one click, so a separate opt-in
// switch would just be another thing to remember. Set
// AICLI_GATEWAY_REPORT_SUBAGENTS=0/false/off to keep the parent-only list
// (docs/gateway/07-console.md).
const EnvReportSubagents = "AICLI_GATEWAY_REPORT_SUBAGENTS"

// SubagentSessionMax bounds how many child rows one session list carries, so a
// wide fan-out cannot flood the console (or the SESSION_UPDATE payload).
const SubagentSessionMax = 8

// ReportSubagentsEnabled resolves the opt-out switch from the environment
// (getenv may be nil → os.Getenv). Only an explicit 0/false/off/no disables the
// child rows; anything else — including unset — keeps them on.
func ReportSubagentsEnabled(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvReportSubagents))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
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
