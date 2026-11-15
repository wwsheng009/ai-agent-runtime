package runtimehost

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestResolveEventPersistSettingsDefaultsToBatchingAndAsyncDispatch(t *testing.T) {
	settings := ResolveEventPersistSettings(runtimecfg.EventPersistConfig{})
	if !settings.Enabled {
		t.Fatal("zero config must enable batching by default")
	}
	if !settings.AsyncDispatch {
		t.Fatal("zero config must enable async dispatch by default")
	}
	if settings.CriticalTypes == nil {
		t.Fatal("critical event classifier must be wired")
	}
	if settings.OverflowDropTypes == nil {
		t.Fatal("stream delta overflow classifier must be wired")
	}
}

func TestResolveEventPersistSettingsDisableIsAuthoritative(t *testing.T) {
	settings := ResolveEventPersistSettings(runtimecfg.EventPersistConfig{
		Disable:         true,
		BatchingEnabled: true,
		AsyncDispatch:   true,
	})
	if settings.Enabled {
		t.Fatal("disable=true must override batchingEnabled")
	}
	if settings.AsyncDispatch {
		t.Fatal("disable=true must override asyncDispatch")
	}
}

func TestResolveEventPersistSettingsPreservesTuningAndClassifiers(t *testing.T) {
	settings := ResolveEventPersistSettings(runtimecfg.EventPersistConfig{
		BatchSize:       64,
		QueueLimit:      128,
		QueueBytesLimit: 4096,
		FailMode:        "drop",
	})
	if settings.BatchSize != 64 || settings.QueueLimit != 128 || settings.QueueBytesLimit != 4096 {
		t.Fatalf("tuning not preserved: %+v", settings)
	}
	if settings.FailMode != "drop" {
		t.Fatalf("fail mode not preserved: %q", settings.FailMode)
	}
	if !settings.CriticalTypes(chat.EventApprovalRequested) {
		t.Fatal("approval request must be classified as critical")
	}
	if !settings.OverflowDropTypes(chat.EventAssistantDelta) {
		t.Fatal("assistant delta must be classified as overflow-droppable")
	}
}

func TestPrepareSessionStoreEventSharedAdmissionAndAlias(t *testing.T) {
	tests := []struct {
		name        string
		event       runtimeevents.Event
		wantPersist bool
		wantType    string
	}{
		{
			name:        "empty session is dropped before type admission",
			event:       runtimeevents.Event{Type: "tool.requested"},
			wantPersist: false,
			wantType:    "tool_started",
		},
		{
			name: "producer-persisted event is skipped",
			event: runtimeevents.Event{
				Type:      "tool.requested",
				SessionID: "s1",
				Payload:   map[string]interface{}{"seq": int64(7)},
			},
			wantPersist: false,
			wantType:    "tool_started",
		},
		{
			name:        "live-only event is not written to the session store",
			event:       runtimeevents.Event{Type: "tool.progress", SessionID: "s1"},
			wantPersist: false,
			wantType:    "tool.progress",
		},
		{
			name:        "persisted event is admitted and aliased",
			event:       runtimeevents.Event{Type: "tool.requested", SessionID: "s1"},
			wantPersist: true,
			wantType:    "tool_started",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped, persist := PrepareSessionStoreEvent(tt.event)
			if persist != tt.wantPersist {
				t.Fatalf("persist = %v, want %v", persist, tt.wantPersist)
			}
			if mapped.Type != tt.wantType {
				t.Fatalf("mapped type = %q, want %q", mapped.Type, tt.wantType)
			}
		})
	}
}
