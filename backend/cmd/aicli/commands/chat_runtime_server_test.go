package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
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
					"request_id": "approval-1",
					"tool_name":  "run_shell_command",
					"reason":     "needs approval",
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
	bridge.askApproval = func(*runtimechat.ApprovalRequest) (bool, error) { return true, nil }
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

	handler := skillsapi.NewHandler(nil, nil, nil)
	handler.SetSessionManager(remoteManager)
	router := mux.NewRouter()
	router.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
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
