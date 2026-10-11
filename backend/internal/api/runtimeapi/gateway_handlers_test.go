package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeGatewayController struct {
	cfg        gatewayagent.Config
	status     gatewayagent.Status
	connects   int
	disconnect int
	reconnect  int
	err        error
}

func (f *fakeGatewayController) Status() gatewayagent.Status { return f.status }
func (f *fakeGatewayController) ConnectManual(context.Context) error {
	f.connects++
	return f.err
}
func (f *fakeGatewayController) Disconnect(context.Context) error {
	f.disconnect++
	return f.err
}
func (f *fakeGatewayController) Reconnect(context.Context) error {
	f.reconnect++
	return f.err
}
func (f *fakeGatewayController) Config() gatewayagent.Config { return f.cfg }
func (f *fakeGatewayController) Configure(cfg gatewayagent.Config) error {
	f.cfg = cfg
	return f.err
}

func newGatewayTestRouter(controller GatewayController) *mux.Router {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	if controller != nil {
		handler.SetGatewayController(controller)
	}
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

func doGatewayRequest(router *mux.Router, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGatewayStatusEndpoint(t *testing.T) {
	controller := &fakeGatewayController{status: gatewayagent.Status{State: gatewayagent.StateConnected, NodeID: "node-1"}}
	router := newGatewayTestRouter(controller)

	rec := doGatewayRequest(router, http.MethodGet, "/api/runtime/gateway/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status gatewayagent.Status `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Status.State != gatewayagent.StateConnected || payload.Status.NodeID != "node-1" {
		t.Fatalf("unexpected status payload: %+v", payload.Status)
	}
}

func TestGatewayStatusNotConfigured(t *testing.T) {
	router := newGatewayTestRouter(nil)
	rec := doGatewayRequest(router, http.MethodGet, "/api/runtime/gateway/status", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want 503", rec.Code)
	}
}

func TestGatewayConnectEndpoint(t *testing.T) {
	controller := &fakeGatewayController{}
	router := newGatewayTestRouter(controller)
	rec := doGatewayRequest(router, http.MethodPost, "/api/runtime/gateway/connect", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if controller.connects != 1 {
		t.Fatalf("ConnectManual calls = %d, want 1", controller.connects)
	}
}

func TestGatewayDisconnectAndReconnectEndpoints(t *testing.T) {
	controller := &fakeGatewayController{}
	router := newGatewayTestRouter(controller)

	if rec := doGatewayRequest(router, http.MethodPost, "/api/runtime/gateway/disconnect", ""); rec.Code != http.StatusOK {
		t.Fatalf("disconnect status = %d", rec.Code)
	}
	if rec := doGatewayRequest(router, http.MethodPost, "/api/runtime/gateway/reconnect", ""); rec.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d", rec.Code)
	}
	if controller.disconnect != 1 || controller.reconnect != 1 {
		t.Fatalf("disconnect=%d reconnect=%d, want 1/1", controller.disconnect, controller.reconnect)
	}
}

func TestGatewayActionFailureReturnsBadGateway(t *testing.T) {
	controller := &fakeGatewayController{err: context.DeadlineExceeded}
	router := newGatewayTestRouter(controller)
	rec := doGatewayRequest(router, http.MethodPost, "/api/runtime/gateway/connect", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want 502", rec.Code)
	}
}

func TestGatewayConfigGetHidesToken(t *testing.T) {
	controller := &fakeGatewayController{cfg: gatewayagent.Config{Enabled: true, URL: "wss://gw", Token: "agw_secretvalue"}}
	router := newGatewayTestRouter(controller)

	rec := doGatewayRequest(router, http.MethodGet, "/api/runtime/gateway/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "agw_secretvalue") {
		t.Fatalf("response must not contain the token: %s", body)
	}
	if !strings.Contains(body, "agw_…") {
		t.Fatalf("response must contain the token hint: %s", body)
	}
}

func TestGatewayConfigUpdate(t *testing.T) {
	controller := &fakeGatewayController{cfg: gatewayagent.Config{Enabled: false}}
	router := newGatewayTestRouter(controller)

	rec := doGatewayRequest(router, http.MethodPut, "/api/runtime/gateway/config",
		`{"url":"wss://gw.example.com","token":"agw_x","name":"laptop","auto_connect":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := controller.Config()
	if got.URL != "wss://gw.example.com" || got.Name != "laptop" || got.Token != "agw_x" {
		t.Fatalf("config not applied: %+v", got)
	}
	if !got.Enabled {
		t.Fatal("url+token should mark the config enabled")
	}
	if got.AutoConnect {
		t.Fatal("auto_connect=false must be applied")
	}
}

func TestGatewayConfigUpdateKeepsTokenWhenOmitted(t *testing.T) {
	controller := &fakeGatewayController{cfg: gatewayagent.Config{Enabled: true, URL: "wss://gw", Token: "agw_keep"}}
	router := newGatewayTestRouter(controller)

	rec := doGatewayRequest(router, http.MethodPut, "/api/runtime/gateway/config", `{"name":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	got := controller.Config()
	if got.Token != "agw_keep" {
		t.Fatalf("token must be preserved when omitted, got %q", got.Token)
	}
	if got.Name != "renamed" {
		t.Fatalf("name not updated: %q", got.Name)
	}
}
