package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// sessionMgmtBackend extends fakeBackend with the optional session-management
// methods so capability gating and dispatch can be tested together.
type sessionMgmtBackend struct {
	*fakeBackend
	listFn   func(ctx context.Context, req SessionListRequest) (SessionListResponse, error)
	deleteFn func(ctx context.Context, req SessionDeleteRequest) error
	closeFn  func(ctx context.Context, req SessionCloseRequest) error
}

func (b *sessionMgmtBackend) ListSessions(ctx context.Context, req SessionListRequest) (SessionListResponse, error) {
	if b.listFn != nil {
		return b.listFn(ctx, req)
	}
	return SessionListResponse{}, nil
}

func (b *sessionMgmtBackend) DeleteSession(ctx context.Context, req SessionDeleteRequest) error {
	if b.deleteFn != nil {
		return b.deleteFn(ctx, req)
	}
	return nil
}

func (b *sessionMgmtBackend) CloseSession(ctx context.Context, req SessionCloseRequest) error {
	if b.closeFn != nil {
		return b.closeFn(ctx, req)
	}
	return nil
}

func newServerForSessionMgmtTest(backend SessionBackend) *Server {
	var out strings.Builder
	conn := NewConn(strings.NewReader(""), &out)
	caps := DefaultAgentCapabilities()
	// Advertise every session-management flag: effectiveAgentCapabilities must
	// clear the ones the backend does not implement.
	caps.SessionCapabilities.SetList(true)
	caps.SessionCapabilities.SetDelete(true)
	caps.SessionCapabilities.SetClose(true)
	return NewServer(conn, backend, ServerOptions{
		AgentInfo:         Implementation{Name: "aicli", Version: "test"},
		AgentCapabilities: caps,
	})
}

func sessionMgmtMessage(t *testing.T, method string, params interface{}) Message {
	t.Helper()
	raw := json.RawMessage(`{}`)
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = encoded
	}
	return Message{JSONRPC: JSONRPCVersion, ID: json.RawMessage(`1`), Method: method, Params: raw}
}

// TestEffectiveAgentCapabilitiesGatesSessionMethods pins the contract that a
// capability flag is only advertised when the backend really implements the
// method, so a client never sees list=true and then gets method-not-found.
func TestEffectiveAgentCapabilitiesGatesSessionMethods(t *testing.T) {
	t.Parallel()

	plain := newServerForSessionMgmtTest(&fakeBackend{})
	caps := plain.effectiveAgentCapabilities()
	if caps.SessionCapabilities == nil {
		t.Fatal("sessionCapabilities must be present in initialize result")
	}
	if caps.SessionCapabilities.SupportsList() || caps.SessionCapabilities.SupportsDelete() || caps.SessionCapabilities.SupportsClose() {
		t.Fatalf("unimplemented session methods must not be advertised: %+v", caps.SessionCapabilities)
	}

	full := newServerForSessionMgmtTest(&sessionMgmtBackend{fakeBackend: &fakeBackend{}})
	caps = full.effectiveAgentCapabilities()
	if !caps.SessionCapabilities.SupportsList() || !caps.SessionCapabilities.SupportsDelete() || !caps.SessionCapabilities.SupportsClose() {
		t.Fatalf("implemented session methods must be advertised: %+v", caps.SessionCapabilities)
	}
	if caps.SessionCapabilities.SupportsResume() {
		t.Fatalf("sessionMgmtBackend does not implement SessionResumer, so resume must stay unadvertised: %+v", caps.SessionCapabilities)
	}
}

// TestServerSessionListWithoutBackendImplementation pins that the method is
// unreachable (-32601) when the backend does not implement SessionLister.
func TestServerSessionListWithoutBackendImplementation(t *testing.T) {
	t.Parallel()

	server := newServerForSessionMgmtTest(&fakeBackend{})
	_, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionList, nil))
	if rpcErr == nil || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("session/list error = %+v, want method-not-found", rpcErr)
	}
}

// TestServerSessionListNormalizesEmptySessions pins the wire shape: the spec
// types sessions as an array, so an empty store must serialize [] not null.
func TestServerSessionListNormalizesEmptySessions(t *testing.T) {
	t.Parallel()

	server := newServerForSessionMgmtTest(&sessionMgmtBackend{fakeBackend: &fakeBackend{}})
	result, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionList, SessionListRequest{}))
	if rpcErr != nil {
		t.Fatalf("session/list error: %+v", rpcErr)
	}
	resp, ok := result.(SessionListResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if resp.Sessions == nil {
		t.Fatal("sessions must be a non-nil empty slice")
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(encoded), `"sessions":[]`) {
		t.Fatalf("encoded response = %s, want sessions:[]", encoded)
	}
}

func TestServerSessionListForwardsCursorAndCwd(t *testing.T) {
	t.Parallel()

	var gotReq SessionListRequest
	backend := &sessionMgmtBackend{
		fakeBackend: &fakeBackend{},
		listFn: func(_ context.Context, req SessionListRequest) (SessionListResponse, error) {
			gotReq = req
			return SessionListResponse{Sessions: []SessionSummary{{SessionID: "sess_1", Cwd: req.Cwd}}}, nil
		},
	}
	server := newServerForSessionMgmtTest(backend)
	if _, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionList, SessionListRequest{Cursor: "50", Cwd: "/repo"})); rpcErr != nil {
		t.Fatalf("session/list error: %+v", rpcErr)
	}
	if gotReq.Cursor != "50" || gotReq.Cwd != "/repo" {
		t.Fatalf("backend got %+v, want cursor=50 cwd=/repo", gotReq)
	}
}

