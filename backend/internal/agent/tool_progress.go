package agent

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// withToolProgressReporter binds a toolprotocol.Reporter that publishes
// tool.progress runtime events for the given tool call.
//
// Tools opt in via toolprotocol.Report(ctx, Progress{...}); no Execute signature
// changes are required. Progress is live-only (not persisted to session store).
func (a *Agent) withToolProgressReporter(ctx context.Context, sessionID, traceID string, call types.ToolCall) context.Context {
	if a == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	toolName := strings.TrimSpace(call.Name)
	callID := strings.TrimSpace(call.ID)
	sessionID = strings.TrimSpace(sessionID)
	traceID = strings.TrimSpace(traceID)
	return toolprotocol.WithReporter(ctx, toolprotocol.ReporterFunc(func(progress toolprotocol.Progress) {
		if a == nil {
			return
		}
		if progress.ToolID == "" {
			progress.ToolID = toolprotocol.ToolID(toolName)
		}
		if progress.CallID == "" {
			progress.CallID = toolprotocol.NormalizeCallID(callID)
		}
		if progress.SessionID == "" {
			progress.SessionID = sessionID
		}
		if progress.TraceID == "" {
			progress.TraceID = traceID
		}
		if progress.Timestamp.IsZero() {
			progress.Timestamp = time.Now().UTC()
		}
		payload := progress.Payload()
		emitName := strings.TrimSpace(string(progress.ToolID))
		if emitName == "" {
			emitName = toolName
		}
		a.emitRuntimeEvent(toolprotocol.EventTypeProgress, sessionID, emitName, payload)
	}))
}
