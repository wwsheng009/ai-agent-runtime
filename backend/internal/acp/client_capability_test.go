package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// booleanTestConfigOptions returns one baseline select option plus one boolean
// option, i.e. the exact mix that the client capability gates.
func booleanTestConfigOptions() []SessionConfigOption {
	return []SessionConfigOption{
		{
			ID:           "model",
			Name:         "Model",
			Type:         SessionConfigOptionTypeSelect,
			CurrentValue: "m1",
			Options:      []SessionConfigSelectOption{{Value: "m1", Name: "m1"}},
		},
		{
			ID:           "verbose",
			Name:         "Verbose",
			Type:         SessionConfigOptionTypeBoolean,
			CurrentValue: "true",
		},
	}
}

// newServerWithClientCaps builds a server that has completed initialize with
// the given clientCapabilities payload (nil means "omitted entirely").
func newServerWithClientCaps(t *testing.T, clientCaps map[string]interface{}) (*Server, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	conn := NewConn(strings.NewReader(""), out)
	srv := NewServer(conn, &fakeBackend{}, ServerOptions{
		AgentInfo:         Implementation{Name: "aicli", Version: "test"},
		AgentCapabilities: DefaultAgentCapabilities(),
	})
	params := map[string]interface{}{"protocolVersion": ProtocolVersion}
	if clientCaps != nil {
		params["clientCapabilities"] = clientCaps
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	if _, rpcErr := srv.handle(context.Background(), Message{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  MethodInitialize,
		Params:  raw,
	}); rpcErr != nil {
		t.Fatalf("initialize: %+v", rpcErr)
	}
	out.Reset()
	return srv, out
}

func sessionConfigOptionsCapability(boolean bool) map[string]interface{} {
	configOptions := map[string]interface{}{}
	if boolean {
		configOptions["boolean"] = map[string]interface{}{}
	}
	return map[string]interface{}{"session": map[string]interface{}{"configOptions": configOptions}}
}

// TestClientCapabilitiesSupportsBooleanConfigOptions pins the exact v1 shape:
// support exists only when session.configOptions.boolean is present and
// non-null. Omitted or null at any level means the kind is unsupported.
func TestClientCapabilitiesSupportsBooleanConfigOptions(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw  string
		want bool
	}{
		"no capabilities at all":       {`{}`, false},
		"session omitted":              {`{"terminal":true}`, false},
		"empty session object":         {`{"session":{}}`, false},
		"empty configOptions object":   {`{"session":{"configOptions":{}}}`, false},
		"boolean null":                 {`{"session":{"configOptions":{"boolean":null}}}`, false},
		"boolean empty object":         {`{"session":{"configOptions":{"boolean":{}}}}`, true},
		"boolean object with metadata": {`{"session":{"configOptions":{"boolean":{}},"_meta":{"x":1}}}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var caps ClientCapabilities
			if err := json.Unmarshal([]byte(tc.raw), &caps); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if got := caps.SupportsBooleanConfigOptions(); got != tc.want {
				t.Fatalf("SupportsBooleanConfigOptions() = %v, want %v (%s)", got, tc.want, tc.raw)
			}
		})
	}
}

// TestSessionEmitterFiltersBooleanConfigOptions pins that an out-of-band
// config_option_update never carries a boolean option to a client that did not
// advertise session.configOptions.boolean, while clients that opted in receive
// the full set unchanged.
func TestSessionEmitterFiltersBooleanConfigOptions(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		caps     map[string]interface{}
		wantBool bool
	}{
		"capabilities omitted":   {nil, false},
		"select-only client":     {sessionConfigOptionsCapability(false), false},
		"boolean-capable client": {sessionConfigOptionsCapability(true), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, out := newServerWithClientCaps(t, tc.caps)
			if err := srv.SessionEmitter().SessionUpdate("sess-1", ConfigOptionUpdate(booleanTestConfigOptions())); err != nil {
				t.Fatalf("emit config option update: %v", err)
			}
			text := out.String()
			if !strings.Contains(text, `"id":"model"`) {
				t.Fatalf("baseline select option must always be delivered: %s", text)
			}
			hasBool := strings.Contains(text, `"id":"verbose"`)
			if hasBool != tc.wantBool {
				t.Fatalf("boolean option delivered = %v, want %v (%s)", hasBool, tc.wantBool, text)
			}
			if !tc.wantBool && strings.Contains(text, `"type":"boolean"`) {
				t.Fatalf("boolean type leaked to a client without the capability: %s", text)
			}
		})
	}
}

// TestSessionEmitterDropsAllBooleanUpdate pins the safety valve: when every
// option in the update is boolean and the client cannot render them, sending an
// empty set would wipe the catalog the client already renders, so the update is
// dropped entirely.
func TestSessionEmitterDropsAllBooleanUpdate(t *testing.T) {
	t.Parallel()

	srv, out := newServerWithClientCaps(t, sessionConfigOptionsCapability(false))
	onlyBoolean := []SessionConfigOption{{
		ID:           "verbose",
		Name:         "Verbose",
		Type:         SessionConfigOptionTypeBoolean,
		CurrentValue: "true",
	}}
	if err := srv.SessionEmitter().SessionUpdate("sess-1", ConfigOptionUpdate(onlyBoolean)); err != nil {
		t.Fatalf("emit config option update: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("all-boolean update must be dropped, got: %s", out.String())
	}
}

// TestSessionNewFiltersBooleanConfigOptions pins the same rule on the
// request/response path: session/new must not advertise an option kind the
// client never claimed to render.
func TestSessionNewFiltersBooleanConfigOptions(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		caps     map[string]interface{}
		wantBool bool
	}{
		"select-only client":     {sessionConfigOptionsCapability(false), false},
		"boolean-capable client": {sessionConfigOptionsCapability(true), true},
	} {
		t.Run(name, func(t *testing.T) {
			out := &strings.Builder{}
			conn := NewConn(strings.NewReader(""), out)
			backend := &fakeBackend{
				newSessionFn: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
					return NewSessionResponse{
						SessionID:     "sess-1",
						ConfigOptions: booleanTestConfigOptions(),
					}, nil
				},
			}
			srv := NewServer(conn, backend, ServerOptions{
				AgentInfo:         Implementation{Name: "aicli", Version: "test"},
				AgentCapabilities: DefaultAgentCapabilities(),
			})
			params := map[string]interface{}{"protocolVersion": ProtocolVersion, "clientCapabilities": tc.caps}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal initialize params: %v", err)
			}
			if _, rpcErr := srv.handle(context.Background(), Message{
				JSONRPC: JSONRPCVersion, ID: json.RawMessage(`1`), Method: MethodInitialize, Params: raw,
			}); rpcErr != nil {
				t.Fatalf("initialize: %+v", rpcErr)
			}
			result, rpcErr := srv.handle(context.Background(), sessionMgmtMessage(t, MethodSessionNew, map[string]interface{}{}))
			if rpcErr != nil {
				t.Fatalf("session/new: %+v", rpcErr)
			}
			resp, ok := result.(NewSessionResponse)
			if !ok {
				t.Fatalf("result type = %T", result)
			}
			wantLen := 1
			if tc.wantBool {
				wantLen = 2
			}
			if len(resp.ConfigOptions) != wantLen {
				t.Fatalf("configOptions = %+v, want %d option(s)", resp.ConfigOptions, wantLen)
			}
			if resp.ConfigOptions[0].ID != "model" {
				t.Fatalf("baseline select option must survive filtering: %+v", resp.ConfigOptions)
			}
			if tc.wantBool && resp.ConfigOptions[1].ID != "verbose" {
				t.Fatalf("boolean-capable client must receive the boolean option: %+v", resp.ConfigOptions)
			}
		})
	}
}
