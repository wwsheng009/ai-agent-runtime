package schedulehost_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

func TestNewPeerDelivererRequiresCaller(t *testing.T) {
	if _, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{}); err == nil {
		t.Fatal("missing caller node id must fail (fail-closed)")
	}
}

func TestPeerDelivererSendsEnvelopeArgs(t *testing.T) {
	var gotTarget string
	var gotArgs map[string]any
	d, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID: "node-caller",
		ResolveTarget: func(sessionID string) (string, error) {
			if sessionID != "session_x" {
				t.Fatalf("resolve session = %q, want session_x", sessionID)
			}
			return "node-target", nil
		},
		Call: func(_ context.Context, callerID, targetNodeID string, args json.RawMessage) *mesh.CallResult {
			if callerID != "node-caller" {
				t.Fatalf("caller = %q, want node-caller", callerID)
			}
			gotTarget = targetNodeID
			_ = json.Unmarshal(args, &gotArgs)
			return &mesh.CallResult{Status: mesh.CallStatusOK}
		},
	})
	if err != nil {
		t.Fatalf("NewPeerDeliverer: %v", err)
	}
	err = d.DeliverEnvelope(context.Background(), schedule.DeliveryEnvelope{
		SessionID:   "session_x",
		Text:        "hello",
		Occurrences: []string{"k1"},
	})
	if err != nil {
		t.Fatalf("DeliverEnvelope: %v", err)
	}
	if gotTarget != "node-target" {
		t.Fatalf("target = %q, want node-target", gotTarget)
	}
	if gotArgs["session_id"] != "session_x" || gotArgs["text"] != "hello" {
		t.Fatalf("args = %+v", gotArgs)
	}
	occurrences, ok := gotArgs["occurrences"].([]any)
	if !ok || len(occurrences) != 1 || occurrences[0] != "k1" {
		t.Fatalf("occurrences = %#v, want [k1]", gotArgs["occurrences"])
	}
}

func TestPeerDelivererRefusesSelfTarget(t *testing.T) {
	d, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID:  "node-caller",
		ResolveTarget: func(string) (string, error) { return "node-caller", nil },
	})
	if err != nil {
		t.Fatalf("NewPeerDeliverer: %v", err)
	}
	err = d.DeliverEnvelope(context.Background(), schedule.DeliveryEnvelope{SessionID: "s", Text: "t"})
	if err == nil || !strings.Contains(err.Error(), "self") {
		t.Fatalf("self target must be refused, got %v", err)
	}
}

func TestPeerDelivererSurfacesResolveAndCallFailures(t *testing.T) {
	resolveErr := errors.New("no live owner")
	d, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID:  "node-caller",
		ResolveTarget: func(string) (string, error) { return "", resolveErr },
	})
	if err != nil {
		t.Fatalf("NewPeerDeliverer: %v", err)
	}
	if err := d.DeliverEnvelope(context.Background(), schedule.DeliveryEnvelope{SessionID: "s", Text: "t"}); err == nil {
		t.Fatal("resolve failure must surface (caller degrades to next stage)")
	}

	d2, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID:  "node-caller",
		ResolveTarget: func(string) (string, error) { return "node-target", nil },
		Call: func(context.Context, string, string, json.RawMessage) *mesh.CallResult {
			return &mesh.CallResult{
				Status:  mesh.CallStatusRefused,
				Code:    mesh.CallCodeWriteNotAllowed,
				Message: "write not allowed",
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPeerDeliverer: %v", err)
	}
	err = d2.DeliverEnvelope(context.Background(), schedule.DeliveryEnvelope{SessionID: "s", Text: "t"})
	if err == nil || !strings.Contains(err.Error(), mesh.CallCodeWriteNotAllowed) {
		t.Fatalf("call failure must carry code, got %v", err)
	}
}
