package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestListAgentControlMailboxCombinesRuntimeAndTeamRegistries(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:agent-control-mailbox-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	session, err := sessionManager.Create(ctx, "user-global-mailbox")
	require.NoError(t, err)
	metadata := agentcontrol.Envelope{
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		MessageType:     agentcontrol.MessageTypeTeamLifecycle,
		ControlAction:   agentcontrol.ActionTeamLifecycle,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindTeamLifecycle,
	}.Metadata()
	_, _, err = runtimeStore.AppendAgentControlMailbox(ctx, session.ID, team.MailMessage{
		ID:        "session-mail",
		TeamID:    "team-1",
		FromAgent: "team-orchestrator",
		ToAgent:   "lead",
		Kind:      agentcontrol.MailboxKindTeamLifecycle,
		Body:      "session mailbox body",
		Metadata:  metadata,
		CreatedAt: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)

	teamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = teamStore.InsertMail(ctx, team.MailMessage{
		ID:        "team-mail",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "team mailbox body",
		CreatedAt: time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/mailbox?workflow=spawn_team&limit=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Records []agentcontrol.MailboxRecord `json:"records"`
		Count   int                          `json:"count"`
		Sources []string                     `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 2, payload.Count)
	require.ElementsMatch(t, []string{"runtime_sessions", "teams"}, payload.Sources)
	require.Len(t, payload.Records, 2)
	require.Equal(t, agentcontrol.MailboxScopeSession, payload.Records[0].Scope)
	require.Equal(t, "session-mail", payload.Records[0].MessageID)
	require.Equal(t, agentcontrol.MailboxScopeTeam, payload.Records[1].Scope)
	require.Equal(t, "team-mail", payload.Records[1].MessageID)
	require.Greater(t, payload.Records[1].Seq, payload.Records[0].Seq)
}

func TestListAgentControlMailboxUsesDurableGlobalRegistryWhenConfigured(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:agent-control-mailbox-global-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	handler.SetAgentControlMailboxStore(globalStore)
	t.Cleanup(func() {
		handler.SetAgentControlMailboxStore(nil)
		_ = globalStore.Close()
	})

	session, err := sessionManager.Create(ctx, "user-global-mailbox-durable")
	require.NoError(t, err)
	metadata := agentcontrol.Envelope{
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		MessageType:     agentcontrol.MessageTypeTeamLifecycle,
		ControlAction:   agentcontrol.ActionTeamLifecycle,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindTeamLifecycle,
	}.Metadata()
	_, _, err = runtimeStore.AppendAgentControlMailbox(ctx, session.ID, team.MailMessage{
		ID:        "session-mail",
		TeamID:    "team-1",
		FromAgent: "team-orchestrator",
		ToAgent:   "lead",
		Kind:      agentcontrol.MailboxKindTeamLifecycle,
		Body:      "session mailbox body",
		Metadata:  metadata,
		CreatedAt: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)

	teamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = teamStore.InsertMail(ctx, team.MailMessage{
		ID:        "team-mail",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "team mailbox body",
		CreatedAt: time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/mailbox?workflow=spawn_team&limit=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Records   []agentcontrol.MailboxRecord `json:"records"`
		Count     int                          `json:"count"`
		LatestSeq int64                        `json:"latest_seq"`
		Sources   []string                     `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 2, payload.Count)
	require.Equal(t, []string{"global", "runtime_sessions", "teams"}, payload.Sources)
	require.Len(t, payload.Records, 2)
	require.Equal(t, int64(1), payload.Records[0].Seq)
	require.Equal(t, agentcontrol.MailboxSourceGlobal, payload.Records[0].Source)
	require.Equal(t, "session-mail", payload.Records[0].MessageID)
	require.Equal(t, agentcontrol.MailboxSourceGlobal, payload.Records[1].Source)
	require.Equal(t, "team-mail", payload.Records[1].MessageID)
	require.Equal(t, payload.Records[1].Seq, payload.LatestSeq)
}

func TestSetAgentControlMailboxStoreReconcilesExistingProjections(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{
		Path: filepath.Join(t.TempDir(), "team.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	sessionEnvelope := agentcontrol.Envelope{
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MessageType:     agentcontrol.MessageTypeSubagentCompleted,
		ControlAction:   agentcontrol.ActionAgentCompleted,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
	}.Metadata()
	_, _, err = runtimeStore.AppendAgentControlMailbox(ctx, "api-local-runtime-session", team.MailMessage{
		ID:        "api-runtime-before-global",
		FromAgent: "child",
		ToAgent:   "root",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "runtime local first",
		Metadata:  sessionEnvelope,
		CreatedAt: time.Unix(10, 0).UTC(),
	})
	require.NoError(t, err)

	localTeamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "api-local-team"})
	require.NoError(t, err)
	_, err = teamStore.InsertMail(ctx, team.MailMessage{
		ID:        "api-team-before-global",
		TeamID:    localTeamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "team local first",
		CreatedAt: time.Unix(11, 0).UTC(),
	})
	require.NoError(t, err)

	globalTeamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "api-global-team"})
	require.NoError(t, err)
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_mailbox.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		handler.SetAgentControlMailboxStore(nil)
		_ = globalStore.Close()
	})

	runtimeGlobal, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "api-global-runtime-session",
		MessageID: "api-runtime-global-only",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "runtime global first",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
		CreatedAt: time.Unix(12, 0).UTC(),
	})
	require.NoError(t, err)
	teamGlobal, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Scope:     agentcontrol.MailboxScopeTeam,
		TeamID:    globalTeamID,
		MessageID: "api-team-global-only",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "team global first",
		CreatedAt: time.Unix(13, 0).UTC(),
	})
	require.NoError(t, err)

	handler.SetAgentControlMailboxStore(globalStore)

	require.Eventually(t, func() bool {
		runtimeGlobalRows, _ := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow:  agentcontrol.WorkflowSpawnAgent,
			SessionID: "api-local-runtime-session",
		})
		teamGlobalRows, _ := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			TeamID:   localTeamID,
		})
		runtimeLocalRows, _ := runtimeStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow:  agentcontrol.WorkflowSpawnAgent,
			SessionID: "api-global-runtime-session",
		})
		teamLocalRows, _ := teamStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			TeamID:   globalTeamID,
		})
		return len(runtimeGlobalRows) == 1 &&
			runtimeGlobalRows[0].Source == agentcontrol.MailboxSourceRuntimeSessions &&
			runtimeGlobalRows[0].MessageID == "api-runtime-before-global" &&
			runtimeGlobalRows[0].GlobalSeq > 0 &&
			len(teamGlobalRows) == 1 &&
			teamGlobalRows[0].Source == agentcontrol.MailboxSourceTeams &&
			teamGlobalRows[0].MessageID == "api-team-before-global" &&
			teamGlobalRows[0].GlobalSeq > 0 &&
			len(runtimeLocalRows) == 1 &&
			runtimeLocalRows[0].GlobalSeq == runtimeGlobal.Seq &&
			len(teamLocalRows) == 1 &&
			teamLocalRows[0].GlobalSeq == teamGlobal.Seq
	}, 3*time.Second, 20*time.Millisecond)
}
