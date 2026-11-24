package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

type recordingV2PurgeStorage struct {
	chat.SessionStorage
	record chat.SessionPurgeRecord

	ackSessionGeneration      int64
	ackPurgeGeneration        int64
	completeSessionGeneration int64
	completePurgeGeneration   int64
}

func (s *recordingV2PurgeStorage) BeginSessionPurge(context.Context, string, string) (chat.SessionPurgeRecord, error) {
	return s.record, nil
}

func (s *recordingV2PurgeStorage) AcknowledgeSessionMemoryPurge(context.Context, string, int64) error {
	return nil
}

func (s *recordingV2PurgeStorage) CompleteSessionPurge(context.Context, string, int64) error {
	return nil
}

func (s *recordingV2PurgeStorage) ListPendingSessionPurges(context.Context, int) ([]chat.SessionPurgeRecord, error) {
	return nil, nil
}

func (s *recordingV2PurgeStorage) AcknowledgeSessionMemoryPurgeWithGenerations(
	_ context.Context, _ string, sessionGeneration, purgeGeneration int64,
) error {
	s.ackSessionGeneration = sessionGeneration
	s.ackPurgeGeneration = purgeGeneration
	return nil
}

func (s *recordingV2PurgeStorage) CompleteSessionPurgeWithGenerations(
	_ context.Context, _ string, sessionGeneration, purgeGeneration int64,
) error {
	s.completeSessionGeneration = sessionGeneration
	s.completePurgeGeneration = purgeGeneration
	return nil
}

func TestWireSessionMemoryPurgeCoordinatorUsesIndependentGenerations(t *testing.T) {
	ctx := context.Background()
	storage := &recordingV2PurgeStorage{
		SessionStorage: chat.NewInMemoryStorage(),
		record: chat.SessionPurgeRecord{
			SessionID:         "api-v2-purge-session",
			SessionGeneration: 41,
			PurgeGeneration:   7,
			Generation:        7,
			MemoryRequired:    true,
			State:             "draining",
		},
	}
	manager := chat.NewSessionManager(storage, &chat.SessionManagerConfig{
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	store, err := memorystore.New(memorystore.Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "api-v2-wiring",
	})
	require.NoError(t, err)
	runtime := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode:       "on",
		Workspace:  t.TempDir(),
		ProjectKey: store.ProjectKey(),
	})
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	wireSessionMemoryPurgeCoordinator(manager, runtime)
	require.NoError(t, manager.Delete(ctx, storage.record.SessionID))
	require.Equal(t, int64(41), storage.ackSessionGeneration)
	require.Equal(t, int64(7), storage.ackPurgeGeneration)
	require.Equal(t, int64(41), storage.completeSessionGeneration)
	require.Equal(t, int64(7), storage.completePurgeGeneration)
}

func newRegistryTestRuntime(t *testing.T, workspace string) (*memoryruntime.Runtime, *memorystore.Store) {
	t.Helper()
	workspace = canonicalMemoryWorkspacePath(workspace)
	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode:       "on",
		Workspace:  workspace,
		ProjectKey: store.ProjectKey(),
	}), store
}

func TestMemoryRuntimeRegistryRoutesCanonicalWorkspaceWithoutFallback(t *testing.T) {
	workspaceA := canonicalMemoryWorkspacePath(t.TempDir())
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, _ := newHarnessTestRouter(t)
	runtimeA, _ := newRegistryTestRuntime(t, workspaceA)
	runtimeB, _ := newRegistryTestRuntime(t, workspaceB)

	handler.SetMemoryRuntime(runtimeA)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	require.Same(t, runtimeA, handler.MemoryRuntime())
	require.Same(t, runtimeA, handler.MemoryRuntimeForWorkspace(workspaceA))
	require.Same(t, runtimeA, handler.MemoryRuntimeForWorkspace(filepath.Join(workspaceA, ".")))
	require.Same(t, runtimeB, handler.MemoryRuntimeForWorkspace(workspaceB))
	require.Nil(t, handler.MemoryRuntimeForWorkspace(t.TempDir()), "unknown workspace must not fall back to the default runtime")

	require.NoError(t, handler.CloseMemoryRuntime())
	require.Nil(t, handler.MemoryRuntime())
	require.Nil(t, handler.MemoryRuntimeForWorkspace(workspaceA))
	require.Nil(t, handler.MemoryRuntimeForWorkspace(workspaceB))
}

