package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestResolveAICLIRuntimeExecution(t *testing.T) {
	mode, serverURL, err := resolveAICLIRuntimeExecution(nil, "", "", false, false)
	if err != nil || mode != aicliRuntimeModeLocal || serverURL != "" {
		t.Fatalf("default mode = %q url=%q err=%v", mode, serverURL, err)
	}

	mode, serverURL, err = resolveAICLIRuntimeExecution(nil, "server", "", true, false)
	if err != nil || mode != aicliRuntimeModeServer || serverURL != defaultAICLIRuntimeServerURL {
		t.Fatalf("server alias mode = %q url=%q err=%v", mode, serverURL, err)
	}

	mode, serverURL, err = resolveAICLIRuntimeExecution(nil, "127.0.0.1:9101", "", true, false)
	if err != nil || mode != aicliRuntimeModeServer || serverURL != "http://127.0.0.1:9101" {
		t.Fatalf("host alias mode = %q url=%q err=%v", mode, serverURL, err)
	}

	cfg := &config.Config{AICLI: &config.AICLIConfig{Runtime: &config.AICLIRuntimeConfig{
		Mode:      "auto",
		ServerURL: "http://127.0.0.1:8102/",
	}}}
	mode, serverURL, err = resolveAICLIRuntimeExecution(cfg, "", "", false, false)
	if err != nil || mode != aicliRuntimeModeAuto || serverURL != "http://127.0.0.1:8102" {
		t.Fatalf("config mode = %q url=%q err=%v", mode, serverURL, err)
	}
}

func TestPrepareRuntimeServerChatPersistenceFailsWhenExplicitServerUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverURL := server.URL
	server.Close()

	opts := &chatCommandOptions{
		RuntimeMode:      aicliRuntimeModeServer,
		RuntimeServerURL: serverURL,
	}
	manager, userID, sessionDir, configured, err := prepareRuntimeServerChatPersistence(nil, opts)
	if err == nil {
		t.Fatal("expected explicit runtime-server mode to fail when server is unavailable")
	}
	if manager != nil || userID != "" || sessionDir != "" || configured {
		t.Fatalf("expected no server persistence on failure, got manager=%v user=%q dir=%q configured=%v", manager, userID, sessionDir, configured)
	}
	if opts.RuntimeMode != aicliRuntimeModeServer || opts.RuntimeServerURL != serverURL {
		t.Fatalf("explicit server mode should not mutate opts on failure: %+v", opts)
	}
}

func TestPrepareRuntimeServerChatPersistenceAutoFallsBackWhenServerUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverURL := server.URL
	server.Close()

	opts := &chatCommandOptions{
		RuntimeMode:      aicliRuntimeModeAuto,
		RuntimeServerURL: serverURL,
	}
	manager, userID, sessionDir, configured, err := prepareRuntimeServerChatPersistence(nil, opts)
	if err != nil {
		t.Fatalf("expected auto runtime mode to fall back without error, got %v", err)
	}
	if manager != nil || userID != "" || sessionDir != "" || configured {
		t.Fatalf("expected local fallback with no server persistence, got manager=%v user=%q dir=%q configured=%v", manager, userID, sessionDir, configured)
	}
	if opts.RuntimeMode != aicliRuntimeModeLocal || opts.RuntimeServerURL != "" {
		t.Fatalf("expected auto fallback to mutate opts to local mode, got %+v", opts)
	}
}

func TestRuntimeServerHealthCheckRequiresUnifiedSessionActorContract(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer legacy.Close()
	if err := runtimeServerHealthCheck(context.Background(), legacy.URL); err == nil || !strings.Contains(err.Error(), "执行核心不兼容") {
		t.Fatalf("expected incompatible runtime core error, got %v", err)
	}

	unified := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":             true,
			"execution_core": runtimechat.SessionActorRuntimeCore(),
		})
	}))
	defer unified.Close()
	if err := runtimeServerHealthCheck(context.Background(), unified.URL); err != nil {
		t.Fatalf("expected unified runtime core health check to pass: %v", err)
	}
}

func TestConfigureRuntimeServerChatExecutorMarksUnifiedActorCoreReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":             true,
			"execution_core": runtimechat.SessionActorRuntimeCore(),
		})
	}))
	defer server.Close()

	session := &ChatSession{}
	configured, err := configureRuntimeServerChatExecutor(context.Background(), &chatCommandOptions{
		RuntimeMode:      aicliRuntimeModeServer,
		RuntimeServerURL: server.URL,
	}, session)
	if err != nil {
		t.Fatalf("configure runtime-server executor: %v", err)
	}
	if !configured || !session.ActorFirstReady || session.LocalRuntimeHost != nil {
		t.Fatalf("expected remote unified actor core, configured=%v ready=%v host=%#v", configured, session.ActorFirstReady, session.LocalRuntimeHost)
	}
	executor, err := ensureChatExecutor(session)
	if err != nil {
		t.Fatalf("ensure runtime-server executor: %v", err)
	}
	descriptor := executor.RuntimeDescriptor()
	if !descriptor.unifiedActorRuntime() || descriptor.Transport != aicliRuntimeTransportHTTP {
		t.Fatalf("unexpected runtime descriptor: %#v", descriptor)
	}
}

