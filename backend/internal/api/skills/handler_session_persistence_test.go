package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

func TestAgentChatReturnsConflictWhenSessionLeaseIsHeld(t *testing.T) {
	storage, err := chat.NewFileStorage(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	manager := chat.NewSessionManager(storage, chat.DefaultSessionManagerConfig())
	defer manager.Stop()

	session, err := manager.Create(context.Background(), "lease-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	store := chat.NewInMemoryRuntimeStore(16)
	_, err = store.AcquireLease(context.Background(), chat.LeaseRequest{
		SessionID: session.ID,
		OwnerID:   "existing-owner",
		OwnerKind: "test",
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire existing lease: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(manager)
	handler.sessionRuntimeStore = store
	handler.sessionEventStore = store

	body := fmt.Sprintf(`{"messages":[{"role":"user","content":"hello"}],"session_id":%q,"user_id":"lease-user"}`, session.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.AgentChat(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session runtime lease conflict") {
		t.Fatalf("expected lease conflict body, got %s", rec.Body.String())
	}
}

func TestRuntimeStatusSnapshotIncludesSessionPersistence(t *testing.T) {
	root := t.TempDir()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = filepath.Join(root, "sessions")
	cfg.SessionRuntime.DefaultPersistence = "file"
	handler := NewHandler(nil, nil, nil)
	handler.runtimeConfig = cfg
	handler.runtimeConfigFile = filepath.Join(root, "runtime.yaml")

	snapshot := handler.runtimeStatusSnapshot(context.Background(), llm.HealthCheckModeNone)
	persistence, ok := snapshot["session_persistence"].(map[string]interface{})
	if !ok {
		t.Fatalf("session_persistence missing from runtime status: %#v", snapshot)
	}
	if persistence["session_dir"] != filepath.Join(root, "sessions") {
		t.Fatalf("unexpected session dir in persistence snapshot: %#v", persistence)
	}
	if persistence["default_persistence"] != "file" {
		t.Fatalf("expected default persistence to be file, got %#v", persistence["default_persistence"])
	}
	if persistence["session_runtime_store_path"] == "" || persistence["background_store_path"] == "" {
		t.Fatalf("expected runtime/background persistence paths: %#v", persistence)
	}
	if persistence["artifact_store_path"] == "" || persistence["background_log_dir"] == "" {
		t.Fatalf("expected artifact/background log paths: %#v", persistence)
	}
	if persistence["agent_control_mailbox_store_path"] == "" || persistence["agent_control_agent_store_path"] == "" {
		t.Fatalf("expected effective agent control mailbox/agent paths: %#v", persistence)
	}
	if _, ok := persistence["checkpoint_enabled"]; !ok {
		t.Fatalf("expected checkpoint_enabled in persistence snapshot: %#v", persistence)
	}
}

func TestRuntimeServerHandlerReadsSharedRuntimeEventsFromConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	configFile := filepath.Join(root, "runtime.yaml")
	sessionID := "session-shared-runtime"

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir
	cfg.SessionRuntime.DefaultPersistence = sessionruntime.PersistenceFile
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     cfg,
		ConfigFile: configFile,
		Mode:       sessionruntime.ModeServer,
	})

	runtimeStore, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{Path: paths.SessionRuntimeStorePath})
	if err != nil {
		t.Fatalf("open shared runtime store: %v", err)
	}
	seq, err := runtimeStore.AppendEvent(ctx, runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"content": "shared runtime event",
		},
	})
	if err != nil {
		t.Fatalf("append shared runtime event: %v", err)
	}
	if err := runtimeStore.Close(); err != nil {
		t.Fatalf("close shared runtime store: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetRuntimeConfig(cfg, configFile)
	defer closeHandlerPersistenceStoresForTest(handler)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+sessionID+"/runtime/events", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected runtime events status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []struct {
			Type    string                 `json:"type"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"events"`
		Count     int   `json:"count"`
		LatestSeq int64 `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode runtime events: %v", err)
	}
	if resp.Count != 1 || len(resp.Events) != 1 {
		t.Fatalf("expected one shared runtime event, got %#v", resp)
	}
	if resp.LatestSeq != seq || resp.Events[0].Type != chat.EventAssistantMessage {
		t.Fatalf("unexpected shared runtime event response: %#v", resp)
	}
	if resp.Events[0].Payload["content"] != "shared runtime event" {
		t.Fatalf("expected shared runtime event payload, got %#v", resp.Events[0].Payload)
	}
}

func TestRuntimeServerHandlerReadsSharedCheckpointStoreFromConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	configFile := filepath.Join(root, "runtime.yaml")

	storage, err := chat.NewFileStorage(sessionDir)
	if err != nil {
		t.Fatalf("new session storage: %v", err)
	}
	manager := chat.NewSessionManager(storage, chat.DefaultSessionManagerConfig())
	defer manager.Stop()
	session, err := manager.Create(ctx, "shared-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir
	cfg.SessionRuntime.DefaultPersistence = sessionruntime.PersistenceFile
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     cfg,
		ConfigFile: configFile,
		Mode:       sessionruntime.ModeServer,
	})

	store, err := artifact.NewStore(&artifact.StoreConfig{Path: paths.ArtifactStorePath})
	if err != nil {
		t.Fatalf("open shared artifact store: %v", err)
	}
	beforeBlobID, beforeHash, err := store.SaveBlob(ctx, []byte("old\n"))
	if err != nil {
		t.Fatalf("save before blob: %v", err)
	}
	afterBlobID, afterHash, err := store.SaveBlob(ctx, []byte("new\n"))
	if err != nil {
		t.Fatalf("save after blob: %v", err)
	}
	checkpointID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    session.ID,
		TaskID:       "task-shared",
		Reason:       "tool:edit",
		HistoryHash:  "hash-shared",
		MessageCount: 2,
		Metadata: map[string]interface{}{
			"origin": "aicli-local",
		},
	})
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	if err := store.SaveCheckpointFiles(ctx, checkpointID, []artifact.CheckpointFile{
		{
			Path:         "shared.txt",
			Op:           "modify",
			BeforeBlobID: beforeBlobID,
			AfterBlobID:  afterBlobID,
			BeforeHash:   beforeHash,
			AfterHash:    afterHash,
			DiffText:     "-old\n+new\n",
		},
	}); err != nil {
		t.Fatalf("save checkpoint files: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close shared artifact store: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(manager)
	handler.SetRuntimeConfig(cfg, configFile)
	defer closeHandlerPersistenceStoresForTest(handler)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/checkpoints", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkpoint list status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Checkpoints []struct {
			ID          string                 `json:"id"`
			SessionID   string                 `json:"session_id"`
			Reason      string                 `json:"reason"`
			HistoryHash string                 `json:"history_hash"`
			Metadata    map[string]interface{} `json:"metadata"`
		} `json:"checkpoints"`
		Count int `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode checkpoint list: %v", err)
	}
	if listResp.Count != 1 || len(listResp.Checkpoints) != 1 {
		t.Fatalf("expected one checkpoint from shared store, got %#v", listResp)
	}
	checkpoint := listResp.Checkpoints[0]
	if checkpoint.ID != checkpointID || checkpoint.SessionID != session.ID || checkpoint.HistoryHash != "hash-shared" {
		t.Fatalf("unexpected checkpoint from shared store: %#v", checkpoint)
	}
	if checkpoint.Metadata["origin"] != "aicli-local" {
		t.Fatalf("expected checkpoint metadata from shared store, got %#v", checkpoint.Metadata)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/checkpoints/"+checkpointID+"/files", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkpoint files status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var filesResp struct {
		Files []artifact.CheckpointFile `json:"files"`
		Count int                       `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&filesResp); err != nil {
		t.Fatalf("decode checkpoint files: %v", err)
	}
	if filesResp.Count != 1 || len(filesResp.Files) != 1 {
		t.Fatalf("expected one checkpoint file from shared store, got %#v", filesResp)
	}
	if filesResp.Files[0].Path != "shared.txt" || filesResp.Files[0].AfterBlobID != afterBlobID {
		t.Fatalf("unexpected checkpoint file from shared store: %#v", filesResp.Files[0])
	}
}

func TestRuntimeServerHandlerReadsSharedBackgroundStoreFromConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	configFile := filepath.Join(root, "runtime.yaml")
	logDir := filepath.Join(sessionDir, "runtime", "background_logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir background log dir: %v", err)
	}
	logPath := filepath.Join(logDir, "job_shared.log")
	if err := os.WriteFile(logPath, []byte("shared background output\n"), 0o644); err != nil {
		t.Fatalf("write background log: %v", err)
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir
	cfg.SessionRuntime.DefaultPersistence = sessionruntime.PersistenceFile
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     cfg,
		ConfigFile: configFile,
		Mode:       sessionruntime.ModeServer,
	})

	store, err := background.NewSQLiteStore(&background.StoreConfig{Path: paths.BackgroundStorePath})
	if err != nil {
		t.Fatalf("open shared background store: %v", err)
	}
	finishedAt := time.Now().UTC()
	exitCode := 0
	job := background.Job{
		ID:         "job_shared",
		SessionID:  "session-shared",
		Kind:       "shell",
		Command:    "echo shared",
		Status:     background.StatusCompleted,
		Message:    "done",
		CreatedAt:  finishedAt.Add(-time.Second),
		FinishedAt: &finishedAt,
		ExitCode:   &exitCode,
		LogPath:    logPath,
		Metadata: map[string]interface{}{
			"restart_policy": string(background.RestartPolicyRerun),
			"origin":         "aicli-local",
		},
	}
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("save background job: %v", err)
	}
	if err := store.AppendEvent(ctx, job.ID, "completed", map[string]interface{}{"origin": "aicli-local"}); err != nil {
		t.Fatalf("append background event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close shared background store: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetRuntimeConfig(cfg, configFile)
	defer closeHandlerPersistenceStoresForTest(handler)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs?session_id=session-shared", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background job list status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Jobs  []background.Job `json:"jobs"`
		Count int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode background job list: %v", err)
	}
	if listResp.Count != 1 || len(listResp.Jobs) != 1 || listResp.Jobs[0].ID != job.ID {
		t.Fatalf("expected one background job from shared store, got %#v", listResp)
	}
	if listResp.Jobs[0].RestartPolicy != background.RestartPolicyRerun {
		t.Fatalf("expected restart policy from shared metadata, got %#v", listResp.Jobs[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs/"+job.ID+"/events", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background events status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var eventsResp struct {
		Events []background.JobEvent `json:"events"`
		Count  int                   `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&eventsResp); err != nil {
		t.Fatalf("decode background events: %v", err)
	}
	if eventsResp.Count != 1 || len(eventsResp.Events) != 1 || eventsResp.Events[0].Type != "completed" {
		t.Fatalf("expected completed background event from shared store, got %#v", eventsResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs/"+job.ID+"/output", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background output status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var outputResp struct {
		Output background.TaskOutputResult `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&outputResp); err != nil {
		t.Fatalf("decode background output: %v", err)
	}
	if !strings.Contains(outputResp.Output.Output, "shared background output") {
		t.Fatalf("expected output from shared log file, got %#v", outputResp.Output)
	}
}

func closeHandlerPersistenceStoresForTest(handler *Handler) {
	if handler == nil {
		return
	}

	handler.sessionRuntimeMu.Lock()
	hub := handler.sessionHub
	stateStore := handler.sessionRuntimeStore
	eventStore := handler.sessionEventStore
	handler.sessionHub = nil
	handler.sessionRuntimeStore = nil
	handler.sessionEventStore = nil
	handler.sessionRuntimeStoreKey = ""
	handler.sessionRuntimeMu.Unlock()
	if hub != nil {
		hub.StopAll()
	}
	closeRuntimeStore(stateStore, eventStore)

	handler.teamStoreMu.Lock()
	teamStore := handler.teamStore
	handler.teamStore = nil
	handler.teamStoreConfigKey = ""
	handler.teamOrchestrator = nil
	handler.teamClaimsManager = nil
	handler.teamStoreMu.Unlock()
	if closer, ok := teamStore.(interface{ Close() error }); ok {
		_ = closer.Close()
	}

	handler.agentControlMu.Lock()
	agentControlService := handler.agentControlRegistryService
	handler.agentControlRegistryService = nil
	handler.agentControlRegistryStoreKey = ""
	handler.agentControlMailboxStore = nil
	handler.agentControlMailboxStoreKey = ""
	handler.agentControlMailboxStoreAuto = false
	handler.agentControlAgentStore = nil
	handler.agentControlAgentStoreKey = ""
	handler.agentControlAgentStoreAuto = false
	handler.agentControlMu.Unlock()
	if agentControlService != nil {
		_ = agentControlService.Close()
	}

	handler.backgroundMu.Lock()
	backgroundManager := handler.backgroundManager
	handler.backgroundManager = nil
	handler.backgroundConfigKey = ""
	handler.backgroundMu.Unlock()
	if backgroundManager != nil {
		_ = backgroundManager.Close()
	}
}
