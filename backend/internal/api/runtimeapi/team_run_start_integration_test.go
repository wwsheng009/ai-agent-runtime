package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type teamRunStartBlockingProvider struct {
	started chan<- *llm.LLMRequest
	release <-chan struct{}
	calls   atomic.Int32
}

func (p *teamRunStartBlockingProvider) Name() string { return "team-run-start-integration" }

func (p *teamRunStartBlockingProvider) Call(ctx context.Context, request *llm.LLMRequest) (*llm.LLMResponse, error) {
	switch p.calls.Add(1) {
	case 1:
		select {
		case p.started <- request:
		default:
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &llm.LLMResponse{
			Content: "Reporting task completion.",
			Model:   p.Name(),
			ToolCalls: []types.ToolCall{{
				ID:   "report-team-run-task",
				Name: toolbroker.ToolTeamTaskReport,
				Args: map[string]interface{}{
					"task_status": "done",
					"summary":     "TeamRun task completed through the runtime actor",
				},
			}},
		}, nil
	case 2:
		return &llm.LLMResponse{
			Content: "```json\n{\"task_status\":\"done\",\"summary\":\"TeamRun task completed through the runtime actor\"}\n```",
			Model:   p.Name(),
		}, nil
	default:
		return &llm.LLMResponse{Content: "done", Model: p.Name()}, nil
	}
}

func (p *teamRunStartBlockingProvider) Stream(context.Context, *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *teamRunStartBlockingProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *teamRunStartBlockingProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *teamRunStartBlockingProvider) CheckHealth(context.Context) error { return nil }

func TestTeamRunStartAPISupervisesProjectedTaskThroughOutcomeAndSummary(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team-run-start-e2e.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	workerSession, err := sessionManager.Create(ctx, "team-run-start-worker")
	require.NoError(t, err)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	providerStarted := make(chan *llm.LLMRequest, 1)
	provider := &teamRunStartBlockingProvider{started: providerStarted, release: release}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: provider.Name(),
		MaxRetries:   0,
	})
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetSessionManager(sessionManager)
	handler.SetLLMRuntime(llmRuntime)
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.DefaultModel = provider.Name()
	handler.SetRuntimeConfig(runtimeConfig, "")
	handler.SetTeamOrchestrator(team.NewOrchestrator(store, nil, nil))
	require.NotNil(t, handler.getTeamOrchestrator())
	handler.teamOrchestrator.TickInterval = 10 * time.Millisecond
	t.Cleanup(handler.CloseSessionHub)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice": {UserID: "alice", WorkspaceID: "workspace-a"},
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	doRequest := func(method, path, idempotencyKey, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("X-API-Key", "alice")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	draftRecorder := doRequest(http.MethodPost, "/api/runtime/team-runs", "start-e2e-draft", `{
		"goal":"Complete one real task through the TeamRun Start path",
		"config":{
			"members":[{"id":"worker","name":"Worker","session_id":"`+workerSession.ID+`","state":"idle"}],
			"tasks":[{"id":"task-1","title":"Complete task","goal":"Report a successful task outcome","assignee":"worker"}]
		}
	}`)
	require.Equal(t, http.StatusCreated, draftRecorder.Code, draftRecorder.Body.String())
	var draftPayload struct {
		Run team.TeamRun `json:"run"`
	}
	require.NoError(t, json.Unmarshal(draftRecorder.Body.Bytes(), &draftPayload))
	require.Equal(t, team.TeamRunStatusDraft, draftPayload.Run.Status)

	startRecorder := doRequest(http.MethodPost,
		"/api/runtime/team-runs/"+draftPayload.Run.ID+"/start",
		"start-e2e-operation",
		`{"expected_version":1}`)
	require.Equal(t, http.StatusOK, startRecorder.Code, startRecorder.Body.String())
	var startPayload struct {
		Run team.TeamRun `json:"run"`
	}
	require.NoError(t, json.Unmarshal(startRecorder.Body.Bytes(), &startPayload))
	require.Equal(t, team.TeamRunStatusActive, startPayload.Run.Status)
	require.Equal(t, team.TeamRunPhaseRunning, startPayload.Run.Phase)
	require.NotEmpty(t, startPayload.Run.LegacyTeamID)

	select {
	case <-providerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("projected task did not reach the real runtime session provider")
	}

	lifecycle := handler.teamLifecycleService()
	require.Eventually(t, func() bool {
		snapshot := lifecycle.SupervisorSnapshot()
		return snapshot.LoopCount == 1 &&
			len(snapshot.Loops) == 1 &&
			snapshot.Loops[0].TeamID == startPayload.Run.LegacyTeamID &&
			snapshot.Loops[0].Running
	}, 2*time.Second, 10*time.Millisecond, "Start must create one running supervisor loop for its projected Team")
	startSnapshot := lifecycle.SupervisorSnapshot()
	require.Len(t, startSnapshot.Loops, 1)
	require.NotZero(t, startSnapshot.Loops[0].Generation)
	generation := startSnapshot.Loops[0].Generation

	lifecycle.SyncLoops()
	reconciledSnapshot := lifecycle.SupervisorSnapshot()
	require.Equal(t, 1, reconciledSnapshot.LoopCount)
	require.Len(t, reconciledSnapshot.Loops, 1)
	require.Equal(t, generation, reconciledSnapshot.Loops[0].Generation,
		"reconciling an already-owned Team must not create a duplicate supervisor generation")

	var projectedTask *team.Task
	require.Eventually(t, func() bool {
		tasks, listErr := store.ListTasks(ctx, team.TaskFilter{TeamID: startPayload.Run.LegacyTeamID})
		if listErr != nil || len(tasks) != 1 {
			return false
		}
		projectedTask = &tasks[0]
		return projectedTask.Status == team.TaskStatusRunning
	}, 2*time.Second, 10*time.Millisecond, "orchestrator must claim the atomically projected task before dispatch")
	require.NotNil(t, projectedTask)

	releaseProvider()
	require.Eventually(t, func() bool {
		loaded, loadErr := store.GetTask(ctx, projectedTask.ID)
		if loadErr != nil || loaded == nil {
			return false
		}
		projectedTask = loaded
		return loaded.Status == team.TaskStatusDone &&
			loaded.Summary == "TeamRun task completed through the runtime actor"
	}, 5*time.Second, 20*time.Millisecond, "the runtime actor's structured outcome must apply to the projected task")
	require.NotNil(t, projectedTask)

	require.Eventually(t, func() bool {
		legacyTeam, loadErr := store.GetTeam(ctx, startPayload.Run.LegacyTeamID)
		return loadErr == nil && legacyTeam != nil && legacyTeam.Status == team.TeamStatusDone
	}, 5*time.Second, 20*time.Millisecond, "the orchestrator must reconcile the completed task to a terminal Team")

	var (
		events       []team.TeamEventRecord
		eventsErr    error
		sawCompleted bool
		sawSummary   bool
	)
	require.Eventually(t, func() bool {
		events, eventsErr = store.ListTeamEvents(ctx, team.TeamEventFilter{
			TeamID: startPayload.Run.LegacyTeamID,
		})
		if eventsErr != nil {
			return false
		}
		sawCompleted = false
		sawSummary = false
		for _, event := range events {
			switch event.Type {
			case "team.completed":
				sawCompleted = event.Payload["status"] == string(team.TeamStatusDone)
			case "team.summary":
				sawSummary = event.Payload["summary"] != ""
			}
		}
		return sawCompleted && sawSummary
	}, 5*time.Second, 20*time.Millisecond, "terminal Team and final summary events must be durably observable")
	require.NoError(t, eventsErr)

	runRecorder := httptest.NewRecorder()
	runRequest := httptest.NewRequest(http.MethodGet, "/api/runtime/team-runs/"+draftPayload.Run.ID, nil)
	runRequest.Header.Set("X-API-Key", "alice")
	router.ServeHTTP(runRecorder, runRequest)
	require.Equal(t, http.StatusOK, runRecorder.Code, runRecorder.Body.String())
	var finalRunPayload struct {
		Run team.TeamRun `json:"run"`
	}
	require.NoError(t, json.Unmarshal(runRecorder.Body.Bytes(), &finalRunPayload))
	require.Equal(t, team.TeamStatusDone, team.TeamStatus(finalRunPayload.Run.Status),
		"the independent TeamRun resource must reflect the runtime's authoritative terminal outcome")
	require.Equal(t, "terminal", finalRunPayload.Run.Phase)
	require.NotNil(t, finalRunPayload.Run.TerminalAt)
	require.Equal(t, "ready", finalRunPayload.Run.SummaryState)
	require.GreaterOrEqual(t, finalRunPayload.Run.EventSeq, int64(3))
}