func TestAICLIRuntimeServerCurrentEventSeqUsesLatestSeq(t *testing.T) {
	var gotLimit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtime/sessions/session-1/runtime/events" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotLimit = r.URL.Query().Get("limit")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"events":     []interface{}{},
			"count":      0,
			"latest_seq": 42,
		})
	}))
	defer server.Close()

	executor := &aicliRuntimeServerChatExecutor{serverURL: server.URL}
	seq, err := executor.currentRuntimeServerEventSeq(context.Background(), &ChatSession{HTTPClient: server.Client()}, "session-1")
	if err != nil {
		t.Fatalf("current event seq failed: %v", err)
	}
	if seq != 42 {
		t.Fatalf("expected latest_seq 42, got %d", seq)
	}
	if gotLimit != "1" {
		t.Fatalf("expected current event seq to request limit=1, got %q", gotLimit)
	}
}

func TestAICLIRuntimeServerListEventsUsesWaitMS(t *testing.T) {
	var gotWaitMS string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtime/sessions/session-1/runtime/events" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotWaitMS = r.URL.Query().Get("wait_ms")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"events": []interface{}{
				runtimeServerTestEvent("session-1", "assistant_message", 7, map[string]interface{}{"content": "streamed"}),
			},
			"count":      1,
			"latest_seq": 7,
		})
	}))
	defer server.Close()

	executor := &aicliRuntimeServerChatExecutor{serverURL: server.URL}
	events, err := executor.listRuntimeServerEventsWithLimit(context.Background(), &ChatSession{HTTPClient: server.Client()}, "session-1", 6, 100, 5*time.Second)
	if err != nil {
		t.Fatalf("list events failed: %v", err)
	}
	if gotWaitMS != "5000" {
		t.Fatalf("expected wait_ms=5000, got %q", gotWaitMS)
	}
	if len(events) != 1 || events[0].Type != "assistant_message" || runtimeServerEventSeq(events[0]) != 7 {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestAICLIRuntimeServerToolAvailabilityUsesRemoteSurface(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtime/sessions/session-1/runtime/tools" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		requestCount++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id": "session-1",
			"tools": []runtimetypes.ToolDefinition{{
				Name:        "remote_echo",
				Description: "remote echo",
				Parameters:  map[string]interface{}{"type": "object"},
			}},
			"count":  1,
			"source": "runtime_server",
		})
	}))
	defer server.Close()

	session := &ChatSession{
		ChatExecutor:   &aicliRuntimeServerChatExecutor{serverURL: server.URL},
		RuntimeSession: &runtimechat.Session{ID: "session-1"},
		HTTPClient:     server.Client(),
	}
	if !chatToolAvailable(session, "remote_echo") {
		t.Fatal("expected remote_echo to be available from runtime-server tool surface")
	}
	if chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("did not expect update_goal to be available when remote surface omits it")
	}
	if requestCount != 1 {
		t.Fatalf("expected runtime tool surface to be cached, got %d requests", requestCount)
	}
}

func TestAICLIRuntimeServerToolAvailabilityUnsupportedSurfaceReturnsFalse(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtime/sessions/session-1/runtime/tools" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		requestCount++
		http.NotFound(w, r)
	}))
	defer server.Close()

	session := &ChatSession{
		ChatExecutor:   &aicliRuntimeServerChatExecutor{serverURL: server.URL},
		RuntimeSession: &runtimechat.Session{ID: "session-1"},
		HTTPClient:     server.Client(),
	}
	if chatToolAvailable(session, "remote_echo") {
		t.Fatal("did not expect tools to be available when runtime-server tool surface is unsupported")
	}
	if chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("did not expect update_goal to be available when runtime-server tool surface is unsupported")
	}
	if requestCount != 1 {
		t.Fatalf("expected unsupported runtime tool surface to be cached, got %d requests", requestCount)
	}
}

