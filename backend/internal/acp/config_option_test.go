package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// configOptionBackend extends fakeBackend with the optional config-option
// interfaces (session/set_config_option + session/load options).
type configOptionBackend struct {
	*fakeBackend
	setFn     func(ctx context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
	optionsFn func(ctx context.Context, sessionID string) ([]SessionConfigOption, error)
}

func (b *configOptionBackend) SetSessionConfigOption(ctx context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	if b.setFn == nil {
		return SetSessionConfigOptionResponse{}, nil
	}
	return b.setFn(ctx, req)
}

func (b *configOptionBackend) SessionConfigOptions(ctx context.Context, sessionID string) ([]SessionConfigOption, error) {
	if b.optionsFn == nil {
		return nil, nil
	}
	return b.optionsFn(ctx, sessionID)
}

func newServerForConfigOptionTest(t *testing.T, backend SessionBackend) *Server {
	t.Helper()
	var out strings.Builder
	conn := NewConn(strings.NewReader(""), &out)
	return NewServer(conn, backend, ServerOptions{
		AgentInfo:         Implementation{Name: "aicli", Version: "test"},
		AgentCapabilities: DefaultAgentCapabilities(),
	})
}

func configOptionParams(t *testing.T, sessionID, configID string, value interface{}) Message {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     value,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return Message{JSONRPC: JSONRPCVersion, ID: json.RawMessage(`1`), Method: MethodSessionSetConfigOption, Params: raw}
}

func TestServerSessionSetConfigOption_NormalizesNilOptions(t *testing.T) {
	var gotReq SetSessionConfigOptionRequest
	backend := &configOptionBackend{
		fakeBackend: &fakeBackend{},
		setFn: func(_ context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
			gotReq = req
			return SetSessionConfigOptionResponse{}, nil
		},
	}
	srv := newServerForConfigOptionTest(t, backend)

	result, rpcErr := srv.handleSessionSetConfigOption(context.Background(), configOptionParams(t, "sess-1", "model", "m2"))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if gotReq.SessionID != "sess-1" || gotReq.ConfigID != "model" {
		t.Fatalf("forwarded request = %+v", gotReq)
	}
	if modelID, ok := gotReq.Value.ValueID(); !ok || modelID != "m2" {
		t.Fatalf("value id = %q ok=%v", modelID, ok)
	}
	resp, ok := result.(SetSessionConfigOptionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if resp.ConfigOptions == nil {
		t.Fatal("configOptions must serialize as an array, never null")
	}
	if len(resp.ConfigOptions) != 0 {
		t.Fatalf("configOptions = %+v, want empty", resp.ConfigOptions)
	}
}

func TestServerSessionSetConfigOption_InvalidParamsClassification(t *testing.T) {
	backend := &configOptionBackend{
		fakeBackend: &fakeBackend{},
		setFn: func(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
			return SetSessionConfigOptionResponse{}, InvalidParams(fmt.Errorf("unknown configId %q", "nope"))
		},
	}
	srv := newServerForConfigOptionTest(t, backend)
	_, rpcErr := srv.handleSessionSetConfigOption(context.Background(), configOptionParams(t, "sess-1", "model", "m2"))
	if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
		t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeInvalidParams)
	}
}

func TestServerSessionSetConfigOption_InternalErrorClassification(t *testing.T) {
	backend := &configOptionBackend{
		fakeBackend: &fakeBackend{},
		setFn: func(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
			return SetSessionConfigOptionResponse{}, fmt.Errorf("boom")
		},
	}
	srv := newServerForConfigOptionTest(t, backend)
	_, rpcErr := srv.handleSessionSetConfigOption(context.Background(), configOptionParams(t, "sess-1", "model", "m2"))
	if rpcErr == nil || rpcErr.Code != CodeInternalError {
		t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeInternalError)
	}
}

func TestServerSessionSetConfigOption_MethodNotFoundWithoutSetter(t *testing.T) {
	srv := newServerForConfigOptionTest(t, &fakeBackend{})
	_, rpcErr := srv.handleSessionSetConfigOption(context.Background(), configOptionParams(t, "sess-1", "model", "m2"))
	if rpcErr == nil || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeMethodNotFound)
	}
}

func TestServerSessionSetConfigOption_ValidatesRequiredFields(t *testing.T) {
	srv := newServerForConfigOptionTest(t, &configOptionBackend{fakeBackend: &fakeBackend{}})
	cases := map[string]Message{
		"missing sessionId": configOptionParams(t, "  ", "model", "m2"),
		"missing configId":  configOptionParams(t, "sess-1", " ", "m2"),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := srv.handleSessionSetConfigOption(context.Background(), msg)
			if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
				t.Fatalf("rpcErr = %+v, want code %d", rpcErr, CodeInvalidParams)
			}
		})
	}
}

