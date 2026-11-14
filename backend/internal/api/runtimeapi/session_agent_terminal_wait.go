package runtimeapi

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// terminalWaitResults reads durable results without starting actors or opening
// an observation window, including batch tasks without a child session row.
func (c *sessionAgentController) terminalWaitResults(ctx context.Context, obligation toolbroker.AgentWaitObligation) ([]toolbroker.AgentStatusResult, error) {
	if obligation.SubjectKind == "batch" {
		store := c.handler.peekSubagentBatchStore()
		batch, err := store.GetBatch(ctx, obligation.SubjectID)
		if err != nil {
			return nil, err
		}
		tasks, err := store.ListTasks(ctx, obligation.SubjectID)
		if err != nil {
			return nil, err
		}
		if batch == nil || len(tasks) < batch.TaskCount {
			return nil, fmt.Errorf("terminal wait: incomplete task ledger for batch %s", obligation.SubjectID)
		}
		results := make([]toolbroker.AgentStatusResult, 0, len(tasks))
		for _, task := range tasks {
			id := firstNonEmptyString(task.ChildSessionID, task.TaskID)
			results = append(results, *apiBatchTaskStatusResult(id, obligation.SubjectID, task))
		}
		return results, nil
	}
	result, err := c.snapshot(ctx, obligation.SubjectID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("terminal wait: child result unavailable for %s", obligation.SubjectID)
	}
	return []toolbroker.AgentStatusResult{*result}, nil
}