func TestAICLIRuntimeServerGoalGuidanceUsesRemoteGoalToolSurface(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	captureStdout(t, func() {
		handleGoalCommand(session, "/goal finish runtime-server goal guidance")
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtime/sessions/"+session.RuntimeSession.ID+"/runtime/tools" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id": session.RuntimeSession.ID,
			"tools": []runtimetypes.ToolDefinition{{
				Name:        updateGoalFunctionName,
				Description: "complete the current goal",
				Parameters:  map[string]interface{}{"type": "object"},
			}},
			"count":  1,
			"source": "runtime_server",
		})
	}))
	defer server.Close()

	session.ChatExecutor = &aicliRuntimeServerChatExecutor{serverURL: server.URL}
	session.HTTPClient = server.Client()
	prompt := renderActiveGoalGuidance(session)
	if !strings.Contains(prompt, "call update_goal") {
		t.Fatalf("expected runtime-server guidance to allow update_goal when remote surface exposes it, got %q", prompt)
	}
}

func TestAICLIRuntimeServerChatExecutorUsesRuntimeCommandsAndRefreshesHistory(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var got map[string]interface{}
	submitted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			events := []map[string]interface{}{}
			if submitted {
				events = []map[string]interface{}{
					runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 1, map[string]interface{}{"content": "server output"}),
					runtimeServerTestEvent(runtimeSession.ID, "session_end", 2, map[string]interface{}{"success": true}),
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "count": len(events)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			submitted = true
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			loaded.AddMessage(*runtimetypes.NewUserMessage("hello server"))
			loaded.AddMessage(*runtimetypes.NewAssistantMessage("server output"))
			if err := manager.Update(r.Context(), loaded); err != nil {
				t.Fatalf("update session: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		ReasoningEffort:  "medium",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    "cli-user",
		ProfileReference: "dev",
		ProfileName:      "dev",
		ProfileAgent:     "coder",
		HTTPClient:       server.Client(),
	}
	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "hello server")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if output != "server output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if got["type"] != "submit_prompt" || got["prompt"] != "hello server" {
		t.Fatalf("request did not submit runtime command: %#v", got)
	}
	loaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if loaded.UserID != "cli-user" ||
		runtimeSessionContextString(loaded, sessionmeta.ProfileRef) != "dev" ||
		runtimeSessionContextString(loaded, sessionmeta.ProfileAgent) != "coder" ||
		runtimeSessionContextString(loaded, sessionmeta.ProviderName) != "test-provider" ||
		runtimeSessionContextString(loaded, sessionmeta.Model) != "test-model" {
		t.Fatalf("session metadata did not include shared runtime context: %#v", loaded.Metadata.Context)
	}
	if len(session.Messages) != 2 || session.Messages[1].Content != "server output" {
		t.Fatalf("session history was not refreshed from server write: %#v", session.Messages)
	}
}

