package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// windowAPIForTest 装配一条真实路由（含 mux 变量注入），只把「节点在线」这一件事
// 用注册表满足：队列要求活连接，所以这里直接往服务里塞一条会话。
func windowAPIForTest(t *testing.T) (*tunnel.Server, *audit.Log, http.HandlerFunc) {
	t.Helper()
	server := tunnel.NewServer(tunnel.Config{})
	log := audit.New(10, "")
	handler := handleNodeWindowAction(server, log, func(*http.Request) string { return "10.0.0.9" })
	return server, log, handler
}

func windowRequest(method, nodeID, sessionID, action string) *http.Request {
	vars := map[string]string{"node_id": nodeID}
	path := "/api/nodes/" + nodeID + "/window"
	if sessionID != "" {
		vars["session_id"] = sessionID
		path = "/api/nodes/" + nodeID + "/sessions/" + sessionID + "/window"
	}
	query := ""
	if action != "" {
		query = "?action=" + action
	}
	req := httptest.NewRequest(method, path+query, nil)
	return mux.SetURLVars(req, vars)
}

func decodeWindowBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// TestWindowAPIAuditsRefusedAction 锁住「被拒也要留痕」：202 的正向路径由
// tunnel 包内测试覆盖（那里能注入活连接），HTTP 层在这里能验证的是——离线节点的
// 请求既要给出 409，也要在审计里留下同一条动作，否则排查不到「谁点过」。
func TestWindowAPIAuditsRefusedAction(t *testing.T) {
	_, log, handler := windowAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, windowRequest(http.MethodPost, "node-offline", "session_1", "open-terminal"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	events := log.Entries(10)
	if len(events) == 0 {
		t.Fatal("refused action must still be audited")
	}
	last := events[len(events)-1]
	if last.Type != audit.TypeWindowAction {
		t.Fatalf("audit type = %q, want %q", last.Type, audit.TypeWindowAction)
	}
	if !strings.Contains(last.Detail, "open-terminal") || !strings.Contains(last.Detail, "session_1") {
		t.Fatalf("audit detail = %q, want node+kind+session", last.Detail)
	}
	if !strings.Contains(last.Detail, "refused:") {
		t.Fatalf("audit detail = %q, want the refusal reason", last.Detail)
	}
	if last.IP != "10.0.0.9" {
		t.Fatalf("audit ip = %q", last.IP)
	}
}

func TestWindowAPIRefusesUnknownAction(t *testing.T) {
	_, _, handler := windowAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, windowRequest(http.MethodPost, "node-a", "session_1", "explode"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(decodeWindowBody(t, rec)["error"].(string), "unknown window action") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWindowAPIRefusesOfflineNodeWithConflict(t *testing.T) {
	_, _, handler := windowAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, windowRequest(http.MethodPost, "node-gone", "session_1", "close"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

func TestWindowAPIRouteWithoutSessionOnlyAcceptsNew(t *testing.T) {
	_, _, handler := windowAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, windowRequest(http.MethodPost, "node-a", "", "close"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400 (no session on this route)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "action=new") {
		t.Fatalf("body = %s, want a pointer to action=new", rec.Body.String())
	}
}

func TestWindowAPIRejectsNonPost(t *testing.T) {
	_, _, handler := windowAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, windowRequest(http.MethodGet, "node-a", "session_1", "close"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
