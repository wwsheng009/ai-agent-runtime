package agent

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// BatchTerminalLifecycle is the host-neutral terminal projection emitted by a
// spawn_subagents batch.  The agent package deliberately does not depend on
// supervision: hosts can translate this record into their own durable
// lifecycle/notification protocol.
type BatchTerminalLifecycle struct {
	BatchID          string
	RootScopeID      string
	ParentSessionID  string
	ParentTurnID     string
	ParentToolCallID string
	TraceID          string
	ExecutionMode    subagentbatch.ExecutionMode
	Status           subagentbatch.BatchStatus
	EventType        string
	SubjectVersion   int64
	TaskCount        int
	CompletedCount   int
	FailedCount      int
	CanceledCount    int
	TimedOutCount    int
	ErrorClass       string
	Error            string
}

// BatchLifecycleProjector persists a terminal batch transition in a host's
// lifecycle control plane. It is best-effort from the agent runtime's point of
// view: a projection error must not change the already durable child/batch
// result or prevent compatibility terminal delivery.
type BatchLifecycleProjector func(context.Context, BatchTerminalLifecycle) error

func (a *Agent) projectBatchLifecycle(ctx context.Context, event BatchTerminalLifecycle) error {
	projector := a.batchLifecycleProjectorSnapshot()
	if projector == nil {
		return nil
	}
	return projector(ctx, event.normalized())
}

func (e BatchTerminalLifecycle) normalized() BatchTerminalLifecycle {
	e.BatchID = strings.TrimSpace(e.BatchID)
	e.RootScopeID = strings.TrimSpace(e.RootScopeID)
	e.ParentSessionID = strings.TrimSpace(e.ParentSessionID)
	if e.RootScopeID == "" {
		e.RootScopeID = e.ParentSessionID
	}
	e.ParentTurnID = strings.TrimSpace(e.ParentTurnID)
	e.ParentToolCallID = strings.TrimSpace(e.ParentToolCallID)
	e.TraceID = strings.TrimSpace(e.TraceID)
	e.EventType = strings.TrimSpace(e.EventType)
	if e.EventType == "" && e.Status.Terminal() {
		e.EventType = batchTerminalEventType(e.Status)
	}
	if e.SubjectVersion <= 0 {
		e.SubjectVersion = 1
	}
	return e
}
