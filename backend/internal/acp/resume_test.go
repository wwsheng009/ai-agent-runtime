package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// resumeBackend extends configOptionBackend with the optional resume/mode
// interfaces so both capability gating and the response payload can be pinned.
type resumeBackend struct {
	*configOptionBackend
	resumeFn func(ctx context.Context, req ResumeSessionRequest) error
	modesFn  func(ctx context.Context, sessionID string) (*SessionModeState, error)
}

func (b *resumeBackend) ResumeSession(ctx context.Context, req ResumeSessionRequest) error {
	if b.resumeFn == nil {
		return nil
	}
	return b.resumeFn(ctx, req)
}

func (b *resumeBackend) SessionModes(ctx context.Context, sessionID string) (*SessionModeState, error) {
	if b.modesFn == nil {
		return nil, nil
	}
	return b.modesFn(ctx, sessionID)
}

func newServerForResumeTest(t *testing.T, backend SessionBackend) (*Server, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	conn := NewConn(strings.NewReader(""), out)
	caps := DefaultAgentCapabilities()
	// Advertise every session-management method the way the aicli host does;
	// effectiveAgentCapabilities must clear exactly the unsupported ones.
	caps.SessionCapabilities.SetList(true)
	caps.SessionCapabilities.SetDelete(true)
	caps.SessionCapabilities.SetResume(true)
	caps.SessionCapabilities.SetClose(true)
	return NewServer(conn, backend, ServerOptions{
		AgentInfo:         Implementation{Name: "aicli", Version: "test"},
		AgentCapabilities: caps,
	}), out
}

func sessionResumeMessage(t *testing.T, params map[string]interface{}) Message {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return Message{JSONRPC: JSONRPCVersion, ID: json.RawMessage(`7`), Method: MethodSessionResume, Params: raw}
}

// TestSessionResumeCapabilityGating pins that session/resume is advertised and
// dispatched only when the backend implements SessionResumer: ACP makes the
// capability an object whose presence is the promise, so promising it without
// an implementation would give clients a method-not-found on reattach.
func TestSessionResumeCapabilityGating(t *testing.T) {
	t.Parallel()

	plain, _ := newServerForResumeTest(t, &fakeBackend{})
	if caps := plain.effectiveAgentCapabilities().SessionCapabilities; caps.SupportsResume() {
		t.Fatalf("resume capability advertised without a SessionResumer backend: %+v", caps)
	}
	if _, rpcErr := plain.handle(context.Background(), sessionResumeMessage(t, map[string]interface{}{
		"sessionId": "sess-1",
		"cwd":       t.TempDir(),
	})); rpcErr == nil || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeMethodNotFound)
	}

	full, _ := newServerForResumeTest(t, &resumeBackend{configOptionBackend: &configOptionBackend{fakeBackend: &fakeBackend{}}})
	caps := full.effectiveAgentCapabilities().SessionCapabilities
	if !caps.SupportsResume() {
		t.Fatalf("resume capability missing for a SessionResumer backend: %+v", caps)
	}
	// ACP v1 spells the advertised method as an object: `"resume":{}`, never
	// `"resume":true` (which strict clients reject).
	encoded, err := json.Marshal(full.effectiveAgentCapabilities())
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	if !strings.Contains(string(encoded), `"resume":{}`) {
		t.Fatalf("sessionCapabilities.resume must serialize as {}: %s", encoded)
	}
}

// TestSessionResumeValidatesRequiredParams covers the one place where the
// resume request is stricter than load: ACP v1 requires cwd and types it as an
// absolute path. Both are rejected before the backend is consulted, so a bad
// request can never reattach a session to the wrong workspace.
func TestSessionResumeValidatesRequiredParams(t *testing.T) {
	t.Parallel()

	calls := 0
	backend := &resumeBackend{
		configOptionBackend: &configOptionBackend{fakeBackend: &fakeBackend{}},
		resumeFn: func(context.Context, ResumeSessionRequest) error {
			calls++
			return nil
		},
	}
	srv, out := newServerForResumeTest(t, backend)

	workspace := t.TempDir()
	cases := map[string]map[string]interface{}{
		"missing sessionId": {"cwd": workspace},
		"blank sessionId":   {"sessionId": "   ", "cwd": workspace},
		"missing cwd":       {"sessionId": "sess-1"},
		"relative cwd":      {"sessionId": "sess-1", "cwd": filepath.Join("relative", "dir")},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := srv.handle(context.Background(), sessionResumeMessage(t, params))
			if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
				t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeInvalidParams)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("backend ResumeSession called %d times for invalid params", calls)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("invalid params must not emit anything: %s", out.String())
	}
}

