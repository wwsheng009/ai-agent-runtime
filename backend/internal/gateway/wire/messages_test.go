package wire

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewJSONFrame(t *testing.T) {
	f, err := NewJSONFrame(TypeHello, 0, Hello{ProtocolVersion: ProtocolVersion, AgentVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("NewJSONFrame: %v", err)
	}
	if f.Version != ProtocolVersion {
		t.Fatalf("version = %d, want %d", f.Version, ProtocolVersion)
	}
	if !f.IsJSON() {
		t.Fatal("JSON flag must be set")
	}
	if f.StreamID != 0 {
		t.Fatalf("control frames must use stream 0, got %d", f.StreamID)
	}
	got, err := DecodePayload[Hello](f)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if got.ProtocolVersion != ProtocolVersion || got.AgentVersion != "1.0.0" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDecodePayloadRoundTripRegister(t *testing.T) {
	in := Register{
		NodeID:       "node-aicli-9f3a",
		Kind:         "aicli",
		Name:         "vince-laptop · aicli",
		DeviceName:   "vince-laptop",
		Capabilities: []string{"manifest", "events", "invoke"},
		Workspace:    &WorkspaceInfo{Path: "E:/proj", Name: "proj"},
		Routes:       []string{"/web", "/web/api"},
		Sessions: []SessionSummary{
			{SessionID: "session_1", Title: "网关设计", State: "busy", Attention: "approval", Busy: true, UpdatedAt: time.Unix(1, 0).UTC()},
		},
	}
	f, err := NewJSONFrame(TypeRegister, 0, in)
	if err != nil {
		t.Fatalf("NewJSONFrame: %v", err)
	}
	got, err := DecodePayload[Register](f)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if got.NodeID != in.NodeID || got.Kind != in.Kind || got.DeviceName != in.DeviceName {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "session_1" || got.Sessions[0].Attention != "approval" {
		t.Fatalf("sessions mismatch: %+v", got.Sessions)
	}
}

func TestDecodePayloadRoundTripRegisterAck(t *testing.T) {
	in := RegisterAck{
		NodeID:         "node-aicli-9f3a",
		ConsoleURL:     "https://gw/n/node-aicli-9f3a/web/",
		AcceptedRoutes: []string{"/web"},
	}
	f, err := NewJSONFrame(TypeRegisterAck, 0, in)
	if err != nil {
		t.Fatalf("NewJSONFrame: %v", err)
	}
	got, err := DecodePayload[RegisterAck](f)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if got.NodeID != in.NodeID || got.ConsoleURL != in.ConsoleURL || len(got.AcceptedRoutes) != 1 {
		t.Fatalf("ack mismatch: %+v", got)
	}
}

func TestDecodePayloadNonJSON(t *testing.T) {
	f := Frame{Type: TypeStreamData, Payload: []byte("raw")}
	if _, err := DecodePayload[Hello](f); err == nil {
		t.Fatal("expected error decoding a non-JSON frame")
	}
}

func TestRegisterJSONKeys(t *testing.T) {
	raw, err := json.Marshal(Register{
		NodeID:       "n1",
		Kind:         "aicli",
		DeviceName:   "laptop",
		Capabilities: []string{"invoke"},
		Routes:       []string{"/web"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"node_id", "kind", "device_name", "capabilities", "routes"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("Register JSON missing key %q: %s", key, raw)
		}
	}
	// The secret must never be part of the registration payload.
	for _, forbidden := range []string{"token", "password", "secret"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatalf("Register JSON must not contain %q: %s", forbidden, raw)
		}
	}
}

func TestSessionUpdateOpConstants(t *testing.T) {
	for _, op := range []string{SessionOpUpsert, SessionOpRemove, SessionOpReconcile} {
		if op == "" {
			t.Fatal("session update op constant must not be empty")
		}
	}
}
