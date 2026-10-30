package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestNodeConnectorViewReadOnlyAndPathScoped(t *testing.T) {
	handler := handleNodeConnectors(tunnel.NewServer(tunnel.Config{ServerVersion: "test"}))

	request := httptest.NewRequest(http.MethodPost, "/api/nodes/node-a/connectors", strings.NewReader("{}"))
	request = mux.SetURLVars(request, map[string]string{"node_id": "node-a"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", recorder.Code)
	}

	for _, path := range []string{
		"/api/nodes/node-a/connectors/../secret",
		"/api/nodes/node-a/connectors/%2e%2e/secret",
		"/api/nodes/node-a/connectorsX",
	} {
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request = mux.SetURLVars(request, map[string]string{"node_id": "node-a"})
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, recorder.Code)
		}
	}
}

func TestNodeConnectorRoutePatternsCarryNodeID(t *testing.T) {
	router := mux.NewRouter()
	var gotNode, gotPath string
	view := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNode = mux.Vars(r)["node_id"]
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	router.Handle("/api/nodes/{node_id}/connectors", view).Methods(http.MethodGet)
	router.PathPrefix("/api/nodes/{node_id}/connectors/").Handler(view).Methods(http.MethodGet)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/api/nodes/node-a/connectors", "/api/nodes/node-a/connectors"},
		{"/api/nodes/node-a/connectors/schema", "/api/nodes/node-a/connectors/schema"},
		{"/api/nodes/node-a/connectors/wechat-1", "/api/nodes/node-a/connectors/wechat-1"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", tc.path, recorder.Code)
		}
		if gotNode != "node-a" || gotPath != tc.want {
			t.Fatalf("%s vars/path = %q/%q, want node-a/%q", tc.path, gotNode, gotPath, tc.want)
		}
	}
}

func TestNodeConnectorViewRewritesToFixedConnectorNamespace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	connector := &agent.WSConnector{
		AgentVersion: "test",
		Register: func() wire.Register {
			return wire.Register{
				NodeID: "node-a",
				Kind:   "aicli",
				Routes: []string{"/web"},
			}
		},
		Forward: agent.ForwardConfig{LocalBaseURL: upstream.URL},
	}
	conn, err := connector.Dial(context.Background(), agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := server.Registry().Get("node-a"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("node-a did not register")
		}
		time.Sleep(10 * time.Millisecond)
	}

	handler := handleNodeConnectors(server)
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/api/nodes/node-a/connectors", "/api/connectors"},
		{"/api/nodes/node-a/connectors/schema", "/api/connectors/schema"},
		{"/api/nodes/node-a/connectors/wechat-1", "/api/connectors/wechat-1"},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		request = mux.SetURLVars(request, map[string]string{"node_id": "node-a"})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", tc.path, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Body.String(); got != tc.want {
			t.Fatalf("%s target = %q, want %q", tc.path, got, tc.want)
		}
	}
}