func TestMemoryRuntimeRegistryRecordsPerWorkspaceBootError(t *testing.T) {
	workspaceA := canonicalMemoryWorkspacePath(t.TempDir())
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, _ := newHarnessTestRouter(t)
	runtimeA, _ := newRegistryTestRuntime(t, workspaceA)
	handler.SetMemoryRuntime(runtimeA)

	handler.setMemoryRuntimeBootErrorForWorkspace(workspaceB, context.Canceled)
	require.Equal(t, "context canceled", handler.memoryRuntimeBootErrorForWorkspace(workspaceB))
	require.Empty(t, handler.memoryRuntimeBootErrorForWorkspace(workspaceA))
	require.Empty(t, handler.MemoryRuntimeBootError())
}

func TestMemoryRuntimeEnableForUnknownWorkspaceFailsClosed(t *testing.T) {
	workspace := canonicalMemoryWorkspacePath(t.TempDir())
	handler, _ := newHarnessTestRouter(t)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "on"
	handler.SetRuntimeConfig(cfg, "")

	_, err := handler.EnableMemoryRuntimeForWorkspace(workspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
	require.Nil(t, handler.MemoryRuntimeForWorkspace(workspace))
	require.Contains(t, handler.memoryRuntimeBootErrorForWorkspace(workspace), "not configured")
}

func TestResolveMemoryWorkspaceAllowsAdminCrossWorkspaceOnly(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	handler, _ := newMemoryTestRouter(t, workspaceA)

	target := "/api/runtime/memory/status?workspace_path=" + url.QueryEscape(workspaceB)
	adminReq := httptest.NewRequest(http.MethodGet, target, nil)
	adminReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	resolved, err := handler.resolveMemoryWorkspace(adminReq, "")
	require.NoError(t, err)
	require.Equal(t, canonicalMemoryWorkspacePath(workspaceB), resolved)

	ordinaryReq := httptest.NewRequest(http.MethodGet, target, nil)
	_, err = handler.resolveMemoryWorkspace(ordinaryReq, "")
	require.ErrorContains(t, err, "does not match the active runtime workspace")

	missingReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/status?workspace_path="+url.QueryEscape(filepath.Join(workspaceB, "missing")), nil)
	missingReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	_, err = handler.resolveMemoryWorkspace(missingReq, "")
	require.ErrorContains(t, err, "not an accessible directory")
}

func TestMemoryStatusUsesWorkspaceRegistryRuntime(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, router := newMemoryTestRouter(t, workspaceA)
	runtimeB, _ := newRegistryTestRuntime(t, workspaceB)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	req := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/status?workspace_path="+url.QueryEscape(workspaceB), nil)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response memoryStatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Equal(t, workspaceB, response.WorkspacePath)
	require.True(t, response.MemoryRuntimePresent)
	require.Equal(t, workspaceB, response.MemoryRuntimeWorkspace)
	require.Equal(t, "active", response.MemoryRuntimeState)
}

func TestMemoryWorkspaceRegistryKeepsRecordCorporaIsolated(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, router := newMemoryTestRouter(t, workspaceA)
	runtimeB, _ := newRegistryTestRuntime(t, workspaceB)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	create := func(workspace, text string) string {
		t.Helper()
		body := map[string]interface{}{
			"text": text, "kind": "decision", "session_id": "workspace-isolation",
		}
		if workspace != "" {
			body["workspace_path"] = workspace
		}
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var created memoryRecordDTO
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&created))
		require.NotEmpty(t, created.ID)
		return created.ID
	}
	noteA := create("", "workspace A isolated record")
	noteB := create(workspaceB, "workspace B isolated record")

	list := func(workspace string) []string {
		t.Helper()
		target := "/api/runtime/memory/records?status=active"
		if workspace != "" {
			target += "&workspace_path=" + url.QueryEscape(workspace)
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response memoryRecordListResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
		ids := make([]string, 0, len(response.Items))
		for _, item := range response.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	require.ElementsMatch(t, []string{noteA}, list(""))
	require.ElementsMatch(t, []string{noteB}, list(workspaceB))
}

func TestMemoryWorkspaceStatusKeepsDigestAndJobBacklogsIsolated(t *testing.T) {
	workspaceA := canonicalMemoryWorkspacePath(t.TempDir())
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, router := newMemoryTestRouter(t, workspaceA)
	runtimeA, storeA := newRegistryTestRuntime(t, workspaceA)
	runtimeB, storeB := newRegistryTestRuntime(t, workspaceB)
	handler.SetMemoryRuntime(runtimeA)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	seed := func(store *memorystore.Store, prefix string, digests, jobs int) {
		t.Helper()
		for index := 0; index < digests; index++ {
			_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
				SessionID: "workspace-status-" + prefix, SessionGeneration: 1,
				TurnID: prefix + "-digest-" + string(rune('a'+index)),
				Status: memorystore.TurnDigestPending,
			})
			require.NoError(t, err)
		}
		for index := 0; index < jobs; index++ {
			_, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
				Kind: "phase1_extract/v2", JobKey: prefix + "-job-" + string(rune('a'+index)),
				ProjectKey: store.ProjectKey(), Status: memorystore.JobStatusPending,
			})
			require.NoError(t, err)
		}
	}
	seed(storeA, "a", 1, 1)
	seed(storeB, "b", 2, 2)

	status := func(workspace string) memoryStatusResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet,
			"/api/runtime/memory/status?workspace_path="+url.QueryEscape(workspace), nil)
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response memoryStatusResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
		return response
	}
	statusA := status(workspaceA)
	statusB := status(workspaceB)
	require.Equal(t, 1, statusA.TurnDigests["pending"])
	require.Equal(t, 2, statusB.TurnDigests["pending"])
	require.Equal(t, 1, statusA.Jobs["pending"])
	require.Equal(t, 2, statusB.Jobs["pending"])
}

