package commands

import (
	"context"
	"strings"
)

// localBatchConvergeHintLimit bounds how many child session ids one converge
// hint carries: the hint is read by a model inside the preflight text, and a
// 32-child batch would otherwise dominate the digest budget. The full matrix
// stays available through supervision_descendants.
const localBatchConvergeHintLimit = 8

// localFinishedChildSessionIDs lists the child sessions of a terminal batch.
//
// It is deliberately read-only (ListTasks on the durable batch store) and
// best-effort: a store error yields no hint rather than failing the lifecycle
// projection that already succeeded.
func localFinishedChildSessionIDs(ctx context.Context, host *localChatRuntimeHost, batchID string) []string {
	if ctx == nil || host == nil || host.SubagentBatches == nil {
		return nil
	}
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil
	}
	tasks, err := host.SubagentBatches.ListTasks(ctx, batchID)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		id := strings.TrimSpace(task.ChildSessionID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= localBatchConvergeHintLimit {
			break
		}
	}
	return ids
}

// localBatchConvergeHint renders the P1-C converge instruction for a BatchDone
// lifecycle row (plan §4.1/§4.1.1).
//
// The row itself is resolution=closed, so the only executable channel is the
// broker tool close_agent; control_descendant would be rejected by the
// evaluator on an already terminal row. The hint therefore names the tool and
// the concrete child sessions instead of relying on the bare recommended_action
// value, and it is appended to the human-readable reason so both the digest row
// and the /debug view carry it.
func localBatchConvergeHint(ctx context.Context, host *localChatRuntimeHost, batchID string) string {
	const instruction = "converge by closing the finished child sessions with close_agent"
	ids := localFinishedChildSessionIDs(ctx, host, batchID)
	if len(ids) == 0 {
		return instruction
	}
	return instruction + ": " + strings.Join(ids, ", ")
}
