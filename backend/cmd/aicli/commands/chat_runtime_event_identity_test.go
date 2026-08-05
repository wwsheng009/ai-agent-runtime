package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestChatRuntimeEventBridge_TracksPrimarySessionAcrossRuntimeProjectionReplacement(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-old"}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	if !bridge.isPrimarySessionEvent(runtimeevents.Event{SessionID: "session-old"}) {
		t.Fatal("expected the initial runtime session to be primary")
	}

	// Restoring a session replaces the mutable RuntimeSession pointer. The
	// asynchronous bridge must route using its synchronized identity snapshot.
	session.RuntimeSession = &runtimechat.Session{ID: "session-new"}
	updateChatRuntimeEventBridgePrimarySession(session)

	if bridge.isPrimarySessionEvent(runtimeevents.Event{SessionID: "session-old"}) {
		t.Fatal("expected events for the replaced session to be rejected")
	}
	if !bridge.isPrimarySessionEvent(runtimeevents.Event{SessionID: "session-new"}) {
		t.Fatal("expected events for the restored session to be accepted")
	}
}
