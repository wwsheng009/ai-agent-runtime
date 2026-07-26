package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu sync.Mutex

	newSessionFn func(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error)
	promptFn     func(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error)
	cancelFn     func(ctx context.Context, sessionID string) error

	cancelled []string
}

func (f *fakeBackend) NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	if f.newSessionFn != nil {
		return f.newSessionFn(ctx, req)
	}
	return NewSessionResponse{SessionID: "sess_test"}, nil
}

func (f *fakeBackend) Prompt(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
	if f.promptFn != nil {
		return f.promptFn(ctx, req, emit)
	}
	_ = emit.SessionUpdate(req.SessionID, AgentMessageChunk("hello"))
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

func (f *fakeBackend) Cancel(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, sessionID)
	f.mu.Unlock()
	if f.cancelFn != nil {
		return f.cancelFn(ctx, sessionID)
	}
	return nil
}

func TestServerInitializeSessionPrompt(t *testing.T) {
	t.Parallel()

	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer agentWriter.Close()
	defer agentReader.Close()
	defer clientWriter.Close()

	backend := &fakeBackend{
		promptFn: func(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
			text := ExtractText(req.Prompt)
			if text != "ping" {
				t.Errorf("prompt text = %q, want ping", text)
			}
			_ = emit.SessionUpdate(req.SessionID, AgentMessageChunk("pong"))
			_ = emit.SessionUpdate(req.SessionID, ToolCallStarted("call_1", "Read file", ToolKindRead, map[string]string{"path": "a.go"}))
			_ = emit.SessionUpdate(req.SessionID, ToolCallFinished("call_1", ToolCallStatusCompleted, map[string]string{"ok": "true"}, nil))
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	}

	agentConn := NewConn(agentReader, agentWriter)
	server := NewServer(agentConn, backend, ServerOptions{
		AgentInfo: Implementation{Name: "aicli", Title: "AICLI", Version: "test"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ctx)
	}()

	clientConn := NewConn(clientReader, clientWriter)

	// Drain client-side notifications/requests while we drive the dialogue.
	var (
		updatesMu sync.Mutex
		updates   []SessionUpdateNotification
	)
	clientConn.SetHandler(func(ctx context.Context, msg Message) (interface{}, *RPCError) {
		switch msg.Method {
		case MethodSessionUpdate:
			var note SessionUpdateNotification
			if err := DecodeParams(msg, &note); err != nil {
				return nil, err
			}
			updatesMu.Lock()
			updates = append(updates, note)
			updatesMu.Unlock()
			return nil, nil
		case MethodSessionRequestPermission:
			return RequestPermissionResult{
				Outcome: PermissionOutcome{Outcome: PermissionOutcomeSelected, OptionID: "allow-once"},
			}, nil
		default:
			if msg.IsNotification() {
				return nil, nil
			}
			return nil, NewRPCError(CodeMethodNotFound, msg.Method)
		}
	})
	go func() { _ = clientConn.Serve(context.Background()) }()

	var initResp InitializeResponse
	if err := clientConn.Call(ctx, MethodInitialize, InitializeRequest{
		ProtocolVersion: ProtocolVersion,
		ClientCapabilities: ClientCapabilities{
			FS: &FileSystemCapabilities{ReadTextFile: true},
		},
		ClientInfo: &Implementation{Name: "test-client", Version: "1.0"},
	}, &initResp); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initResp.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocolVersion = %d, want %d", initResp.ProtocolVersion, ProtocolVersion)
	}
	if initResp.AgentInfo == nil || initResp.AgentInfo.Name != "aicli" {
		t.Fatalf("agentInfo = %+v", initResp.AgentInfo)
	}
	if initResp.AgentCapabilities.LoadSession {
		t.Fatal("loadSession should be false in MVP")
	}
	if initResp.AuthMethods == nil {
		t.Fatal("authMethods should be a non-nil empty slice")
	}

	var newResp NewSessionResponse
	if err := clientConn.Call(ctx, MethodSessionNew, NewSessionRequest{Cwd: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if newResp.SessionID != "sess_test" {
		t.Fatalf("sessionId = %q", newResp.SessionID)
	}

	var promptResp PromptResponse
	if err := clientConn.Call(ctx, MethodSessionPrompt, PromptRequest{
		SessionID: newResp.SessionID,
		Prompt:    []ContentBlock{TextContent("ping")},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if promptResp.StopReason != StopReasonEndTurn {
		t.Fatalf("stopReason = %q", promptResp.StopReason)
	}

	// Wait briefly for notifications to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updatesMu.Lock()
		n := len(updates)
		updatesMu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	updatesMu.Lock()
	got := append([]SessionUpdateNotification(nil), updates...)
	updatesMu.Unlock()
	if len(got) < 3 {
		t.Fatalf("expected >=3 session/update notifications, got %d (%+v)", len(got), got)
	}
	foundMessage := false
	foundTool := false
	for _, note := range got {
		if note.SessionID != "sess_test" {
			t.Fatalf("update sessionId = %q", note.SessionID)
		}
		switch note.Update.SessionUpdate {
		case SessionUpdateAgentMessageChunk:
			foundMessage = true
		case SessionUpdateToolCall:
			foundTool = true
			if note.Update.ToolCallID != "call_1" {
				t.Fatalf("toolCallId = %q", note.Update.ToolCallID)
			}
		}
	}
	if !foundMessage || !foundTool {
		t.Fatalf("missing updates: message=%v tool=%v raw=%+v", foundMessage, foundTool, got)
	}

	cancel()
	agentReader.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit")
	}
}

func TestPermissionRoundTrip(t *testing.T) {
	t.Parallel()

	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer agentWriter.Close()
	defer agentReader.Close()
	defer clientWriter.Close()

	var server *Server
	backend := &fakeBackend{
		promptFn: func(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
			requester := server.PermissionRequester()
			result, err := requester.RequestPermission(ctx, RequestPermissionParams{
				SessionID: req.SessionID,
				ToolCall: ToolCallPermission{
					ToolCallID: "call_perm",
					Title:      "Run shell",
					Kind:       ToolKindExecute,
					Status:     ToolCallStatusPending,
				},
				Options: DefaultPermissionOptions(),
			})
			if err != nil {
				return PromptResponse{}, err
			}
			if result.Outcome.Outcome != PermissionOutcomeSelected || result.Outcome.OptionID != "allow-once" {
				t.Errorf("permission outcome = %+v", result.Outcome)
			}
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	}

	agentConn := NewConn(agentReader, agentWriter)
	server = NewServer(agentConn, backend, ServerOptions{
		AgentInfo: Implementation{Name: "aicli", Version: "test"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()

	clientConn := NewConn(clientReader, clientWriter)
	clientConn.SetHandler(func(ctx context.Context, msg Message) (interface{}, *RPCError) {
		if msg.Method == MethodSessionRequestPermission {
			var params RequestPermissionParams
			if err := DecodeParams(msg, &params); err != nil {
				return nil, err
			}
			if params.ToolCall.ToolCallID != "call_perm" {
				t.Errorf("toolCallId = %q", params.ToolCall.ToolCallID)
			}
			if len(params.Options) == 0 {
				t.Error("expected permission options")
			}
			return RequestPermissionResult{
				Outcome: PermissionOutcome{Outcome: PermissionOutcomeSelected, OptionID: "allow-once"},
			}, nil
		}
		if msg.IsNotification() {
			return nil, nil
		}
		return nil, NewRPCError(CodeMethodNotFound, msg.Method)
	})
	go func() { _ = clientConn.Serve(context.Background()) }()

	var initResp InitializeResponse
	if err := clientConn.Call(ctx, MethodInitialize, InitializeRequest{ProtocolVersion: ProtocolVersion}, &initResp); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var newResp NewSessionResponse
	if err := clientConn.Call(ctx, MethodSessionNew, NewSessionRequest{}, &newResp); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	var promptResp PromptResponse
	if err := clientConn.Call(ctx, MethodSessionPrompt, PromptRequest{
		SessionID: newResp.SessionID,
		Prompt:    []ContentBlock{TextContent("need tools")},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if promptResp.StopReason != StopReasonEndTurn {
		t.Fatalf("stopReason = %q", promptResp.StopReason)
	}
}

func TestSessionCancelDuringPrompt(t *testing.T) {
	t.Parallel()

	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer agentWriter.Close()
	defer agentReader.Close()
	defer clientWriter.Close()

	started := make(chan struct{})
	backend := &fakeBackend{
		promptFn: func(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
			close(started)
			select {
			case <-ctx.Done():
				return PromptResponse{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return PromptResponse{StopReason: StopReasonEndTurn}, nil
			}
		},
	}

	agentConn := NewConn(agentReader, agentWriter)
	server := NewServer(agentConn, backend, ServerOptions{AgentInfo: Implementation{Name: "aicli"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()

	clientConn := NewConn(clientReader, clientWriter)
	clientConn.SetHandler(func(ctx context.Context, msg Message) (interface{}, *RPCError) {
		return nil, nil
	})
	go func() { _ = clientConn.Serve(context.Background()) }()

	_ = clientConn.Call(ctx, MethodInitialize, InitializeRequest{ProtocolVersion: ProtocolVersion}, &InitializeResponse{})
	var newResp NewSessionResponse
	_ = clientConn.Call(ctx, MethodSessionNew, NewSessionRequest{}, &newResp)

	promptDone := make(chan PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		var resp PromptResponse
		err := clientConn.Call(ctx, MethodSessionPrompt, PromptRequest{
			SessionID: newResp.SessionID,
			Prompt:    []ContentBlock{TextContent("long")},
		}, &resp)
		if err != nil {
			promptErr <- err
			return
		}
		promptDone <- resp
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not start")
	}

	if err := clientConn.Notify(MethodSessionCancel, CancelNotification{SessionID: newResp.SessionID}); err != nil {
		t.Fatalf("cancel notify: %v", err)
	}

	select {
	case resp := <-promptDone:
		if resp.StopReason != StopReasonCancelled {
			t.Fatalf("stopReason = %q, want cancelled", resp.StopReason)
		}
	case err := <-promptErr:
		t.Fatalf("prompt error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not complete after cancel")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.cancelled) != 1 || backend.cancelled[0] != newResp.SessionID {
		t.Fatalf("cancelled = %v", backend.cancelled)
	}
}

func TestExtractText(t *testing.T) {
	t.Parallel()
	got := ExtractText([]ContentBlock{
		TextContent("hello"),
		{Type: "resource_link", Name: "main.go", URI: "file:///main.go"},
		{Type: "resource", Resource: json.RawMessage(`{"text":"body","uri":"file:///x"}`)},
	})
	if !strings.Contains(got, "hello") || !strings.Contains(got, "main.go") || !strings.Contains(got, "body") {
		t.Fatalf("ExtractText = %q", got)
	}
}

func TestMapToolKind(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"read":    ToolKindRead,
		"search":  ToolKindSearch,
		"edit":    ToolKindEdit,
		"exec":    ToolKindExecute,
		"network": ToolKindFetch,
		"other":   ToolKindOther,
		"":        ToolKindOther,
	}
	for in, want := range cases {
		if got := MapToolKind(in); got != want {
			t.Fatalf("MapToolKind(%q)=%q want %q", in, got, want)
		}
	}
}

func TestConnRejectsUnknownMethod(t *testing.T) {
	t.Parallel()
	var in, out bytes.Buffer
	// Preload one request line.
	req := Message{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "nope/method",
		Params:  json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(req)
	in.Write(append(data, '\n'))

	conn := NewConn(&in, &out)
	server := NewServer(conn, &fakeBackend{}, ServerOptions{AgentInfo: Implementation{Name: "aicli"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Serve will block on next Read; close by cancelling after a tick via EOF on empty.
	// Use a reader that EOFs after the one line — bytes.Buffer returns EOF after content.
	_ = server.Serve(ctx)

	var resp Message
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode response: %v\nraw=%s", err, out.String())
	}
	if resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected method not found, got %+v", resp)
	}
}

func TestIsAllowOption(t *testing.T) {
	t.Parallel()
	if !IsAllowOption("allow-once") || !IsAllowOption("allow-always") {
		t.Fatal("allow options should be allowed")
	}
	if IsAllowOption("reject-once") || IsAllowOption("") {
		t.Fatal("reject/empty should not be allowed")
	}
	if !IsRememberOption("allow-always") || IsRememberOption("allow-once") {
		t.Fatal("remember mapping incorrect")
	}
}
