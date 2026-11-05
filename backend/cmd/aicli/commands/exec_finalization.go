package commands

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// buildExecFinalizationIncomplete preserves the durable child results when the
// headless parent hits its deadline. The result is best-effort: a missing
// control plane still yields a structured finalization_incomplete document, just
// without child rows.
func buildExecFinalizationIncomplete(ctx context.Context, session *ExecSession) *ExecFinalizationIncomplete {
	finalization := &ExecFinalizationIncomplete{Reason: "parent_exec_timeout"}
	if session == nil || session.ChatSession == nil {
		return finalization
	}
	host := session.ChatSession.LocalRuntimeHost
	if host == nil || host.SubagentBatches == nil {
		return finalization
	}
	store := host.SubagentBatches
	parentSessionID := strings.TrimSpace(currentRuntimeSessionID(session.ChatSession))
	if parentSessionID == "" {
		return finalization
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	turnID := strings.TrimSpace(host.localTurnIDForSession(ctx, parentSessionID))
	record := execTurnSuspension(ctx, store, parentSessionID, turnID)
	if record != nil && turnID == "" {
		turnID = strings.TrimSpace(record.TurnID)
	}

	seenBatch := make(map[string]struct{})
	seenChild := make(map[string]struct{})
	addBatch := func(batch *subagentbatch.SubagentBatch) {
		if batch == nil {
			return
		}
		batchID := strings.TrimSpace(batch.BatchID)
		if batchID == "" {
			return
		}
		if _, exists := seenBatch[batchID]; exists {
			return
		}
		seenBatch[batchID] = struct{}{}
		finalization.BatchIDs = append(finalization.BatchIDs, batchID)

		packet := agent.BuildBatchSynthesisPacket(ctx, store, batch)
		if packet == nil {
			return
		}
		terminal := packet.CompletedCount + packet.FailedCount + packet.CanceledCount + packet.TimedOutCount
		finalization.CompletedObligations += terminal
		if remaining := packet.TaskCount - terminal; remaining > 0 {
			finalization.PendingObligations += remaining
		}
		if packet.Truncated {
			finalization.Truncated = true
		}
		for _, child := range packet.Children {
			addExecChildResult(finalization, seenChild, child)
		}
	}

	if record != nil {
		for _, batchID := range record.ObligationBatchIDs() {
			batch, err := store.GetBatch(ctx, strings.TrimSpace(batchID))
			if err != nil || batch == nil || !strings.EqualFold(strings.TrimSpace(batch.ParentSessionID), parentSessionID) {
				continue
			}
			addBatch(batch)
		}
		addExecAgentSessionResults(ctx, host, record, finalization, seenChild)
	}

	// The parked record can disappear after a successful settlement while the
	// current run is still unwinding. Fall back to the batches anchored to the
	// same parent turn so the timeout still reports their durable results.
	if len(finalization.ChildResults) == 0 && turnID != "" {
		batches, err := store.ListBatches(ctx, subagentbatch.BatchFilter{ParentSessionID: parentSessionID})
		if err == nil {
			for _, batch := range batches {
				if strings.TrimSpace(batch.ParentTurnID) != turnID {
					continue
				}
				addBatch(&batch)
			}
		}
	}

	return finalization
}

func execTurnSuspension(ctx context.Context, store subagentbatch.BatchStore, parentSessionID, turnID string) *subagentbatch.TurnSuspension {
	if store == nil || strings.TrimSpace(parentSessionID) == "" {
		return nil
	}
	if turnID = strings.TrimSpace(turnID); turnID != "" {
		record, ok, err := store.GetTurnSuspension(ctx, parentSessionID, turnID)
		if err == nil && ok {
			return record
		}
		return nil
	}
	records, err := store.ListTurnSuspensions(ctx, parentSessionID)
	if err != nil {
		return nil
	}
	for _, record := range records {
		if record != nil && record.HasChildResults() {
			return record
		}
	}
	return nil
}

func addExecAgentSessionResults(
	ctx context.Context,
	host *localChatRuntimeHost,
	record *subagentbatch.TurnSuspension,
	finalization *ExecFinalizationIncomplete,
	seenChild map[string]struct{},
) {
	if record == nil || finalization == nil {
		return
	}
	resolver := host.agentSessionObligationResolver()
	for _, obligationID := range record.ObligationIDs {
		childSessionID := subagentbatch.AgentSessionIDFromObligation(obligationID)
		if childSessionID == "" {
			continue
		}
		status := "unknown"
		terminal := false
		found := false
		if resolver != nil {
			terminal, found, _ = resolver.AgentSessionTerminal(ctx, childSessionID)
		}
		if found && terminal {
			status = "completed"
			finalization.CompletedObligations++
		} else {
			if found {
				status = "running"
			}
			finalization.PendingObligations++
		}
		addExecChildResult(finalization, seenChild, toolbroker.SynthesisChildResult{
			SessionID: childSessionID,
			Status:    status,
		})
	}
}

func addExecChildResult(
	finalization *ExecFinalizationIncomplete,
	seenChild map[string]struct{},
	child toolbroker.SynthesisChildResult,
) {
	if finalization == nil {
		return
	}
	key := strings.TrimSpace(child.TaskID) + "|" + strings.TrimSpace(child.SessionID)
	if key == "|" {
		return
	}
	if _, exists := seenChild[key]; exists {
		return
	}
	seenChild[key] = struct{}{}
	if len(finalization.ChildResults) >= toolbroker.SynthesisChildLimit {
		finalization.Truncated = true
		return
	}
	finalization.ChildResults = append(finalization.ChildResults, child)
}
