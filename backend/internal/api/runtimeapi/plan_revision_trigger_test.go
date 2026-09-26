package runtimeapi

// §4.4 / §8.5 自动修订回合（trigger_revision）: a request_changes verdict can ask
// the runtime to start the revision round immediately instead of waiting for the
// next user input. The three endings under test:
//
//  1. no live actor → the decision still lands, the notes stay pending (degraded);
//  2. contract violations (not request_changes / empty notes) → 400;
//  3. live actor → one revision turn starts with the synthetic instruction, and
//     the review notes are delivered by that turn exactly once (request-only
//     system channel, never persisted twice).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

const planRevisionTestPlanPath = "docs/plan.md"

// postPlanModeDecision drives the real HTTP route and decodes the plan-mode
// response, so the assertions cover the same contract the Web panel consumes.
func postPlanModeDecision(t *testing.T, router *mux.Router, sessionID, body string) (*httptest.ResponseRecorder, sessionPlanModeResponse, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/plan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var resp sessionPlanModeResponse
	raw := rec.Body.Bytes()
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(raw, &resp))
	}
	return rec, resp, raw
}

func seedPlanModeWorkspace(t *testing.T, session *chat.Session) {
	t.Helper()
	workspace := t.TempDir()
	planAbs := filepath.Join(workspace, filepath.FromSlash(planRevisionTestPlanPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(planAbs), 0o755))
	require.NoError(t, os.WriteFile(planAbs, []byte("# Plan\n\n1. first pass\n"), 0o644))
	session.SetContext(sessionmeta.WorkspacePath, workspace)
}

// newPlanRevisionOfflineHandler builds a handler with a session manager but no
// live session hub: the store-only path.
func newPlanRevisionOfflineHandler(t *testing.T) (*Handler, *chat.SessionManager) {
	t.Helper()
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(manager.Stop)
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	return handler, manager
}

func historyContainsUserText(session *chat.Session, fragment string) bool {
	if session == nil {
		return false
	}
	for _, message := range session.GetMessages() {
		if message.Role != "user" {
			continue
		}
		if strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}

func TestPlanRevisionTriggerWithoutLiveActorDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	handler, manager := newPlanRevisionOfflineHandler(t)
	session, err := manager.Create(ctx, "user-plan-revision-offline")
	require.NoError(t, err)
	seedPlanModeWorkspace(t, session)
	require.NoError(t, manager.Update(ctx, session))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	enterRec, entered, _ := postPlanModeDecision(t, router, session.ID,
		`{"action":"enter","plan_path":"`+planRevisionTestPlanPath+`"}`)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())
	require.True(t, entered.Active)

	// request_changes + trigger_revision: the verdict must land even though no
	// actor can start the round, and the notes must stay pending for the next turn.
	rec, resp, raw := postPlanModeDecision(t, router, session.ID,
		`{"action":"request_changes","notes":"add rollback risks","trigger_revision":true}`)
	require.Equal(t, http.StatusOK, rec.Code, string(raw))
	require.True(t, resp.Active, "request_changes keeps plan mode active")
	require.Equal(t, "add rollback risks", resp.PendingReviewNotes)
	require.False(t, resp.RevisionTriggered)
	require.Contains(t, resp.RevisionError, "not attached to the live runtime")

	// Durable state: notes survive so the pre-existing delivery path still works.
	stored, err := manager.GetStorage().Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, "add rollback risks", planmode.Load(stored).PendingReviewNotes)
}

func TestPlanRevisionTriggerRejectsInvalidCombinations(t *testing.T) {
	ctx := context.Background()
	handler, manager := newPlanRevisionOfflineHandler(t)
	session, err := manager.Create(ctx, "user-plan-revision-validation")
	require.NoError(t, err)
	seedPlanModeWorkspace(t, session)
	require.NoError(t, manager.Update(ctx, session))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	enterRec, _, _ := postPlanModeDecision(t, router, session.ID,
		`{"action":"enter","plan_path":"`+planRevisionTestPlanPath+`"}`)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())

	// approve + trigger_revision is a contradiction.
	approveRec, _, approveRaw := postPlanModeDecision(t, router, session.ID,
		`{"action":"approve","notes":"looks good","trigger_revision":true}`)
	require.Equal(t, http.StatusBadRequest, approveRec.Code, string(approveRaw))
	require.Contains(t, string(approveRaw), "trigger_revision is only supported with request_changes")

	// request_changes without notes would ask for a blind rewrite.
	emptyRec, _, emptyRaw := postPlanModeDecision(t, router, session.ID,
		`{"action":"request_changes","trigger_revision":true}`)
	require.Equal(t, http.StatusBadRequest, emptyRec.Code, string(emptyRaw))
	require.Contains(t, string(emptyRaw), "trigger_revision requires non-empty notes")

	// The rejected calls must not have moved the durable plan state.
	stored, err := manager.GetStorage().Load(ctx, session.ID)
	require.NoError(t, err)
	state := planmode.Load(stored)
	require.True(t, planmode.IsActive(state))
	require.Empty(t, state.PendingReviewNotes)
	require.Empty(t, state.ExitDecision)
}

func TestPlanRevisionTriggerStartsRevisionTurnWithNotes(t *testing.T) {
	ctx := context.Background()
	handler, _, _ := newProfileSwitchTestHandler(t)
	session, err := handler.sessionManager.Create(ctx, "user-plan-revision-live")
	require.NoError(t, err)
	seedPlanModeWorkspace(t, session)
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	// Register the actor first so both the enter and the verdict run through the
	// live-actor path (the same path the Web panel hits in production).
	actor, err := handler.getSessionHub().GetOrCreate(session.ID)
	require.NoError(t, err)
	actor.Start()
	t.Cleanup(func() { handler.getSessionHub().StopAll() })

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	enterRec, entered, _ := postPlanModeDecision(t, router, session.ID,
		`{"action":"enter","plan_path":"`+planRevisionTestPlanPath+`"}`)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())
	require.True(t, entered.Active)

	rec, resp, raw := postPlanModeDecision(t, router, session.ID,
		`{"action":"request_changes","notes":"add rollback risks","trigger_revision":true}`)
	require.Equal(t, http.StatusOK, rec.Code, string(raw))
	require.True(t, resp.RevisionTriggered, "live actor must start the revision round")
	require.Empty(t, resp.RevisionError)

	// The triggered turn is asynchronous: the revision instruction shows up in the
	// session history as this turn's user prompt (durable, so poll the store).
	require.Eventually(t, func() bool {
		stored, loadErr := handler.sessionManager.GetStorage().Load(ctx, session.ID)
		if loadErr != nil {
			return false
		}
		return historyContainsUserText(stored, "按评审意见修订当前计划正文")
	}, 5*time.Second, 20*time.Millisecond, "revision turn must start with the synthetic instruction")

	// The notes travel through the request-only system channel and are consumed
	// exactly once: cleared from durable state, never duplicated into history.
	require.Eventually(t, func() bool {
		stored, loadErr := handler.sessionManager.GetStorage().Load(ctx, session.ID)
		if loadErr != nil {
			return false
		}
		return planmode.Load(stored).PendingReviewNotes == ""
	}, 5*time.Second, 20*time.Millisecond, "review notes must be consumed by the triggered turn")

	stored, err := handler.sessionManager.GetStorage().Load(ctx, session.ID)
	require.NoError(t, err)
	require.False(t, historyContainsUserText(stored, "add rollback risks"),
		"review notes are request-only: they must not be appended as a user message")
}
