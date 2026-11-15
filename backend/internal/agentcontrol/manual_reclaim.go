package agentcontrol

import (
	"fmt"
	"strings"
	"time"
)

// ManualReclaimReport is the host-neutral projection of one `/agents cleanup`
// pass. CLI local mode and the runtime-server HTTP adapter build the same
// report so dry-run/executed output cannot drift.
type ManualReclaimReport struct {
	RootSessionID          string
	QuotaChildren          int
	Decisions              []ReclaimDecision
	Outcome                ReclaimOutcome
	HasOutcome             bool
	DryRun                 bool
	Idle                   time.Duration
	RemainingQuotaChildren int
}

// Render is the human-readable cleanup result used by both hosts.
func (r ManualReclaimReport) Render() string {
	lines := []string{"Agent Registry Cleanup:"}
	lines = append(lines, fmt.Sprintf("  root_session=%s quota_children=%d reclaimable=%d idle_policy=%s",
		strings.TrimSpace(r.RootSessionID), r.QuotaChildren, len(r.Decisions), idlePolicyText(r.Idle)))
	if len(r.Decisions) == 0 {
		lines = append(lines, "  没有可安全回收的子 agent（running / waiting_approval / waiting_input 永不回收）")
	}
	if r.DryRun {
		for _, decision := range r.Decisions {
			lines = append(lines, decisionLine("would_reclaim", decision))
		}
		lines = append(lines, "  dry_run=true（未执行任何回收）")
		return strings.Join(lines, "\n")
	}
	if !r.HasOutcome {
		return strings.Join(lines, "\n")
	}
	summary := r.Outcome.Summary()
	if summary == "" {
		summary = "reclaimed=0"
	}
	if human := r.Outcome.HumanSummary(); human != "" {
		lines = append(lines, "  "+human)
	}
	lines = append(lines, "  详情: "+summary)
	reclaimed := make(map[string]string, len(r.Outcome.Decisions))
	for _, decision := range r.Outcome.Decisions {
		reclaimed[decision.AgentPath] = decision.Reason
	}
	for _, decision := range r.Decisions {
		if reason, closed := reclaimed[decision.AgentPath]; closed {
			decision.Reason = reason
			lines = append(lines, decisionLine("reclaimed", decision))
			continue
		}
		lines = append(lines, decisionLine("reclaim_failed", decision))
	}
	lines = append(lines, fmt.Sprintf("  remaining_quota_children=%d", r.RemainingQuotaChildren))
	if r.Outcome.Failed > 0 && strings.TrimSpace(r.Outcome.FirstError) != "" {
		lines = append(lines, "  first_error="+strings.TrimSpace(r.Outcome.FirstError))
	}
	lines = append(lines, "  提示: /agents cleanup --dry-run 预览；--idle 30m 额外回收长期空闲对象")
	return strings.Join(lines, "\n")
}

func decisionLine(prefix string, decision ReclaimDecision) string {
	fields := []string{"  " + prefix}
	if path := strings.TrimSpace(decision.AgentPath); path != "" {
		fields = append(fields, "path="+path)
	}
	if sessionID := strings.TrimSpace(decision.SessionID); sessionID != "" {
		fields = append(fields, "session="+sessionID)
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		fields = append(fields, "reason="+reason)
	}
	return strings.Join(fields, " ")
}

func idlePolicyText(idle time.Duration) string {
	if idle <= 0 {
		return "disabled"
	}
	return idle.String()
}
