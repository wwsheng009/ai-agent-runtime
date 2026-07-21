package goal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type goalCapabilityTestSession struct {
	session *runtimechat.Session
	storage runtimechat.SessionStorage
}

func (s *goalCapabilityTestSession) SessionID() string {
	if s == nil || s.session == nil {
		return ""
	}
	return s.session.ID
}

func (s *goalCapabilityTestSession) RuntimeSession() *runtimechat.Session {
	if s == nil {
		return nil
	}
	return s.session
}

func (s *goalCapabilityTestSession) SessionStorage() runtimechat.SessionStorage {
	if s == nil {
		return nil
	}
	return s.storage
}

func (s *goalCapabilityTestSession) RefreshRuntimeSession(context.Context, *runtimechat.Session) error {
	return nil
}

func (s *goalCapabilityTestSession) ExecutorPath() aiclitools.ExposurePath {
	return aiclitools.ExposureShared
}

func TestExecuteUpdateGoalWithoutGoalIsStructuredNoOp(t *testing.T) {
	session := runtimechat.NewSession("tester")
	toolSession := &goalCapabilityTestSession{
		session: session,
		storage: runtimechat.NewInMemoryStorage(),
	}

	result, err := executeUpdateGoalCapability(context.Background(), toolSession, map[string]interface{}{
		"status":  string(StatusComplete),
		"summary": "all requested work is complete",
	})
	if err != nil {
		t.Fatalf("expected missing goal to be a no-op, got error: %v", err)
	}
	if result.Metadata["no_op"] != true || result.Metadata["reason"] != "goal_missing" {
		t.Fatalf("unexpected no-op metadata: %#v", result.Metadata)
	}

	output, ok := result.Output.(string)
	if !ok {
		t.Fatalf("expected JSON string output, got %T", result.Output)
	}
	var payload struct {
		Updated bool        `json:"updated"`
		Goal    interface{} `json:"goal"`
		Reason  string      `json:"reason"`
		Message string      `json:"message"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode result output: %v", err)
	}
	if payload.Updated || payload.Goal != nil || payload.Reason != "goal_missing" {
		t.Fatalf("unexpected no-op payload: %#v", payload)
	}
	if payload.Message == "" {
		t.Fatal("expected no-op payload to warn against claiming an update")
	}
}