// TestSessionResumeReattachesWithoutReplay pins the contract that separates
// resume from load: the backend reattaches and stays silent (no history replay,
// no catalog re-emit), while the response carries the state a detaching client
// cannot derive locally (configOptions / modes) and is never JSON null.
func TestSessionResumeReattachesWithoutReplay(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	var gotReq ResumeSessionRequest
	backend := &resumeBackend{
		configOptionBackend: &configOptionBackend{
			fakeBackend: &fakeBackend{},
			optionsFn: func(context.Context, string) ([]SessionConfigOption, error) {
				return testModelConfigOptions(), nil
			},
		},
		resumeFn: func(_ context.Context, req ResumeSessionRequest) error {
			gotReq = req
			return nil
		},
		modesFn: func(context.Context, string) (*SessionModeState, error) {
			return &SessionModeState{CurrentModeID: "default"}, nil
		},
	}
	srv, out := newServerForResumeTest(t, backend)

	result, rpcErr := srv.handle(context.Background(), sessionResumeMessage(t, map[string]interface{}{
		"sessionId": "sess-1",
		"cwd":       workspace,
	}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if gotReq.SessionID != "sess-1" || gotReq.Cwd != workspace {
		t.Fatalf("forwarded request = %+v", gotReq)
	}
	// No replay: resume must not push a single session/update notification.
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("session/resume emitted notifications: %s", out.String())
	}

	resp, ok := result.(ResumeSessionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(resp.ConfigOptions) != 1 || resp.ConfigOptions[0].ID != "model" {
		t.Fatalf("configOptions = %+v", resp.ConfigOptions)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeID != "default" {
		t.Fatalf("modes = %+v", resp.Modes)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.TrimSpace(string(encoded)) == "null" {
		t.Fatal("ResumeSessionResponse must never serialize as null")
	}
}

// TestSessionResumeProviderFailureDoesNotFailAttach keeps config options and
// modes in the "degrade, never fail" class: once the session is reattached a
// missing selector is a UI nicety, and an empty object is still a valid result.
func TestSessionResumeProviderFailureDoesNotFailAttach(t *testing.T) {
	t.Parallel()

	backend := &resumeBackend{
		configOptionBackend: &configOptionBackend{
			fakeBackend: &fakeBackend{},
			optionsFn: func(context.Context, string) ([]SessionConfigOption, error) {
				return nil, fmt.Errorf("catalog unavailable")
			},
		},
		modesFn: func(context.Context, string) (*SessionModeState, error) {
			return nil, fmt.Errorf("mode store unavailable")
		},
	}
	srv, _ := newServerForResumeTest(t, backend)

	result, rpcErr := srv.handle(context.Background(), sessionResumeMessage(t, map[string]interface{}{
		"sessionId": "sess-1",
		"cwd":       t.TempDir(),
	}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	resp, ok := result.(ResumeSessionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.TrimSpace(string(encoded)) != "{}" {
		t.Fatalf("marshaled ResumeSessionResponse = %s, want {}", encoded)
	}
}

// TestSessionResumeNotFoundClassification pins that a missing session is a
// resource-not-found (-32002), not an internal error, so a client can fall back
// to session/load or drop the stale id without blaming the agent.
func TestSessionResumeNotFoundClassification(t *testing.T) {
	t.Parallel()

	backend := &resumeBackend{
		configOptionBackend: &configOptionBackend{fakeBackend: &fakeBackend{}},
		resumeFn: func(context.Context, ResumeSessionRequest) error {
			return NotFound(fmt.Errorf("session %q not found", "sess-gone"))
		},
	}
	srv, _ := newServerForResumeTest(t, backend)

	_, rpcErr := srv.handle(context.Background(), sessionResumeMessage(t, map[string]interface{}{
		"sessionId": "sess-gone",
		"cwd":       t.TempDir(),
	}))
	if rpcErr == nil || rpcErr.Code != CodeResourceNotFound {
		t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeResourceNotFound)
	}
}