func TestMemorySessionWorkspaceRoutesRegistryRuntime(t *testing.T) {
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, _ := newHarnessTestRouter(t)
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)
	handler.sessionManager = manager

	session, err := manager.CreateSession(context.Background(), "workspace-user")
	require.NoError(t, err)
	require.NoError(t, manager.UpdateContext(context.Background(), session.ID, sessionmeta.WorkspacePath, workspaceB))

	runtimeB, _ := newRegistryTestRuntime(t, workspaceB)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	resolvedWorkspace := handler.memorySessionWorkspace(session.ID)
	require.Equal(t, workspaceB, resolvedWorkspace)
	require.Same(t, runtimeB, handler.MemoryRuntimeForWorkspace(resolvedWorkspace))
}

func TestMemoryRuntimeSnapshotIncludesDefaultAndRegisteredWorkspaces(t *testing.T) {
	workspaceA := canonicalMemoryWorkspacePath(t.TempDir())
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	handler, _ := newHarnessTestRouter(t)
	runtimeA, _ := newRegistryTestRuntime(t, workspaceA)
	runtimeB, _ := newRegistryTestRuntime(t, workspaceB)
	handler.SetMemoryRuntime(runtimeA)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	snapshot := handler.memoryRuntimeSnapshot()
	require.Len(t, snapshot, 2)
	require.Same(t, runtimeA, snapshot[memoryWorkspaceKey(workspaceA)])
	require.Same(t, runtimeB, snapshot[memoryWorkspaceKey(workspaceB)])
	require.Equal(t, []string{
		memoryWorkspaceKey(workspaceA),
		memoryWorkspaceKey(workspaceB),
	}, sortedMemoryRuntimeKeys(snapshot))
}

