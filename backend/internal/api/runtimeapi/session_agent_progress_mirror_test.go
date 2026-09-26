package runtimeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

func mirrorEventsOnBus(bus *runtimeevents.Bus, parentSessionID string) []runtimeevents.Event {
	if bus == nil {
		return nil
	}
	matched := make([]runtimeevents.Event, 0, 4)
	for _, event := range bus.Recent(64) {
		if event.Type != supervision.EventTypeSubagentProgress {
			continue
		}
		if event.SessionID != parentSessionID {
			continue
		}
		matched = append(matched, event)
	}
	return matched
}

// TestAPIAgentProgressMirrorThrottlesAndStaysLiveOnly covers P1-5 方案 2 on the
// API host: child tool progress is mirrored onto the parent session as a
// throttled live-only subagent.progress event. The mirror must merge repeated
// updates inside the window, keep state changes, ignore other sessions and never
// append a durable row.
func TestAPIAgentProgressMirrorThrottlesAndStaysLiveOnly(t *testing.T) {
	ctx := context.Background()
	handler, _, _ := newAPIWakeTestHandler(t, "api-progress-mirror")
	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionEventStore = runtimeStore

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	parent, err := sessionManager.Create(ctx, "user-progress-mirror")
	require.NoError(t, err)
	child := chat.NewSession(parent.UserID)
	child.ID = "child-progress-1"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	controller.subscribeAgentCompletion(parent.ID, child)

	bus.Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: child.ID,
		ToolName:  "bash",
		TraceID:   "trace-child-progress",
		Payload: map[string]interface{}{
			"tool_call_id": "call-child-1",
			"kind":         "progress",
			"message":      "step 1",
			"percent":      10.0,
		},
	})

	mirrors := mirrorEventsOnBus(bus, parent.ID)
	require.Len(t, mirrors, 1, "first child progress must be mirrored to the parent")
	mirror := mirrors[0]
	assert.Equal(t, supervision.EventTypeSubagentProgress, mirror.Type)
	assert.Equal(t, parent.ID, mirror.SessionID)
	assert.Equal(t, "trace-child-progress", mirror.TraceID)
	assert.Equal(t, child.ID, mirror.Payload["agent_id"])
	assert.Equal(t, child.ID, mirror.Payload["session_id"])
	assert.Equal(t, parent.ID, mirror.Payload["parent_session_id"])
	assert.Equal(t, "call-child-1", mirror.Payload["tool_call_id"])
	assert.Equal(t, "bash", mirror.Payload["tool_name"])
	assert.Equal(t, "progress", mirror.Payload["state"])
	assert.Equal(t, "step 1", mirror.Payload["message"])
	assert.Equal(t, 10.0, mirror.Payload["percent"])
	assert.Equal(t, true, mirror.Payload["live"])
	assert.Equal(t, toolprotocol.EventTypeProgress, mirror.Payload["source_event_type"])

	// Inside the throttle window the same (child, tool call, state) is merged.
	bus.Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: child.ID,
		ToolName:  "bash",
		Payload: map[string]interface{}{
			"tool_call_id": "call-child-1",
			"kind":         "progress",
			"message":      "step 2",
			"percent":      20.0,
		},
	})
	assert.Len(t, mirrorEventsOnBus(bus, parent.ID), 1, "window must merge repeated progress")

	// State changes bypass the window: a finishing background job stays visible.
	bus.Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: child.ID,
		ToolName:  "bash",
		Payload: map[string]interface{}{
			"tool_call_id": "call-child-1",
			"kind":         "background_complete",
			"message":      "job done",
		},
	})
	mirrors = mirrorEventsOnBus(bus, parent.ID)
	require.Len(t, mirrors, 2, "state change must be delivered immediately")
	assert.Equal(t, "background_complete", mirrors[1].Payload["state"])

	// A different child session must not leak into this parent's mirror.
	bus.Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "child-other",
		ToolName:  "bash",
		Payload: map[string]interface{}{
			"tool_call_id": "call-other",
			"kind":         "progress",
			"message":      "should-not-be-mirrored",
		},
	})
	assert.Len(t, mirrorEventsOnBus(bus, parent.ID), 2)

	// Live-only: the mirror never reaches the durable session store.
	stored, err := runtimeStore.ListEvents(ctx, parent.ID, 0, 0)
	require.NoError(t, err)
	for _, event := range stored {
		assert.NotEqual(t, supervision.EventTypeSubagentProgress, event.Type)
		assert.NotContains(t, event.Payload, "should-not-be-mirrored")
	}
	childStored, err := runtimeStore.ListEvents(ctx, child.ID, 0, 0)
	require.NoError(t, err)
	for _, event := range childStored {
		assert.NotEqual(t, supervision.EventTypeSubagentProgress, event.Type)
	}
}

// TestStreamSessionRuntimeEventsLiveForwardsSubagentProgressMirror verifies the
// mirror is delivered through the existing live (live=1) SSE channel, marked
// live, and stays out of the durable stream.
func TestStreamSessionRuntimeEventsLiveForwardsSubagentProgressMirror(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const parentSessionID = "session-runtime-stream-mirror"
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+parentSessionID+"/runtime/stream?live=1&poll_ms=5000", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": parentSessionID})
	rec := newSynchronizedResponseRecorder()

	done := make(chan struct{})
	go func() {
		handler.StreamSessionRuntimeEvents(rec, req)
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      supervision.EventTypeSubagentProgress,
		SessionID: parentSessionID,
		AgentName: "agent-controller",
		TraceID:   "trace-mirror",
		Payload: map[string]interface{}{
			"agent_id":     "child-mirror-1",
			"session_id":   "child-mirror-1",
			"tool_call_id": "call-mirror-1",
			"tool_name":    "bash",
			"state":        "progress",
			"message":      "mirrored step",
			"live":         true,
		},
	})
	// Another session's mirror must not leak into this stream.
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      supervision.EventTypeSubagentProgress,
		SessionID: "session-other-mirror",
		Payload: map[string]interface{}{
			"agent_id": "child-other",
			"message":  "mirror-should-not-appear",
		},
	})

	require.Eventually(t, func() bool {
		body := rec.BodyString()
		return strings.Contains(body, `"type":"subagent.progress"`) &&
			strings.Contains(body, `"mirrored step"`) &&
			strings.Contains(body, `"live":true`)
	}, 2*time.Second, 20*time.Millisecond)

	stored, err := runtimeStore.ListEvents(context.Background(), parentSessionID, 0, 0)
	require.NoError(t, err)
	for _, event := range stored {
		assert.NotEqual(t, supervision.EventTypeSubagentProgress, event.Type)
	}

	body := rec.BodyString()
	assert.Contains(t, body, "event: runtime_event")
	assert.Contains(t, body, `"tool_call_id":"call-mirror-1"`)
	assert.NotContains(t, body, "mirror-should-not-appear")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}
}
