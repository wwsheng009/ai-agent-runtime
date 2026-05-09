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
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestListAgentControlAgentsProjectsEmptyWithoutDurableStore(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Agents []agentcontrol.AgentRecord `json:"agents"`
		Count  int                        `json:"count"`
		Source string                     `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "agent_control_projection", payload.Source)
	require.Equal(t, 0, payload.Count)
	require.Empty(t, payload.Agents)
}

func TestListAgentControlAgentsUsesDurableRegistryStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.SetAgentControlAgentStore(store)

	_, err = store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "root-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
	})
	require.NoError(t, err)
	_, err = store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "child-agent",
		RootSessionID:   "root-session",
		ParentAgentID:   "root-agent",
		ParentSessionID: "root-session",
		SessionID:       "child-session",
		AgentPath:       "/root/child-agent",
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?root_session_id=root-session&path_prefix=/root/child&limit=5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Agents  []agentcontrol.AgentRecord `json:"agents"`
		Count   int                        `json:"count"`
		Source  string                     `json:"source"`
		Filters map[string]interface{}     `json:"filters"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "agent_control_agents", payload.Source)
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Agents, 1)
	require.Equal(t, "child-agent", payload.Agents[0].AgentID)
	require.Equal(t, "/root/child-agent", payload.Agents[0].AgentPath)
	require.Equal(t, "root-session", payload.Filters["root_session_id"])
	require.Equal(t, "/root/child", payload.Filters["path_prefix"])
}

func TestListAgentControlAgentsProjectsSessionAgentsWithoutDurableStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	root, err := sessionManager.Create(ctx, "user-agent-projection")
	require.NoError(t, err)
	child := chat.NewSession(root.UserID)
	child.ID = "projected-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/projected-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?root_session_id="+root.ID+"&path_prefix=/root/projected", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Agents []agentcontrol.AgentRecord `json:"agents"`
		Count  int                        `json:"count"`
		Source string                     `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "agent_control_projection", payload.Source)
	require.Equal(t, 1, payload.Count)
	require.Equal(t, "projected-child", payload.Agents[0].SessionID)
	require.Equal(t, "worker", payload.Agents[0].AgentType)
	require.Equal(t, agentcontrol.WorkflowSpawnAgent, payload.Agents[0].Workflow)
}

func TestListAgentControlAgentsMaterializesProjectionIntoDurableStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.SetAgentControlAgentStore(store)

	root, err := sessionManager.Create(ctx, "user-agent-materialize")
	require.NoError(t, err)
	child := chat.NewSession(root.UserID)
	child.ID = "materialized-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/materialized-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?root_session_id="+root.ID+"&path_prefix=/root/materialized", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Agents []agentcontrol.AgentRecord `json:"agents"`
		Source string                     `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "agent_control_agents", payload.Source)
	require.Len(t, payload.Agents, 1)

	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: root.ID,
		PathPrefix:    "/root/materialized",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "materialized-child", records[0].SessionID)
}

func TestListAgentControlAgentsMaterializeDoesNotReopenClosedDurableRow(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.SetAgentControlAgentStore(store)

	root, err := sessionManager.Create(ctx, "user-agent-closed-projection")
	require.NoError(t, err)
	child := chat.NewSession(root.UserID)
	child.ID = "closed-materialized-child"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/closed-materialized-child")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	require.NoError(t, handler.materializeAgentControlAgentProjections(ctx, store, agentcontrol.AgentFilter{RootSessionID: root.ID}))
	_, err = store.CloseAgentControlAgentSubtree(ctx, root.ID, "/root/closed-materialized-child", time.Now().UTC())
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?root_session_id="+root.ID+"&include_closed=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		SessionID:     "closed-materialized-child",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.True(t, records[0].Closed(), "projection should not reopen durable closed row: %#v", records[0])
}

func TestListAgentControlAgentsProjectsTeamTeammatesWithoutDurableStore(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer teamStore.Close()
	handler.teamStore = teamStore
	teamID, err := teamStore.CreateTeam(ctx, team.Team{
		ID:            "project-team",
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Reviewer",
		Profile:   "reviewer",
		SessionID: "mate-session",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?team_id=project-team&path_prefix=/root/teams/project-team", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Agents []agentcontrol.AgentRecord `json:"agents"`
		Count  int                        `json:"count"`
		Source string                     `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "agent_control_projection", payload.Source)
	require.Equal(t, 1, payload.Count)
	require.Equal(t, "mate-session", payload.Agents[0].SessionID)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, payload.Agents[0].Workflow)
	require.Equal(t, "member-1", payload.Agents[0].TeammateID)
	require.Equal(t, "/root/teams/project-team/member-1", payload.Agents[0].AgentPath)
}

func TestListAgentControlAgentsMaterializesTeamSessionContextAsCanonicalTeammate(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)
	registry, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	handler.SetAgentControlAgentStore(registry)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	handler.SetTeamStore(teamStore)

	root, err := sessionManager.Create(ctx, "user-team-context")
	require.NoError(t, err)
	teamID, err := teamStore.CreateTeam(ctx, team.Team{
		ID:            "team-context",
		LeadSessionID: root.ID,
	})
	require.NoError(t, err)
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Reviewer",
		Profile:   "reviewer",
		SessionID: "team-session",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	child := chat.NewSession(root.UserID)
	child.ID = "team-session"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, root.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/teams/team-context/member-1")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextTeamID, "team-context")
	child.SetContext(toolbroker.AgentSessionContextTeammateID, "member-1")
	child.SetContext(toolbroker.AgentSessionContextAgentType, "reviewer")
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	require.NoError(t, handler.materializeAgentControlAgentProjections(ctx, registry, agentcontrol.AgentFilter{RootSessionID: root.ID}))
	records, err := registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: root.ID,
		PathPrefix:    "/root/teams/team-context",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "team:team-context:member-1", records[0].AgentID)
	require.Equal(t, "team-session", records[0].SessionID)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, records[0].Workflow)
	require.Equal(t, "member-1", records[0].TeammateID)
}
