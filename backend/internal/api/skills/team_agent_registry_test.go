package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestUpsertTeammateWritesAgentRegistryImmediately(t *testing.T) {
	ctx := context.Background()
	handler, teamStore, registry := newTeamAgentRegistryTestHandler(t)
	_, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-immediate"})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/team-immediate/teammates", strings.NewReader(`{
		"id":"member-1",
		"name":"Reviewer",
		"profile":"documentation-reviewer",
		"session_id":"mate-session",
		"state":"idle"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		TeamID:     "team-immediate",
		TeammateID: "member-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "team:team-immediate:member-1", records[0].AgentID)
	require.Equal(t, "team:team-immediate", records[0].RootSessionID)
	require.Equal(t, "mate-session", records[0].SessionID)
	require.Equal(t, "/root/teams/team-immediate/member-1", records[0].AgentPath)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, records[0].Workflow)
	require.Equal(t, "documentation-reviewer", records[0].AgentType)
	require.Equal(t, "Reviewer", records[0].Nickname)

	roots, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: "team:team-immediate",
		AgentPath:     "/root",
	})
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.Empty(t, roots[0].SessionID, "synthetic team roots must not claim the teammate session binding")
}

func TestUpdateTeammateWritesAgentRegistryAndDoesNotReopenClosedRecord(t *testing.T) {
	ctx := context.Background()
	handler, teamStore, registry := newTeamAgentRegistryTestHandler(t)
	_, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-update", LeadSessionID: "lead-session"})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:     "member-1",
		TeamID: "team-update",
		Name:   "Pending",
		State:  team.TeammateStateIdle,
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodPatch, "/api/runtime/teams/team-update/teammates/member-1", strings.NewReader(`{
		"name":"Builder",
		"profile":"build-engineer",
		"session_id":"patched-session"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		TeamID:     "team-update",
		TeammateID: "member-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "lead-session", records[0].RootSessionID)
	require.Equal(t, "lead-session", records[0].ParentSessionID)
	require.Equal(t, "patched-session", records[0].SessionID)
	require.Equal(t, "build-engineer", records[0].AgentType)
	require.Equal(t, "Builder", records[0].Nickname)

	_, err = registry.CloseAgentControlAgentSubtree(ctx, "lead-session", "/root/teams/team-update/member-1", time.Now().UTC())
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPatch, "/api/runtime/teams/team-update/teammates/member-1", strings.NewReader(`{
		"name":"Builder v2",
		"profile":"reviewer"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	records, err = registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		AgentID:       "team:team-update:member-1",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.True(t, records[0].Closed(), "teammate update should not reopen a closed durable registry row")
	require.Equal(t, "Builder", records[0].Nickname)
}

func TestUpdateTeammateClearsAgentRegistrySessionBinding(t *testing.T) {
	ctx := context.Background()
	handler, teamStore, registry := newTeamAgentRegistryTestHandler(t)
	_, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-clear", LeadSessionID: "lead-session"})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-1",
		TeamID:    "team-clear",
		Name:      "Reviewer",
		Profile:   "reviewer",
		SessionID: "old-session",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	require.NoError(t, handler.upsertTeammateAgentRegistryRecord(ctx, teamStore, team.Teammate{
		ID:        "member-1",
		TeamID:    "team-clear",
		Name:      "Reviewer",
		Profile:   "reviewer",
		SessionID: "old-session",
		State:     team.TeammateStateIdle,
	}))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodPatch, "/api/runtime/teams/team-clear/teammates/member-1", strings.NewReader(`{"session_id":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	active, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		TeamID:     "team-clear",
		TeammateID: "member-1",
	})
	require.NoError(t, err)
	require.Empty(t, active)
	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		AgentID:       "team:team-clear:member-1",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.True(t, records[0].Closed())
	require.Equal(t, "old-session", records[0].SessionID)
}

func TestSpawnTeamToolProjectsTeammatesIntoAgentRegistryImmediately(t *testing.T) {
	ctx := context.Background()
	handler, _, registry := newTeamAgentRegistryTestHandler(t)
	broker := &toolbroker.Broker{
		TeamStore:      handler.getTeamStore(),
		TeamDispatcher: handler,
	}

	raw, _, err := broker.Execute(ctx, "lead-session", toolbroker.ToolSpawnTeam, map[string]interface{}{
		"team_id": "team-tool",
		"teammates": []interface{}{
			map[string]interface{}{
				"id":         "member-1",
				"name":       "Reviewer",
				"profile":    "reviewer",
				"session_id": "tool-session",
			},
		},
	})
	require.NoError(t, err)
	result, ok := raw.(toolbroker.SpawnTeamResult)
	require.True(t, ok)
	require.Equal(t, "team-tool", result.TeamID)

	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		TeamID:     "team-tool",
		TeammateID: "member-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "team:team-tool:member-1", records[0].AgentID)
	require.Equal(t, "lead-session", records[0].RootSessionID)
	require.Equal(t, "tool-session", records[0].SessionID)
}

func TestSpawnTeamToolUsesRuntimeConfigAgentRegistryStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.AgentControl.AgentStorePath = filepath.Join(t.TempDir(), "agents.sqlite")
	handler.SetRuntimeConfig(cfg, "")
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)
	agentStore := handler.getAgentControlAgentStore()
	require.NotNil(t, agentStore)
	t.Cleanup(func() { _ = agentStore.Close() })

	broker := &toolbroker.Broker{
		TeamStore:      teamStore,
		TeamDispatcher: handler,
	}
	_, _, err = broker.Execute(ctx, "lead-session", toolbroker.ToolSpawnTeam, map[string]interface{}{
		"team_id": "team-configured",
		"teammates": []interface{}{
			map[string]interface{}{
				"id":         "member-1",
				"session_id": "configured-session",
			},
		},
	})
	require.NoError(t, err)

	records, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		TeamID:     "team-configured",
		TeammateID: "member-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "configured-session", records[0].SessionID)
}

func TestRuntimeConfigAgentControlStorePathOpensUnifiedRegistryService(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.AgentControl.StorePath = filepath.Join(t.TempDir(), "agent-control.sqlite")
	handler.SetRuntimeConfig(cfg, "")
	t.Cleanup(func() {
		if handler.agentControlRegistryService != nil {
			_ = handler.agentControlRegistryService.Close()
		}
	})

	mailboxStore := handler.getAgentControlMailboxStore()
	agentStore := handler.getAgentControlAgentStore()
	require.NotNil(t, mailboxStore)
	require.NotNil(t, agentStore)
	require.NotNil(t, handler.agentControlRegistryService)

	_, err := mailboxStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "root-session",
		MessageID: "message-1",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "hello",
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = agentStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	mailboxRows, err := mailboxStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{SessionID: "root-session"})
	require.NoError(t, err)
	require.Len(t, mailboxRows, 1)
	agentRows, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, agentRows, 1)
}

func newTeamAgentRegistryTestHandler(t *testing.T) (*Handler, *team.SQLiteStore, *agentcontrol.SQLiteGlobalAgentRegistryStore) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	registry, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	handler.SetTeamStore(teamStore)
	handler.SetAgentControlAgentStore(registry)
	return handler, teamStore, registry
}