func TestAICLIRuntimeServerChatExecutorRejectsLegacyAgentChatFallback(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	agentChatCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": []interface{}{}, "count": 0, "latest_seq": 0})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			http.NotFound(w, r)
		case r.URL.Path == "/api/agent/chat":
			agentChatCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"output": "legacy output"}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
	}
	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "hello")
	if err == nil {
		t.Fatal("expected missing SessionActor command API to fail")
	}
	if output != "" {
		t.Fatalf("expected no legacy output, got %q", output)
	}
	if agentChatCalled {
		t.Fatal("runtime-server executor must not fall back to /api/agent/chat")
	}
	if !strings.Contains(err.Error(), "统一 SessionActor Runtime 命令接口") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAICLIRuntimeServerChatExecutorContinueGoalUsesHiddenAuditCommand(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var got map[string]interface{}
	submitted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			events := []map[string]interface{}{}
			if submitted {
				events = []map[string]interface{}{
					runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 1, map[string]interface{}{"content": "audit output"}),
					runtimeServerTestEvent(runtimeSession.ID, "session_end", 2, map[string]interface{}{"success": true}),
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "count": len(events)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			submitted = true
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			loaded.AddMessage(*runtimetypes.NewAssistantMessage("audit output"))
			if err := manager.Update(r.Context(), loaded); err != nil {
				t.Fatalf("update session: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
	}
	output, err := newAICLIRuntimeServerChatExecutor(server.URL).(aicliGoalContinuationExecutor).ContinueGoal(context.Background(), session)
	if err != nil {
		t.Fatalf("ContinueGoal failed: %v", err)
	}
	if output != "audit output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if got["type"] != "continue" || !strings.Contains(fmt.Sprint(got["prompt"]), "Continue working toward") {
		t.Fatalf("request did not submit hidden continuation command: %#v", got)
	}
	metadata, ok := got["continuation_metadata"].(map[string]interface{})
	if !ok || metadata[goalContinuationMetadataKey] != true {
		t.Fatalf("expected continuation metadata, got %#v", got["continuation_metadata"])
	}
	if !strings.Contains(fmt.Sprint(got["strip_metadata_keys"]), goalContinuationMetadataKey) {
		t.Fatalf("expected strip metadata key, got %#v", got["strip_metadata_keys"])
	}
	if len(session.Messages) != 1 || session.Messages[0].Content != "audit output" {
		t.Fatalf("session history was not refreshed from server write: %#v", session.Messages)
	}
}

func TestAICLIRuntimeServerChatExecutorApprovesRuntimeServerToolRequest(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var mu sync.Mutex
	submitted := false
	approved := false
	var approveBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			after := r.URL.Query().Get("after_seq")
			events := []map[string]interface{}{}
			if submitted && after == "" && !approved {
				events = append(events, runtimeServerTestEvent(runtimeSession.ID, "approval_requested", 1, map[string]interface{}{
					"request_id":             "approval-1",
					"tool_name":              "run_shell_command",
					"reason":                 "needs approval",
					"team_id":                "team-approval",
					"task_id":                "task-approval",
					"teammate_id":            "mate-approval",
					"permission_mode":        "default",
					"route_provider":         "openai",
					"route_model":            "gpt-test",
					"route_reasoning_effort": "high",
					"route_source":           "difficulty_level",
					"route_warnings":         []interface{}{"provider_fallback_parent"},
				}))
			}
			if approved && after == "1" {
				events = append(events,
					runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 2, map[string]interface{}{"content": "approved output"}),
					runtimeServerTestEvent(runtimeSession.ID, "session_end", 3, map[string]interface{}{"success": true}),
				)
				loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
				loaded.AddMessage(*runtimetypes.NewUserMessage("run approved tool"))
				loaded.AddMessage(*runtimetypes.NewAssistantMessage("approved output"))
				_ = manager.Update(r.Context(), loaded)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "count": len(events)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/state":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": map[string]interface{}{"session_id": runtimeSession.ID, "status": "running"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode command: %v", err)
			}
			switch body["type"] {
			case "submit_prompt":
				submitted = true
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
			case "approve_tool":
				approved = true
				approveBody = body
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
			default:
				t.Fatalf("unexpected command body: %#v", body)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.askApproval = func(approval *runtimechat.ApprovalRequest, contextLines []string) (chatApprovalAnswer, error) {
		if approval == nil || approval.ToolName != "run_shell_command" {
			t.Fatalf("unexpected approval request: %+v", approval)
		}
		require.Equal(t, []string{
			"team=team-approval task=task-approval teammate=mate-approval permission_mode=default",
			"provider=openai model=gpt-test reasoning=high route_source=difficulty_level warnings=provider_fallback_parent",
		}, contextLines)
		return chatApprovalAnswer{Allowed: true}, nil
	}
	bridge.writeLine = func(string) {}
	bridge.renderResponse = func(string) {}
	bridge.writePrompt = func() {}
	session.RuntimeEventBridge = bridge

	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "run approved tool")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if output != "approved output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if approveBody["request_id"] != "approval-1" || approveBody["allow"] != true {
		t.Fatalf("approval command not posted correctly: %#v", approveBody)
	}
}

// TestApproveRuntimeServerToolPostsFeedback 固化 §4.8 的 server 路径请求体：
// 拒绝理由作为 `feedback` 与 allow=false 一起下发，remember_scope 只在允许且
// 非空时下发（runtimeapi 的 approve_tool 已接受并校验这两个字段）；普通决策与
// 拒绝都保持原请求体不变。
func TestApproveRuntimeServerToolPostsFeedback(t *testing.T) {
	var mu sync.Mutex
	bodies := make([]map[string]interface{}, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode command: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer server.Close()

	session := &ChatSession{HTTPClient: server.Client()}
	executor := &aicliRuntimeServerChatExecutor{serverURL: server.URL}
	require.NoError(t, executor.approveRuntimeServerTool(context.Background(), session, "session-1", "request-1", false, "不要动这个文件", ""))
	require.NoError(t, executor.approveRuntimeServerTool(context.Background(), session, "session-1", "request-2", true, "", ""))
	require.NoError(t, executor.approveRuntimeServerTool(context.Background(), session, "session-1", "request-3", true, "", " SESSION "))
	require.NoError(t, executor.approveRuntimeServerTool(context.Background(), session, "session-1", "request-4", false, "", "project"))

	require.Len(t, bodies, 4)
	require.Equal(t, "approve_tool", bodies[0]["type"])
	require.Equal(t, "request-1", bodies[0]["request_id"])
	require.Equal(t, false, bodies[0]["allow"])
	require.Equal(t, "不要动这个文件", bodies[0]["feedback"])
	_, hasFeedback := bodies[1]["feedback"]
	require.False(t, hasFeedback, "an ordinary allow must keep the request body unchanged")
	_, hasScope := bodies[1]["remember_scope"]
	require.False(t, hasScope, "an ordinary allow must not carry a remember scope")
	require.Equal(t, "session", bodies[2]["remember_scope"], "an allow-always style decision forwards the normalized scope")
	_, denyScope := bodies[3]["remember_scope"]
	require.False(t, denyScope, "a denial must not carry a remember scope")
}

func TestExplainRuntimeServerApprovalPostsExplainPathAndDecodesPayload(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"explanation": "会删除 build 目录及其内容",
			"source":      "model",
			"model":       "gpt-test",
			"mode":        "on_demand",
			"cached":      true,
		})
	}))
	defer server.Close()

	session := &ChatSession{HTTPClient: server.Client()}
	executor := &aicliRuntimeServerChatExecutor{serverURL: server.URL}
	explanation, err := executor.explainRuntimeServerApproval(context.Background(), session, "session/1", "req id#2")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/runtime/sessions/session%2F1/runtime/approvals/req%20id%232/explain", gotPath)
	require.Empty(t, gotBody, "the explain endpoint is a read-only POST without a request body")
	require.Equal(t, "会删除 build 目录及其内容", explanation.Explanation)
	require.Equal(t, "model", explanation.Source)
	require.Equal(t, "gpt-test", explanation.Model)
	require.Equal(t, "on_demand", explanation.Mode)
	require.True(t, explanation.Cached)
}

