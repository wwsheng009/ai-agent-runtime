package eventbridge

import (
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

type recordingPublisher struct {
	events []runtimeevents.Event
}

func (p *recordingPublisher) Publish(event runtimeevents.Event) {
	p.events = append(p.events, event)
}

func TestObserverProjectsRequestWithSession(t *testing.T) {
	pub := &recordingPublisher{}
	observer := Observer(pub, Options{FallbackSessionID: "fallback"})
	if observer == nil {
		t.Fatal("observer must not be nil for non-nil publisher")
	}
	observer(runtimelsp.Event{
		Kind:           runtimelsp.EventRequest,
		Time:           time.Now().UTC(),
		SessionID:      "s-1",
		Trigger:        "inline",
		Outcome:        "injected",
		DurationMS:     12,
		DiagCount:      3,
		AppendedBytes:  42,
		OmittedItems:   1,
		OmittedByChars: 2,
		Server:         "gopls",
		Path:           "/tmp/secret.go", // 路径不得进入载荷
	})
	if len(pub.events) != 1 {
		t.Fatalf("published = %d, want 1", len(pub.events))
	}
	event := pub.events[0]
	if event.Type != runtimeevents.EventLSPRequestFinished || event.SessionID != "s-1" {
		t.Fatalf("event = %#v, want lsp.request.finished with session s-1", event)
	}
	payload := event.Payload
	if payload["trigger"] != "inline" || payload["outcome"] != "injected" {
		t.Fatalf("payload trigger/outcome = %v/%v", payload["trigger"], payload["outcome"])
	}
	if payload["duration_ms"] != int64(12) || payload["diag_count"] != 3 || payload["appended_bytes"] != 42 {
		t.Fatalf("payload scalars = %#v", payload)
	}
	if payload["server"] != "gopls" {
		t.Fatalf("payload server = %v", payload["server"])
	}
	if _, hasPath := payload["path"]; hasPath {
		t.Fatalf("path 不得进入载荷: %#v", payload)
	}
}

func TestObserverFallbackLifecycleAndNilBus(t *testing.T) {
	if Observer(nil, Options{}) != nil {
		t.Fatal("nil publisher must yield nil observer")
	}

	pub := &recordingPublisher{}
	observer := Observer(pub, Options{FallbackSessionID: "s-fallback"})
	observer(runtimelsp.Event{Kind: runtimelsp.EventRequest, Outcome: "clean"})
	observer(runtimelsp.Event{
		Kind:   runtimelsp.EventServerState,
		Status: runtimelsp.ServerStatus{Name: "gopls", State: runtimelsp.StateReady, PID: 100},
	})
	observer(runtimelsp.Event{Kind: runtimelsp.EventDiagnostics, Server: "gopls", Count: 2})

	if len(pub.events) != 3 {
		t.Fatalf("published = %d, want 3", len(pub.events))
	}
	if pub.events[0].SessionID != "s-fallback" || pub.events[0].Type != runtimeevents.EventLSPRequestFinished {
		t.Fatalf("fallback session not applied: %#v", pub.events[0])
	}
	state := pub.events[1]
	if state.Type != runtimeevents.EventLSPServerState || state.SessionID != "s-fallback" {
		t.Fatalf("state event = %#v", state)
	}
	if state.Payload["server"] != "gopls" || state.Payload["state"] != string(runtimelsp.StateReady) {
		t.Fatalf("state payload = %#v", state.Payload)
	}
	diag := pub.events[2]
	if diag.Type != runtimeevents.EventLSPDiagnosticsUpdated || diag.Payload["count"] != 2 {
		t.Fatalf("diagnostics event = %#v", diag)
	}
}
