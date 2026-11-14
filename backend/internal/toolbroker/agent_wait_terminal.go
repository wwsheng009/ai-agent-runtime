package toolbroker

import (
	"context"
	"fmt"
	"time"
)

// BuildTerminalAgentWaitResult performs one read of the entire parked cohort.
// An all-terminal ledger means "do not wait", not "omit the results": returning
// an empty finalize result leaves the durable ResultsConsumed gate closed and
// the parent parks again after its only settlement wake was already delivered.
//
// Hosts supply their existing durable batch/session readers. Targets come only
// from the caller's ledger, not model-supplied ids; even a wait for the last
// child must deliver all results before the per-turn consumption gate can close.
func BuildTerminalAgentWaitResult(
	ctx context.Context,
	observed *AgentWaitResult,
	obligations []AgentWaitObligation,
	baseline []string,
	load func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error),
) (*AgentWaitResult, error) {
	startedAt := time.Now()
	result := &AgentWaitResult{}
	if observed != nil {
		// Keep window timing, mailbox events and interruption metadata when a
		// formerly pending ledger settles during the observation.
		*result = *observed
	}
	result.Agent, result.Agents = nil, nil
	result.ReadyCount, result.PendingCount = 0, 0
	result.ReadyIDs, result.PendingIDs = nil, nil
	result.MatchedID, result.MatchedSessionID = "", ""
	result.Synthesis = nil
	result.ExecutionContinues = false
	seen := make(map[string]bool)
	for _, obligation := range obligations {
		if !obligation.Terminal {
			return nil, fmt.Errorf("terminal wait requires a terminal ledger: %s", obligation.ObligationID)
		}
		if obligation.SubjectKind != "batch" && obligation.SubjectKind != "agent_session" {
			continue
		}
		if load == nil {
			return nil, fmt.Errorf("terminal wait result reader is unavailable")
		}
		agents, err := load(ctx, obligation)
		if err != nil {
			return nil, err
		}
		for _, item := range agents {
			id := firstNonEmptySynthesisValue(item.SessionID, item.ID)
			// Unstarted tasks can share a task id across different batches.
			// Deduplicate only within the same obligation, never across them.
			key := obligation.ObligationID + "|" + id + "|" + item.CurrentTaskID
			if seen[key] {
				continue
			}
			seen[key] = true
			result.Agents = append(result.Agents, item)
			status := firstNonEmptySynthesisValue(item.CurrentTaskStatus, item.Status)
			if item.Exists && synthesisStatusClass(status) != "pending" {
				result.ReadyCount++
				result.ReadyIDs = append(result.ReadyIDs, id)
				if result.Agent == nil {
					copy := item
					result.Agent = &copy
					result.MatchedID = item.ID
					result.MatchedSessionID = item.SessionID
				}
			} else {
				// Missing data or a restarted child is not evidence of consumed
				// results, even if an older lifecycle projection was terminal.
				result.PendingCount++
				result.PendingIDs = append(result.PendingIDs, id)
			}
		}
	}
	if observed != nil {
		startedAt = time.Time{}
	}
	result = ApplyAgentWaitLedger(FinalizeAgentWaitResult(result, startedAt), obligations, baseline)
	if result.PendingCount > 0 {
		result.NextAction = "inspect: terminal ledger has unavailable or non-terminal child results; inspect subagent_status / subagent_inspect_task before finalizing"
		if result.Synthesis != nil {
			result.Synthesis.NextAction = result.NextAction
		}
	}
	return result, nil
}