func TestExplainRuntimeServerApprovalDecodesRulesFallbackAndHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"explanation": "工具：execute_shell_command",
			"source":      "rules",
			"mode":        "on_demand",
		})
	}))
	defer server.Close()
	session := &ChatSession{HTTPClient: server.Client()}
	executor := &aicliRuntimeServerChatExecutor{serverURL: server.URL}

	explanation, err := executor.explainRuntimeServerApproval(context.Background(), session, "s1", "r1")
	require.NoError(t, err, "a 200 + source=rules fallback is a success, not an error")
	require.Equal(t, "rules", explanation.Source)
	require.Empty(t, explanation.Model)
	require.False(t, explanation.Cached)

	// 缺少 session/request id 时本地拒绝，不发请求。
	_, err = executor.explainRuntimeServerApproval(context.Background(), session, " ", "r1")
	require.Error(t, err)
	_, err = executor.explainRuntimeServerApproval(context.Background(), session, "s1", " ")
	require.Error(t, err)

	for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"approval is no longer pending"}`))
			}))
			defer failing.Close()

			failingExecutor := &aicliRuntimeServerChatExecutor{serverURL: failing.URL}
			_, err := failingExecutor.explainRuntimeServerApproval(context.Background(), &ChatSession{HTTPClient: failing.Client()}, "s1", "r1")
			require.Error(t, err, "409/5xx must surface as an error instead of a panic")
			var httpErr *runtimeServerHTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, status, httpErr.StatusCode)
		})
	}
}

func TestAICLIRuntimeServerChatExecutorAnswersRuntimeServerQuestion(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var mu sync.Mutex
	submitted := false
	answered := false
	var answerBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			after := r.URL.Query().Get("after_seq")
			events := []map[string]interface{}{}
			if submitted && after == "" && !answered {
				events = append(events, runtimeServerTestEvent(runtimeSession.ID, "question_asked", 1, map[string]interface{}{
					"question_id": "question-1",
					"prompt":      "Need input",
					"required":    true,
				}))
			}
			if answered && after == "1" {
				events = append(events,
					runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 2, map[string]interface{}{"content": "question output"}),
					runtimeServerTestEvent(runtimeSession.ID, "session_end", 3, map[string]interface{}{"success": true}),
				)
				loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
				loaded.AddMessage(*runtimetypes.NewUserMessage("ask remote question"))
				loaded.AddMessage(*runtimetypes.NewAssistantMessage("question output"))
				_ = manager.Update(r.Context(), loaded)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "count": len(events)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/state":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": map[string]interface{}{"session_id": runtimeSession.ID, "status": "running"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode command: %v", err)
			}
			switch body["type"] {
			case "submit_prompt":
				submitted = true
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
			case "answer_question":
				answered = true
				answerBody = body
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
			default:
				t.Fatalf("unexpected command body: %#v", body)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.askQuestion = func(string, []string, bool) (string, error) { return "answer from cli", nil }
	bridge.writeLine = func(string) {}
	bridge.renderResponse = func(string) {}
	bridge.writePrompt = func() {}
	session.RuntimeEventBridge = bridge

	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "ask remote question")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if output != "question output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if answerBody["question_id"] != "question-1" || answerBody["answer"] != "answer from cli" {
		t.Fatalf("question command not posted correctly: %#v", answerBody)
	}
}

func TestAICLIRuntimeServerChatExecutorPrefersRuntimeStream(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var submitted bool
	var streamRequested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"events":     []interface{}{},
				"count":      0,
				"latest_seq": 0,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			streamRequested = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeRuntimeServerTestSSE(w,
				runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 1, map[string]interface{}{"content": "stream output"}),
				runtimeServerTestEvent(runtimeSession.ID, "session_end", 2, map[string]interface{}{"success": true}),
			)
			loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
			loaded.AddMessage(*runtimetypes.NewUserMessage("stream this turn"))
			loaded.AddMessage(*runtimetypes.NewAssistantMessage("stream output"))
			_ = manager.Update(r.Context(), loaded)
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode command: %v", err)
			}
			if body["type"] != "submit_prompt" {
				t.Fatalf("unexpected command body: %#v", body)
			}
			submitted = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, _ := manager.Get(r.Context(), runtimeSession.ID)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
	}
	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "stream this turn")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if output != "stream output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !submitted {
		t.Fatal("expected submit_prompt to be posted")
	}
	if !streamRequested {
		t.Fatal("expected runtime stream endpoint to be used")
	}
	loaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if len(loaded.History) != 2 || loaded.History[1].Content != "stream output" {
		t.Fatalf("session history was not refreshed from stream write: %#v", loaded.History)
	}
}

func runtimeServerTestEvent(sessionID, eventType string, seq int64, payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["seq"] = seq
	return map[string]interface{}{
		"type":       eventType,
		"session_id": sessionID,
		"payload":    payload,
	}
}

func writeRuntimeServerTestSSE(w http.ResponseWriter, events ...map[string]interface{}) {
	for _, event := range events {
		if event == nil {
			continue
		}
		payload, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if eventType, _ := event["type"].(string); eventType != "" {
			_, _ = w.Write([]byte("event: runtime_event\n"))
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func TestPrepareChatPersistence_ServerModeUsesRuntimeServerSessionManager(t *testing.T) {
	remoteStorage := runtimechat.NewInMemoryStorage()
	remoteManager := runtimechat.NewSessionManager(remoteStorage, runtimechat.DefaultSessionManagerConfig())
	defer remoteManager.Stop()

	handler := runtimeapi.NewHandler(nil, nil, nil)
	handler.SetSessionManager(remoteManager)
	router := mux.NewRouter()
	router.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":             true,
			"execution_core": runtimechat.SessionActorRuntimeCore(),
		})
	})
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	state, err := prepareChatPersistence(nil, &chatCommandOptions{
		RuntimeMode:       aicliRuntimeModeServer,
		RuntimeServerURL:  server.URL,
		SessionUserFlag:   "server-user",
		SessionTitleFlag:  "server title",
		NoInteractive:     true,
		OutputFormat:      "text",
		RuntimeModeFlag:   "server",
		RuntimeServerFlag: server.URL,
	}, nil)
	if err != nil {
		t.Fatalf("prepareChatPersistence failed: %v", err)
	}
	if state.runtimeSessionManager == nil {
		t.Fatal("expected runtime-server backed session manager")
	}
	defer state.runtimeSessionManager.Stop()
	if state.sessionUserID != "server-user" {
		t.Fatalf("unexpected user id: %q", state.sessionUserID)
	}

	session := &ChatSession{
		SessionManager: state.runtimeSessionManager,
		SessionUserID:  state.sessionUserID,
		SessionDir:     state.resolvedSessionDir,
	}
	if err := createNewRuntimeConversation(session, "server title"); err != nil {
		t.Fatalf("createNewRuntimeConversation failed: %v", err)
	}
	if session.RuntimeSession == nil || session.RuntimeSession.ID == "" {
		t.Fatal("expected CLI session to receive runtime-server session id")
	}

	remoteSessions, err := remoteManager.List(context.Background(), "server-user")
	if err != nil {
		t.Fatalf("list remote sessions: %v", err)
	}
	if len(remoteSessions) != 1 || remoteSessions[0].ID != session.RuntimeSession.ID {
		t.Fatalf("expected remote session to be created, got %#v", remoteSessions)
	}
	if remoteSessions[0].Metadata.Title != "server title" {
		t.Fatalf("expected remote title to be patched, got %q", remoteSessions[0].Metadata.Title)
	}
}

func TestAICLIRuntimeServerInterruptTurnPostsInterruptCommand(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var got map[string]interface{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		requests++
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true, "cancelled": false, "reason": "no_active_turn",
		})
	}))
	defer server.Close()

	session := &ChatSession{RuntimeSession: runtimeSession, HTTPClient: server.Client()}
	exec := newAICLIRuntimeServerChatExecutor(server.URL)
	interruptExec, ok := exec.(aicliRuntimeInterruptExecutor)
	if !ok {
		t.Fatal("runtime server executor must implement aicliRuntimeInterruptExecutor")
	}
	if err := interruptExec.InterruptTurn(context.Background(), session); err != nil {
		t.Fatalf("interrupt turn: %v", err)
	}
	if requests != 1 {
		t.Fatalf("expected exactly one interrupt request, got %d", requests)
	}
	if got["type"] != "interrupt" {
		t.Fatalf("expected interrupt command payload, got %#v", got)
	}
}

func TestAICLIRuntimeServerInterruptTurnWithoutSessionIsNoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s for session-less interrupt", r.Method, r.URL.Path)
	}))
	defer server.Close()

	exec := newAICLIRuntimeServerChatExecutor(server.URL).(aicliRuntimeInterruptExecutor)
	if err := exec.InterruptTurn(context.Background(), &ChatSession{}); err != nil {
		t.Fatalf("session-less interrupt must be a no-op, got %v", err)
	}
}

// runtimeServerInterruptFakeExecutor 覆盖 runInterruptCleanup 的 server 分支：
// 本地 host 缺失时必须把中断转发给实现 aicliRuntimeInterruptExecutor 的 executor。
type runtimeServerInterruptFakeExecutor struct {
	called int
	err    error
}

func (f *runtimeServerInterruptFakeExecutor) Execute(context.Context, *ChatSession, string) (string, error) {
	return "", nil
}

func (f *runtimeServerInterruptFakeExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return newAICLIActorRuntimeDescriptor(aicliRuntimeTransportHTTP)
}

func (f *runtimeServerInterruptFakeExecutor) ToolAvailable(*ChatSession, string) bool { return false }

func (f *runtimeServerInterruptFakeExecutor) InterruptTurn(context.Context, *ChatSession) error {
	f.called++
	return f.err
}

func TestRunInterruptCleanupForwardsToRuntimeServerExecutor(t *testing.T) {
	fake := &runtimeServerInterruptFakeExecutor{}
	session := &ChatSession{ChatExecutor: fake}
	done := make(chan struct{})
	session.runInterruptCleanup(done)
	if fake.called != 1 {
		t.Fatalf("expected remote interrupt forwarded once, got %d", fake.called)
	}
	select {
	case <-done:
	default:
		t.Fatal("expected interrupt cleanup signal to close")
	}
}

func TestExecuteManualChatCompactRoutesToRuntimeServer(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var got map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true,
				"status": map[string]interface{}{
					"mode": "local", "phase": "pre_turn", "reason": "",
					"provider": "openai", "model": "gpt-test",
					"trigger_token_limit": 6800, "max_context_tokens": 8000, "token_before": 7000,
				},
				"result": map[string]interface{}{
					"mode": "local", "phase": "pre_turn",
					"provider": "openai", "model": "gpt-test",
					"trigger_token_limit": 6800, "max_context_tokens": 8000,
					"token_before": 7000, "token_after": 2100,
					"compacted_messages": 12,
					"checkpoint_ids":     []string{"ckpt-1"},
					"usage_source":       "provider", "usage": nil,
					"replacement_history_len": 4,
					"reconciliation":          map[string]interface{}{"drift_count": 2, "correction_made": true},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		HTTPClient:     server.Client(),
		ChatExecutor:   newAICLIRuntimeServerChatExecutor(server.URL),
	}
	report, err := executeManualChatCompact(session, "local")
	if err != nil {
		t.Fatalf("server-mode compact failed: %v", err)
	}
	if got["type"] != "compact" || got["mode"] != "local" {
		t.Fatalf("expected compact command payload, got %#v", got)
	}
	if report == nil || report.Result == nil {
		t.Fatalf("expected compact result, got %#v", report)
	}
	if report.Result.TokenAfter != 2100 || report.Result.CompactedMessages != 12 || report.HistoryMessages != 4 {
		t.Fatalf("unexpected result mapping: %#v", report.Result)
	}
	if report.Result.Reconciliation == nil ||
		report.Result.Reconciliation.DriftCount != 2 ||
		!report.Result.Reconciliation.CorrectionMade {
		t.Fatalf("unexpected reconciliation mapping: %#v", report.Result.Reconciliation)
	}
	if report.Status.Mode != "local" || report.Status.MaxContextTokens != 8000 {
		t.Fatalf("unexpected status mapping: %#v", report.Status)
	}
	text := formatChatCompactReport(report)
	for _, want := range []string{"压缩完成", "token_after=2100", "history_messages=4", "context_drift=2", "correction=applied"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted report %q missing %q", text, want)
		}
	}
}

func TestAICLIRuntimeServerChatExecutorUploadsImagesBeforeSubmit(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	imagePath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(imagePath, []byte("fake-png-bytes"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	uploadedFiles := 0
	var submit map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/uploads":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			uploadedFiles = len(r.MultipartForm.File["file"])
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "accepted": 1,
				"attachments": []map[string]interface{}{
					{"name": "shot.png", "path": "/srv/uploads/shot.png", "bytes": 14},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/commands":
			if err := json.NewDecoder(r.Body).Decode(&submit); err != nil {
				t.Fatalf("decode submit: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/stream":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/runtime/events":
			events := []map[string]interface{}{
				runtimeServerTestEvent(runtimeSession.ID, "assistant_message", 1, map[string]interface{}{"content": "image output"}),
				runtimeServerTestEvent(runtimeSession.ID, "session_end", 2, map[string]interface{}{"success": true}),
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "count": len(events)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "cli-user",
		HTTPClient:     server.Client(),
		ImagePaths:     []string{imagePath},
	}
	output, err := newAICLIRuntimeServerChatExecutor(server.URL).Execute(context.Background(), session, "describe this image")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if output != "image output" {
		t.Fatalf("unexpected output: %q", output)
	}
	if uploadedFiles != 1 {
		t.Fatalf("expected exactly one uploaded file, got %d", uploadedFiles)
	}
	images, ok := submit["images"].([]interface{})
	if !ok || len(images) != 1 || images[0] != "/srv/uploads/shot.png" {
		t.Fatalf("submit payload missing uploaded image: %#v", submit)
	}
	if submit["type"] != "submit_prompt" || submit["prompt"] != "describe this image" {
		t.Fatalf("unexpected submit payload: %#v", submit)
	}
}

func TestChatBacktrackRoutesToRuntimeServer(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), runtimechat.DefaultSessionManagerConfig())
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "cli-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var previewReq, applyReq map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/turns":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"session_id": runtimeSession.ID,
				"count":      1,
				"turns": []map[string]interface{}{
					{
						"index": 0, "message_index": 0, "end_message_index": 2,
						"preview": "first", "base_checkpoint_id": "ckpt-1",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/backtrack/audit":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"session_id": runtimeSession.ID,
				"count":      1,
				"entries": []map[string]interface{}{
					{
						"id": "bt-1", "mode": "conversation",
						"user_turn_index": 0, "message_index": 0,
						"removed_message_count": 2, "removed_user_turns": 1,
						"truncated_to_message_count": 0,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/backtrack/preview":
			if err := json.NewDecoder(r.Body).Decode(&previewReq); err != nil {
				t.Fatalf("decode preview request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"session_id": runtimeSession.ID, "mode": "conversation",
					"user_turn_index": 0, "message_index": 0,
					"removed_message_count": 3, "removed_user_turns": 1,
					"truncated_to_message_count": 0, "anchor_preview": "first",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID+"/backtrack":
			if err := json.NewDecoder(r.Body).Decode(&applyReq); err != nil {
				t.Fatalf("decode apply request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"session_id": runtimeSession.ID, "mode": "conversation",
					"user_turn_index": 0, "message_index": 0,
					"removed_message_count": 2, "removed_user_turns": 1,
					"truncated_to_message_count": 0,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime/sessions/"+runtimeSession.ID:
			loaded, err := manager.Get(r.Context(), runtimeSession.ID)
			if err != nil {
				t.Fatalf("load session: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"session": loaded})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		HTTPClient:     server.Client(),
		ChatExecutor:   newAICLIRuntimeServerChatExecutor(server.URL),
	}
	if !chatBacktrackBackendAvailable(session) {
		t.Fatal("expected runtime-server backtrack backend to be available")
	}

	turns, err := chatBacktrackTurns(session)
	if err != nil || len(turns) != 1 || turns[0].BaseCheckpointID != "ckpt-1" {
		t.Fatalf("turns = (%#v, %v), want one annotated turn", turns, err)
	}
	entries, err := chatBacktrackAudit(session)
	if err != nil || len(entries) != 1 || entries[0].ID != "bt-1" {
		t.Fatalf("audit = (%#v, %v), want one tombstone", entries, err)
	}
	preview, err := chatBacktrackPreview(session, runtimechat.BacktrackRequest{UserTurnIndex: runtimechat.IntPtr(0)})
	if err != nil || preview == nil || preview.RemovedMessageCount != 3 {
		t.Fatalf("preview = (%#v, %v), want server preview result", preview, err)
	}
	if previewReq["preview_only"] != true {
		t.Fatalf("preview request must force preview_only, got %#v", previewReq)
	}

	outcome, err := applyChatBacktrack(session, runtimechat.BacktrackRequest{
		UserTurnIndex: runtimechat.IntPtr(0),
		Mode:          runtimechat.BacktrackModeConversation,
	})
	if err != nil || outcome.SyncErr != nil || outcome.Result == nil {
		t.Fatalf("apply outcome = (%#v, %v), want synced server result", outcome, err)
	}
	if outcome.Result.UserTurnIndex != 0 || applyReq["mode"] != "conversation" {
		t.Fatalf("unexpected apply result/request: %#v / %#v", outcome.Result, applyReq)
	}
}
