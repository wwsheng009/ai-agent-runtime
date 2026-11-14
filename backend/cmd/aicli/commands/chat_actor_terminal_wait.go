package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// localTerminalWaitResults reads results without materializing child actors or
// waiting for new events. Batch workers need not have a SessionStore row (and
// canceled-before-start tasks need not have a child session id).
func (r *localActorRegistry) localTerminalWaitResults(ctx context.Context, obligation toolbroker.AgentWaitObligation) ([]toolbroker.AgentStatusResult, error) {
	if obligation.SubjectKind == "batch" {
		store := r.Host.SubagentBatches
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
			id := firstNonEmptyChatValue(task.ChildSessionID, task.TaskID)
			results = append(results, *localBatchTaskStatusResult(id, obligation.SubjectID, task))
		}
		return results, nil
	}
	result, err := r.agentSnapshot(ctx, obligation.SubjectID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("terminal wait: child result unavailable for %s", obligation.SubjectID)
	}
	if result.Exists && result.Output == "" && r.Host.SessionStore != nil {
		// agentSnapshot uses LoadMetadata for cheap polling. The terminal read
		// is result-bearing, so load history once instead of claiming that the
		// parent consumed a metadata-only snapshot.
		session, err := r.Host.SessionStore.Load(ctx, obligation.SubjectID)
		if err != nil {
			return nil, err
		}
		if session != nil {
			messages := session.GetMessages()
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "assistant" {
					result.Output = strings.TrimSpace(messages[i].Content)
					break
				}
			}
		}
	}
	return []toolbroker.AgentStatusResult{*result}, nil
}
