package supervision

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

func progressSourceEvent(kind, callID, message string, percent float64) runtimeevents.Event {
	payload := map[string]interface{}{
		"tool_call_id": callID,
		"kind":         kind,
	}
	if message != "" {
		payload["message"] = message
	}
	if percent > 0 {
		payload["percent"] = percent
	}
	return runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "child-1",
		ToolName:  "bash",
		TraceID:   "trace-child-1",
		Timestamp: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
		Payload:   payload,
	}
}

func TestSubagentProgressMirrorThrottlesWindowAndKeepsStateChanges(t *testing.T) {
	mirror := NewSubagentProgressMirror(2 * time.Second)
	target := SubagentProgressTarget{
		ParentSessionID: "parent-1",
		ChildSessionID:  "child-1",
		Path:            "/root/child-1",
		Depth:           1,
		AgentType:       "researcher",
	}
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

	first, ok := mirror.Observe(target, progressSourceEvent("progress", "call-1", "step 1", 10), base)
	require.True(t, ok)

	// Inside the window with the same state: merged, not emitted.
	_, ok = mirror.Observe(target, progressSourceEvent("progress", "call-1", "step 2", 20), base.Add(500*time.Millisecond))
	require.False(t, ok)

	// State change bypasses the window (a tool finishing must not be swallowed).
	backgroundDone, ok := mirror.Observe(target, progressSourceEvent("background_complete", "call-1", "job done", 80), base.Add(1500*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, "background_complete", backgroundDone.Payload["state"])

	// Returning to progress is another state change -> emitted.
	_, ok = mirror.Observe(target, progressSourceEvent("progress", "call-1", "step 3", 85), base.Add(1600*time.Millisecond))
	require.True(t, ok)

	// Same state, window anchored on the last emitted stamp: still throttled.
	_, ok = mirror.Observe(target, progressSourceEvent("progress", "call-1", "step 4", 90), base.Add(2*time.Second))
	require.False(t, ok)

	// Window elapsed without a state change: emitted again.
	afterWindow, ok := mirror.Observe(target, progressSourceEvent("progress", "call-1", "step 5", 95), base.Add(3600*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, "step 5", afterWindow.Payload["message"])

	assert.Equal(t, 1, mirror.Pending())
	assert.Equal(t, "parent-1", first.SessionID)
	assert.Equal(t, EventTypeSubagentProgress, first.Type)
}

func TestSubagentProgressMirrorPayloadShapeAndSourceIsolation(t *testing.T) {
	mirror := NewSubagentProgressMirror(0) // non-positive window falls back to the default
	assert.Equal(t, DefaultSubagentProgressWindow, mirror.window)

	source := progressSourceEvent("progress", "call-7", "building", 42)
	source.Payload["partial"] = "compiling main.go"
	source.Payload["metadata"] = map[string]interface{}{
		"step":      3,
		"tool_name": "ignored-because-present",
	}
	target := SubagentProgressTarget{
		ParentSessionID: "parent-9",
		ChildSessionID:  "child-9",
		Path:            "/root/child-9",
		Depth:           2,
		AgentType:       "coder",
	}

	mirrored, ok := mirror.Observe(target, source, time.Date(2026, 9, 13, 11, 0, 0, 0, time.UTC))
	require.True(t, ok)
	assert.Equal(t, EventTypeSubagentProgress, mirrored.Type)
	assert.Equal(t, "parent-9", mirrored.SessionID)
	assert.Equal(t, "trace-child-1", mirrored.TraceID)
	assert.Equal(t, "agent-controller", mirrored.AgentName)
	assert.Equal(t, map[string]interface{}{
		"agent_id":               "child-9",
		"session_id":             "child-9",
		"parent_session_id":      "parent-9",
		"source_event_type":      toolprotocol.EventTypeProgress,
		"state":                  "progress",
		"live":                   true,
		"tool_call_id":           "call-7",
		"tool_name":              "bash",
		"message":                "building",
		"partial":                "compiling main.go",
		"percent":                float64(42),
		"source_event_timestamp": "2026-09-13T10:00:00Z",
		"path":                   "/root/child-9",
		"depth":                  2,
		"agent_type":             "coder",
		"role":                   "coder",
		"step":                   3,
		"mirror_window_ms":       DefaultSubagentProgressWindow.Milliseconds(),
	}, mirrored.Payload)

	// The mirror must never mutate the bus-retained source payload.
	assert.NotContains(t, source.Payload, "parent_session_id")
	assert.NotContains(t, source.Payload, "mirror_window_ms")
	assert.NotContains(t, source.Payload, "tool_name", "metadata merge must not rewrite the source")
	assert.Equal(t, "ignored-because-present", source.Payload["metadata"].(map[string]interface{})["tool_name"])
}

func TestSubagentProgressMirrorRejectsUnmirrorableEvents(t *testing.T) {
	mirror := NewSubagentProgressMirror(time.Second)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	valid := SubagentProgressTarget{ParentSessionID: "parent-1", ChildSessionID: "child-1"}

	var nilMirror *SubagentProgressMirror
	_, ok := nilMirror.Observe(valid, progressSourceEvent("progress", "call-1", "x", 0), now)
	assert.False(t, ok)
	nilMirror.Forget("child-1")
	assert.Zero(t, nilMirror.Pending())

	cases := []struct {
		name   string
		target SubagentProgressTarget
		event  runtimeevents.Event
	}{
		{
			name:   "missing parent session",
			target: SubagentProgressTarget{ChildSessionID: "child-1"},
			event:  progressSourceEvent("progress", "call-1", "x", 0),
		},
		{
			name:   "missing child session",
			target: SubagentProgressTarget{ParentSessionID: "parent-1"},
			event:  progressSourceEvent("progress", "call-1", "x", 0),
		},
		{
			name:   "non progress event",
			target: valid,
			event:  runtimeevents.Event{Type: "tool.started", SessionID: "child-1", ToolName: "bash"},
		},
		{
			name:   "no tool identity",
			target: valid,
			event: runtimeevents.Event{
				Type:      toolprotocol.EventTypeProgress,
				SessionID: "child-1",
				Payload:   map[string]interface{}{"kind": "progress"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, ok := mirror.Observe(testCase.target, testCase.event, now)
			assert.False(t, ok)
		})
	}
	assert.Zero(t, mirror.Pending())
}

func TestSubagentProgressMirrorToolNameFallbackAndForget(t *testing.T) {
	mirror := NewSubagentProgressMirror(time.Second)
	now := time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)
	target := SubagentProgressTarget{ParentSessionID: "parent-1", ChildSessionID: "child-1"}

	// No tool_call_id: the tool name keeps the throttle identity stable.
	event := runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: "child-1",
		ToolName:  "shell",
		Payload:   map[string]interface{}{"kind": "progress", "message": "tick"},
	}
	_, ok := mirror.Observe(target, event, now)
	require.True(t, ok)
	_, ok = mirror.Observe(target, event, now.Add(100*time.Millisecond))
	require.False(t, ok)
	_, ok = mirror.Observe(target, event, now.Add(1200*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, 1, mirror.Pending())

	mirror.Forget("child-1")
	assert.Zero(t, mirror.Pending())

	// Forget for another child / empty id never clears unrelated stamps.
	mirror.Forget("")
	mirror.Forget("child-other")
	_, ok = mirror.Observe(target, event, now.Add(1300*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, 1, mirror.Pending())
}

func TestSubagentProgressMirrorBoundsAndStalePruning(t *testing.T) {
	mirror := NewSubagentProgressMirror(time.Second)
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC)

	for index := 0; index < subagentProgressMaxEntries+64; index++ {
		target := SubagentProgressTarget{ParentSessionID: "parent-1", ChildSessionID: childIDForIndex(index)}
		_, ok := mirror.Observe(target, progressSourceEvent("progress", "call", "tick", 0), now.Add(time.Duration(index)*time.Millisecond))
		require.True(t, ok)
	}
	assert.Equal(t, subagentProgressMaxEntries, mirror.Pending(), "oldest stamps are evicted past the bound")

	// Idle stamps older than staleFactor windows are dropped by the next prune.
	later := now.Add(time.Duration(subagentProgressMaxEntries+64)*time.Millisecond + subagentProgressStaleFactor*2*time.Second)
	_, ok := mirror.Observe(
		SubagentProgressTarget{ParentSessionID: "parent-1", ChildSessionID: "child-new"},
		progressSourceEvent("progress", "call", "tick", 0),
		later,
	)
	require.True(t, ok)
	assert.Equal(t, 1, mirror.Pending())
}

func TestSubagentProgressMirrorIsConcurrencySafe(t *testing.T) {
	mirror := NewSubagentProgressMirror(50 * time.Millisecond)
	target := SubagentProgressTarget{ParentSessionID: "parent-1", ChildSessionID: "child-1"}
	base := time.Date(2026, 9, 13, 15, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var mu sync.Mutex
	emitted := 0
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for step := 0; step < 32; step++ {
				at := base.Add(time.Duration(step*worker) * time.Millisecond)
				if _, ok := mirror.Observe(target, progressSourceEvent("progress", "call-1", "tick", float64(step%100)), at); ok {
					mu.Lock()
					emitted++
					mu.Unlock()
				}
			}
		}(worker)
	}
	wg.Wait()
	assert.Greater(t, emitted, 0)
	assert.Equal(t, 1, mirror.Pending())
}

func childIDForIndex(index int) string {
	return "child-" + strconv.Itoa(index)
}
