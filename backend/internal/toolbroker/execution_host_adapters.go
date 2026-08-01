package toolbroker

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// CompletionDispatchFunc adapts a host mailbox append closure to the
// supervision.CompletionDispatcher interface. Hosts wire it in one line, for
// example:
//
//	supervisor.Dispatcher = CompletionDispatchFunc(func(ctx context.Context, entry supervision.CompletionOutboxEntry) (int64, error) {
//		message := BuildSubagentCompletionMailboxMessage(entry.ParentSessionID, entry.SessionID, "", "", "completion_outbox", outboxPayloadMap(entry))
//		_, seq, err := store.AppendAgentControlMailbox(ctx, entry.ParentSessionID, message)
//		return seq, err
//	})
type CompletionDispatchFunc func(ctx context.Context, entry supervision.CompletionOutboxEntry) (int64, error)

// DispatchCompletion implements supervision.CompletionDispatcher.
func (f CompletionDispatchFunc) DispatchCompletion(ctx context.Context, entry supervision.CompletionOutboxEntry) (int64, error) {
	if f == nil {
		return 0, nil
	}
	return f(ctx, entry)
}

// AgentSessionRunInterrupter adapts the child agent session controller to the
// supervision.RunInterrupter contract. InterruptRun closes the child session,
// which is the host's canonical way to stop a live child run (doc 5.5
// interrupt; no-op when no live actor exists).
type AgentSessionRunInterrupter struct {
	Controller AgentSessionController
}

// InterruptRun implements supervision.RunInterrupter.
func (i AgentSessionRunInterrupter) InterruptRun(ctx context.Context, run supervision.ExecutionRun) error {
	if i.Controller == nil {
		return nil
	}
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(run.AgentID)
	}
	if sessionID == "" {
		return nil
	}
	_, err := i.Controller.Close(ctx, sessionID)
	return err
}
