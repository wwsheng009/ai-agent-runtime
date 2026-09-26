package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// TestAPIHostProgressMirrorEnrichesDurableProgressSource covers the API half of
// P0-1c/M7 parity: the per-host live-only mirror must be shared between the
// child progress subscription and the durable progress projection's Messages
// hook, so `supervision_descendants` / preflight digests render the same
// last_message the CLI host does. The mirror itself is read-only enrichment and
// must never inject a durable event.
func TestAPIHostProgressMirrorEnrichesDurableProgressSource(t *testing.T) {
	ctx := context.Background()
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-progress-mirror-source")
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)
	seedAPIRunningBatch(t, store, "batch_mirror_api", "sess_mirror_api")

	mirror := handler.subagentProgressMirror()
	require.NotNil(t, mirror, "the host must expose one shared mirror")
	require.Same(t, mirror, handler.subagentProgressMirror(), "the mirror is per host, never rebuilt per call")

	// The exact shape a child tool.progress event produces (see
	// session_runtime_support.go): state + human message for the running child.
	_, ok := mirror.Observe(supervision.SubagentProgressTarget{
		ParentSessionID: "sess_mirror_api",
		ChildSessionID:  "child-restart-1",
	}, runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		TraceID:   "trace-mirror-source",
		SessionID: "child-restart-1",
		ToolName:  "bash",
		Timestamp: time.Now().UTC(),
		Payload: map[string]interface{}{
			"tool_call_id": "call-mirror-1",
			"kind":         "progress",
			"message":      "compiling backend",
		},
	}, time.Now().UTC())
	require.True(t, ok, "the first observation must be mirrored")

	source := handler.supervisionProgressSource()
	require.NotNil(t, source)
	groups, err := source.ListProgress(ctx, supervision.ProgressRequest{
		RootScopeID:     "sess_mirror_api",
		ParentSessionID: "sess_mirror_api",
	})
	require.NoError(t, err)
	require.NotEmpty(t, groups)

	var running supervision.ProgressTask
	found := false
	for _, group := range groups {
		for _, task := range group.RunningTasks {
			if task.ChildSessionID == "child-restart-1" {
				running = task
				found = true
			}
		}
	}
	require.True(t, found, "the durable projection must keep the running child row")
	require.Equal(t, "compiling backend", running.LastMessage,
		"the host mirror must enrich RunningTasks.LastMessage on the API host too")

	// Enrichment is live-only: no durable subagent.progress row may appear.
	if eventStore := handler.getSessionEventStore(); eventStore != nil {
		events, listErr := eventStore.ListEvents(ctx, "sess_mirror_api", 0, 128)
		require.NoError(t, listErr)
		for _, event := range events {
			require.NotEqual(t, supervision.EventTypeSubagentProgress, event.Type,
				"live-only progress mirrors must never reach the durable event store")
		}
	}
}