func TestServerSessionDeleteValidatesSessionID(t *testing.T) {
	t.Parallel()

	server := newServerForSessionMgmtTest(&sessionMgmtBackend{fakeBackend: &fakeBackend{}})
	_, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionDelete, SessionDeleteRequest{SessionID: "   "}))
	if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
		t.Fatalf("empty sessionId error = %+v, want invalid-params", rpcErr)
	}
}

func TestServerSessionDeleteForwardsToBackend(t *testing.T) {
	t.Parallel()

	deleted := ""
	backend := &sessionMgmtBackend{
		fakeBackend: &fakeBackend{},
		deleteFn: func(_ context.Context, req SessionDeleteRequest) error {
			deleted = req.SessionID
			return nil
		},
	}
	server := newServerForSessionMgmtTest(backend)
	result, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionDelete, SessionDeleteRequest{SessionID: "sess_1"}))
	if rpcErr != nil {
		t.Fatalf("session/delete error: %+v", rpcErr)
	}
	if deleted != "sess_1" {
		t.Fatalf("backend delete = %q, want sess_1", deleted)
	}
	if _, ok := result.(SessionDeleteResponse); !ok {
		t.Fatalf("result type = %T", result)
	}
}

func TestServerSessionDeletePropagatesBackendError(t *testing.T) {
	t.Parallel()

	backend := &sessionMgmtBackend{
		fakeBackend: &fakeBackend{},
		deleteFn: func(_ context.Context, _ SessionDeleteRequest) error {
			return errors.New("session \"missing\" not found")
		},
	}
	server := newServerForSessionMgmtTest(backend)
	if _, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionDelete, SessionDeleteRequest{SessionID: "missing"})); rpcErr == nil {
		t.Fatal("expected error from session/delete")
	}
}

// TestServerSessionCloseWithoutBackendImplementation pins that close is also
// gated on the backend interface.
func TestServerSessionCloseWithoutBackendImplementation(t *testing.T) {
	t.Parallel()

	server := newServerForSessionMgmtTest(&fakeBackend{})
	_, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionClose, SessionCloseRequest{SessionID: "sess_1"}))
	if rpcErr == nil || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("session/close error = %+v, want method-not-found", rpcErr)
	}
}

func TestServerSessionCloseForwardsToBackend(t *testing.T) {
	t.Parallel()

	closed := ""
	backend := &sessionMgmtBackend{
		fakeBackend: &fakeBackend{},
		closeFn: func(_ context.Context, req SessionCloseRequest) error {
			closed = req.SessionID
			return nil
		},
	}
	server := newServerForSessionMgmtTest(backend)
	if _, rpcErr := server.handle(context.Background(), sessionMgmtMessage(t, MethodSessionClose, SessionCloseRequest{SessionID: "sess_9"})); rpcErr != nil {
		t.Fatalf("session/close error: %+v", rpcErr)
	}
	if closed != "sess_9" {
		t.Fatalf("backend close = %q, want sess_9", closed)
	}
}

// TestSessionUpdateNotificationsWireShape pins the JSON emitted for the three
// notification kinds added for ACP v1 parity.
func TestSessionUpdateNotificationsWireShape(t *testing.T) {
	t.Parallel()

	usage := UsageUpdate(1200, 200000)
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal usage_update: %v", err)
	}
	if !strings.Contains(string(encoded), `"sessionUpdate":"usage_update"`) ||
		!strings.Contains(string(encoded), `"used":1200`) ||
		!strings.Contains(string(encoded), `"size":200000`) {
		t.Fatalf("usage_update wire = %s", encoded)
	}

	info := SessionInfoUpdate("my title", "2026-09-19T00:00:00Z")
	encoded, err = json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal session_info_update: %v", err)
	}
	if !strings.Contains(string(encoded), `"sessionUpdate":"session_info_update"`) ||
		!strings.Contains(string(encoded), `"title":"my title"`) ||
		!strings.Contains(string(encoded), `"updatedAt":"2026-09-19T00:00:00Z"`) {
		t.Fatalf("session_info_update wire = %s", encoded)
	}

	commands := AvailableCommandsUpdate([]AvailableCommand{{Name: "help", Description: "help"}})
	encoded, err = json.Marshal(commands)
	if err != nil {
		t.Fatalf("marshal available_commands_update: %v", err)
	}
	if !strings.Contains(string(encoded), `"sessionUpdate":"available_commands_update"`) ||
		!strings.Contains(string(encoded), `"availableCommands":[{"name":"help"`) {
		t.Fatalf("available_commands_update wire = %s", encoded)
	}

	// An empty catalog must still serialize as [] so clients clear the panel.
	encoded, err = json.Marshal(AvailableCommandsUpdate(nil))
	if err != nil {
		t.Fatalf("marshal empty catalog: %v", err)
	}
	if !strings.Contains(string(encoded), `"availableCommands":[]`) {
		t.Fatalf("empty catalog wire = %s", encoded)
	}
}
