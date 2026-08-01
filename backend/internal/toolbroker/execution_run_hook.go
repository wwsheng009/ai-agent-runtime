package toolbroker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// Execution supervision policy string returned to the parent (doc 7.1). P3
// enforcement sends an interrupt request when the deadline expires and fails
// the run after the cancel grace; observe mode only records.
const spawnSupervisionPolicyEnforce = "interrupt_then_fail"

// startSpawnExecutionRun registers a durable execution run for a freshly
// created child session (doc 7.1: run_<uuid> per submission). It is a
// side-channel: any failure is reported through metadata by the caller and
// never blocks the spawn itself.
//
// RootSessionID is approximated with the parent session when the child result
// does not carry a root scope; the supervision projection keys on this scope
// and P4/P5 refine it with the durable descendant graph.
func startSpawnExecutionRun(ctx context.Context, supervisor *supervision.ExecutionSupervisor, parentSessionID string, args SpawnAgentArgs, result *AgentStatusResult) (string, string, error) {
	if supervisor == nil {
		return "", "", nil
	}
	if result == nil || strings.TrimSpace(result.SessionID) == "" {
		return "", "", fmt.Errorf("child session id is required for execution run registration")
	}
	if args.TimeoutSec < 0 || args.ProgressTimeoutSec < 0 || args.ApprovalTimeoutSec < 0 || args.CancelGraceSec < 0 {
		return "", "", fmt.Errorf("supervision timeouts must be non-negative")
	}
	spec := supervision.RunSpec{
		Workflow:         supervision.RunWorkflowSpawnAgent,
		RootSessionID:    strings.TrimSpace(parentSessionID),
		ParentSessionID:  strings.TrimSpace(parentSessionID),
		SessionID:        strings.TrimSpace(result.SessionID),
		AgentID:          strings.TrimSpace(firstNonEmptyToolValue(result.SessionID, result.ID)),
		ExecutionTimeout: secondsToDuration(args.TimeoutSec),
		ProgressTimeout:  secondsToDuration(args.ProgressTimeoutSec),
		ApprovalTimeout:  secondsToDuration(args.ApprovalTimeoutSec),
		CancelGrace:      secondsToDuration(args.CancelGraceSec),
	}
	run, err := supervisor.StartRun(ctx, spec)
	if err != nil {
		return "", "", fmt.Errorf("register execution run: %w", err)
	}
	deadline := ""
	if run.ExecutionDeadlineAt != nil && !run.ExecutionDeadlineAt.IsZero() {
		deadline = run.ExecutionDeadlineAt.UTC().Format(time.RFC3339)
	}
	result.RunID = run.RunID
	result.ExecutionDeadlineAt = deadline
	result.SupervisionPolicy = spawnSupervisionPolicyEnforce
	return run.RunID, deadline, nil
}

func secondsToDuration(sec int64) time.Duration {
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}
