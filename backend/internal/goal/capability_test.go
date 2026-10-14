package goal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestExecuteUpdateGoalRejectsBlockedGoal(t *testing.T) {
	session := runtimechat.NewSession("tester")
	toolSession := &goalCapabilityTestSession{
		session: session,
		storage: runtimechat.NewInMemoryStorage(),
	}
	goal, err := NewSessionGoal(session.ID, "blocked objective", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = StatusBlocked
	if err := NewMetadataStore().Put(session, goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	_, err = executeUpdateGoalCapability(context.Background(), toolSession, map[string]interface{}{
		"status":  string(StatusComplete),
		"summary": "attempted completion",
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked-goal rejection, got err=%v", err)
	}
}

func TestExecuteUpdateGoalMarksActiveGoalBlocked(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	session, err := manager.Create(context.Background(), "tester")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	defer manager.Stop()
	toolSession := &goalCapabilityTestSession{
		session: session,
		storage: storage,
	}
	goal, err := NewSessionGoal(session.ID, "active objective", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	if err := NewMetadataStore().Put(session, goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	result, err := executeUpdateGoalCapability(context.Background(), toolSession, map[string]interface{}{
		"status":  string(StatusBlocked),
		"summary": "same blocker repeated for three consecutive turns",
	})
	if err != nil {
		t.Fatalf("blocked write failed: %v", err)
	}
	output, _ := result.Output.(string)
	if !strings.Contains(output, "blocked") {
		t.Fatalf("expected blocked confirmation, got %q", result.Output)
	}
	persisted, err := manager.Get(context.Background(), session.ID)
	if err != nil || persisted == nil {
		t.Fatalf("manager.Load: %v", err)
	}
	stored, ok, err := NewMetadataStore().Get(persisted)
	if err != nil || !ok || stored == nil {
		t.Fatalf("expected stored goal, ok=%v err=%v", ok, err)
	}
	if stored.Status != StatusBlocked || stored.BlockedReason != "same blocker repeated for three consecutive turns" {
		t.Fatalf("unexpected stored goal: %+v", stored)
	}
}

func TestExecuteUpdateGoalRejectsBlockedOnNonActiveGoal(t *testing.T) {
	session := runtimechat.NewSession("tester")
	toolSession := &goalCapabilityTestSession{session: session, storage: runtimechat.NewInMemoryStorage()}
	goal, err := NewSessionGoal(session.ID, "paused objective", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = StatusPaused
	if err := NewMetadataStore().Put(session, goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	_, err = executeUpdateGoalCapability(context.Background(), toolSession, map[string]interface{}{
		"status":  string(StatusBlocked),
		"summary": "attempted block while paused",
	})
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("expected active-only rejection, got err=%v", err)
	}
}

func TestUpdateGoalDescriptionRequiresConfirmedActiveGoal(t *testing.T) {
	capabilities := Capabilities()
	for _, capability := range capabilities {
		if capability.Name != UpdateToolName {
			continue
		}
		if !strings.Contains(capability.Description, "get_goal confirmed a non-null active goal") {
			t.Fatalf("update_goal description must guard missing-goal no-ops: %q", capability.Description)
		}
		return
	}
	t.Fatal("update_goal capability not found")
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
