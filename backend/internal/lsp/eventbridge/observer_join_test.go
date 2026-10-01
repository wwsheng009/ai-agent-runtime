package eventbridge

import (
	"testing"
	"time"

	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// TestObserverProjectsJoinKeysFingerprintsAndReasonCategory pins the §3.1 join
// contract: request events carry tool_call_id/turn_id and only fingerprints
// (never the raw path); lifecycle events carry a low-sensitivity reason
// category instead of free-form stderr text.
func TestObserverProjectsJoinKeysFingerprintsAndReasonCategory(t *testing.T) {
	pub := &recordingPublisher{}
	observer := Observer(pub, Options{})
	observer(runtimelsp.Event{
		Kind: runtimelsp.EventRequest, Time: time.Now().UTC(),
		Trigger: "inline", Outcome: "injected",
		ToolCallID: "call-1", TurnID: "turn-1",
		Path: "/tmp/secret.go", PathFingerprint: "fp-path", DiagFingerprint: "fp-diag",
		ReasonCategory: "wait_timeout",
		AppendedDiagBytes: 120, AppendedNoteBytes: 30,
		ColdFastFail: true, AttemptedMembers: 2,
	})
	observer(runtimelsp.Event{
		Kind: runtimelsp.EventServerState,
		Status: runtimelsp.ServerStatus{
			Name:   "pyright",
			State:  runtimelsp.StateUnavailable,
			Reason: `lsp: start pyright: executable "pyright-langserver" not found`,
		},
	})
	observer(runtimelsp.Event{
		Kind: runtimelsp.EventDiagnostics, Server: "gopls", Count: 2,
		Path: "/tmp/secret.go", PathFingerprint: "fp-path", DiagFingerprint: "fp-diag",
		Version: 7, HasVersion: true,
	})
	if len(pub.events) != 3 {
		t.Fatalf("published = %d, want 3", len(pub.events))
	}

	request := pub.events[0].Payload
	if request["tool_call_id"] != "call-1" || request["turn_id"] != "turn-1" {
		t.Fatalf("request join keys = %#v", request)
	}
	if request["path_fingerprint"] != "fp-path" || request["diag_fingerprint"] != "fp-diag" {
		t.Fatalf("request fingerprints = %#v", request)
	}
	if _, hasPath := request["path"]; hasPath {
		t.Fatalf("raw path must not enter the payload: %#v", request)
	}
	if request["reason_category"] != "wait_timeout" {
		t.Fatalf("request reason_category = %v, want wait_timeout", request["reason_category"])
	}
	if request["appended_diag_bytes"] != 120 || request["appended_note_bytes"] != 30 {
		t.Fatalf("append breakdown = %#v", request)
	}
	if _, hasEmpty := request["appended_empty_bytes"]; hasEmpty {
		t.Fatalf("zero append breakdown must be omitted: %#v", request)
	}
	if request["cold_fast_fail"] != true || request["attempted_members"] != 2 {
		t.Fatalf("perf attribution = %#v", request)
	}

	state := pub.events[1].Payload
	if state["reason_category"] != "binary_missing" {
		t.Fatalf("state reason_category = %v, want binary_missing", state["reason_category"])
	}
	if _, hasReason := state["reason"]; hasReason {
		t.Fatalf("raw reason must not enter the payload: %#v", state)
	}

	diagnostics := pub.events[2].Payload
	if diagnostics["path_fingerprint"] != "fp-path" || diagnostics["version"] != 7 || diagnostics["has_version"] != true {
		t.Fatalf("diagnostics payload = %#v", diagnostics)
	}
	if _, hasPath := diagnostics["path"]; hasPath {
		t.Fatalf("raw path must not enter the payload: %#v", diagnostics)
	}
}

// TestObserverComputesPathFingerprintFallback pins that a request carrying only
// a raw path still gets a stable fingerprint (and never the path itself).
func TestObserverComputesPathFingerprintFallback(t *testing.T) {
	pub := &recordingPublisher{}
	observer := Observer(pub, Options{})
	observer(runtimelsp.Event{
		Kind: runtimelsp.EventRequest, Trigger: "inline", Outcome: "clean",
		Path: "/tmp/another.go",
	})
	if len(pub.events) != 1 {
		t.Fatalf("published = %d, want 1", len(pub.events))
	}
	payload := pub.events[0].Payload
	fingerprint, _ := payload["path_fingerprint"].(string)
	if fingerprint == "" || fingerprint == "/tmp/another.go" {
		t.Fatalf("expected a non-reversible fingerprint, got %#v", payload)
	}
}