func testModelConfigOptions() []SessionConfigOption {
	return []SessionConfigOption{{
		ID:           "model",
		Name:         "Model",
		Category:     SessionConfigOptionCategoryModel,
		Type:         SessionConfigOptionTypeSelect,
		CurrentValue: "m1",
		Options: []SessionConfigSelectOption{
			{Value: "m1", Name: "m1"},
			{Value: "m2", Name: "m2"},
		},
	}}
}

func TestServerSessionLoad_AttachesConfigOptions(t *testing.T) {
	loaded := false
	options := testModelConfigOptions()
	backend := &configOptionBackend{
		fakeBackend: &fakeBackend{
			loadSessionFn: func(context.Context, LoadSessionRequest, Emitter) error {
				loaded = true
				return nil
			},
		},
		optionsFn: func(context.Context, string) ([]SessionConfigOption, error) {
			return options, nil
		},
	}
	srv := newServerForConfigOptionTest(t, backend)

	raw, err := json.Marshal(map[string]string{"sessionId": "sess-1"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, rpcErr := srv.handleSessionLoad(context.Background(), Message{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(`2`),
		Method:  MethodSessionLoad,
		Params:  raw,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !loaded {
		t.Fatal("backend LoadSession was not called")
	}
	resp, ok := result.(LoadSessionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(resp.ConfigOptions) != 1 || resp.ConfigOptions[0].ID != "model" {
		t.Fatalf("configOptions = %+v", resp.ConfigOptions)
	}
	if resp.ConfigOptions[0].CurrentValue != "m1" {
		t.Fatalf("currentValue = %q", resp.ConfigOptions[0].CurrentValue)
	}
}

func TestServerSessionLoad_ProviderFailureDoesNotFailLoad(t *testing.T) {
	backend := &configOptionBackend{
		fakeBackend: &fakeBackend{
			loadSessionFn: func(context.Context, LoadSessionRequest, Emitter) error { return nil },
		},
		optionsFn: func(context.Context, string) ([]SessionConfigOption, error) {
			return nil, fmt.Errorf("catalog unavailable")
		},
	}
	srv := newServerForConfigOptionTest(t, backend)

	raw, err := json.Marshal(map[string]string{"sessionId": "sess-1"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, rpcErr := srv.handleSessionLoad(context.Background(), Message{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(`3`),
		Method:  MethodSessionLoad,
		Params:  raw,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	resp, ok := result.(LoadSessionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(resp.ConfigOptions) != 0 {
		t.Fatalf("configOptions = %+v, want none", resp.ConfigOptions)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.TrimSpace(string(encoded)) != "{}" {
		t.Fatalf("marshaled LoadSessionResponse = %s, want {}", encoded)
	}
}

func TestSessionConfigOptionValue_Unmarshal(t *testing.T) {
	t.Run("scalar value id", func(t *testing.T) {
		var value SessionConfigOptionValue
		if err := json.Unmarshal([]byte(`"m2"`), &value); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if id, ok := value.ValueID(); !ok || id != "m2" {
			t.Fatalf("value id = %q ok=%v", id, ok)
		}
		if _, ok := value.Boolean(); ok {
			t.Fatal("scalar string must not decode as boolean")
		}
	})
	t.Run("scalar boolean", func(t *testing.T) {
		var value SessionConfigOptionValue
		if err := json.Unmarshal([]byte(`true`), &value); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if b, ok := value.Boolean(); !ok || !b {
			t.Fatalf("boolean = %v ok=%v", b, ok)
		}
		if _, ok := value.ValueID(); ok {
			t.Fatal("boolean must not decode as value id")
		}
	})
	t.Run("tagged object", func(t *testing.T) {
		var value SessionConfigOptionValue
		if err := json.Unmarshal([]byte(`{"type":"value_id","value":"m3"}`), &value); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if id, ok := value.ValueID(); !ok || id != "m3" {
			t.Fatalf("value id = %q ok=%v", id, ok)
		}
	})
}

func TestSessionUpdateConfigOptionUpdate_Marshal(t *testing.T) {
	update := SessionUpdate{
		SessionUpdate: SessionUpdateConfigOptionUpdate,
		ConfigOptions: testModelConfigOptions(),
	}
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"sessionUpdate":"config_option_update"`) {
		t.Fatalf("missing sessionUpdate discriminator: %s", text)
	}
	if !strings.Contains(text, `"configOptions":[`) {
		t.Fatalf("missing configOptions array: %s", text)
	}
	if !strings.Contains(text, `"category":"model"`) {
		t.Fatalf("missing model category: %s", text)
	}

	empty, err := json.Marshal(SessionUpdate{SessionUpdate: SessionUpdateConfigOptionUpdate})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if !strings.Contains(string(empty), `"configOptions":[]`) {
		t.Fatalf("empty configOptions must serialize as [], got %s", empty)
	}
}