func TestMemoryIdleSweepAttemptsEveryRegisteredWorkspace(t *testing.T) {
	workspaceA := canonicalMemoryWorkspacePath(t.TempDir())
	workspaceB := canonicalMemoryWorkspacePath(t.TempDir())
	storeA, err := memorystore.New(memorystore.Config{ProjectRoot: workspaceA, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeA.Close()) })
	storeB, err := memorystore.New(memorystore.Config{ProjectRoot: workspaceB, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storeB.Close()) })
	runtimeA := memoryruntime.New(storeA, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: workspaceA, GenerationTrigger: "idle",
	})
	runtimeB := memoryruntime.New(storeB, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: workspaceB, GenerationTrigger: "idle",
	})

	handler, _ := newHarnessTestRouter(t)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Generation.Enabled = true
	cfg.Memory.Generation.Trigger = "idle"
	handler.SetRuntimeConfig(cfg, "")
	handler.SetMemoryRuntime(runtimeA)
	handler.memoryRuntimeMu.Lock()
	handler.registerMemoryRuntimeLocked(runtimeB, false)
	handler.memoryRuntimeMu.Unlock()

	result, err := handler.RunMemoryIdleSweep(context.Background())
	require.Error(t, err)
	require.Equal(t, memoryruntime.TriggerIdle, result.Trigger)
	joined := strings.Join(result.Errors, " | ")
	require.Contains(t, joined, memoryWorkspaceKey(workspaceA))
	require.Contains(t, joined, memoryWorkspaceKey(workspaceB))
}

func TestMemoryRunResultAggregation(t *testing.T) {
	result := memoryruntime.RunResult{Trigger: memoryruntime.TriggerIdle}
	mergeMemoryRunResult(&result, memoryruntime.RunResult{
		Trigger: memoryruntime.TriggerIdle, Sessions: 2, Processed: 1, Candidates: 3,
		Ignored: []string{"ignore-a"}, Errors: []string{"error-a"},
	})
	mergeMemoryRunResult(&result, memoryruntime.RunResult{
		Trigger: memoryruntime.TriggerIdle, Sessions: 4, Skipped: 2, Failed: 1,
		RolledUp: 2, RollupFailed: 1, Errors: []string{"error-b"},
	})
	require.Equal(t, 6, result.Sessions)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 2, result.Skipped)
	require.Equal(t, 3, result.Candidates)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, 2, result.RolledUp)
	require.Equal(t, 1, result.RollupFailed)
	require.Equal(t, []string{"ignore-a"}, result.Ignored)
	require.Equal(t, []string{"error-a", "error-b"}, result.Errors)
}

func TestMemoryConsolidationResultAggregation(t *testing.T) {
	result := memoryruntime.ConsolidationResult{}
	mergeMemoryConsolidationResult(&result, memoryruntime.ConsolidationResult{
		Promoted: 1, Superseded: 2, TokenCount: 10, Rebuilt: 1,
		SummaryID: "summary-a", Revision: 3, SummaryText: "text-a", SummaryModel: "model-a",
	})
	mergeMemoryConsolidationResult(&result, memoryruntime.ConsolidationResult{
		Promoted: 2, TokenCount: 5, RebuildJobs: 1, OpenQuestions: 1,
		SummaryID: "summary-b", Revision: 4, SummaryText: "text-b", SummaryModel: "model-b",
	})
	require.Equal(t, 3, result.Promoted)
	require.Equal(t, 2, result.Superseded)
	require.Equal(t, 15, result.TokenCount)
	require.Equal(t, 1, result.Rebuilt)
	require.Equal(t, 1, result.RebuildJobs)
	require.Equal(t, 1, result.OpenQuestions)
	require.Equal(t, "summary-a", result.SummaryID)
	require.Equal(t, int64(3), result.Revision)
	require.Equal(t, "text-a", result.SummaryText)
	require.Equal(t, "model-a", result.SummaryModel)
}
