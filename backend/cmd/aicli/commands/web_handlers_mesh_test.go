package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// GET /web/api/health — 网格存活探针（架构 §5.2）。
//
// 门禁：不依赖会话与渲染器，无会话时同样 200 且字段齐全。

func decodeChatWebHealth(t *testing.T, body []byte) (map[string]any, ChatWebAPIHealthResponse) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, string(body))
	}
	var typed ChatWebAPIHealthResponse
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("响应无法解码为 health 结构: %v", err)
	}
	return raw, typed
}

func TestHandleChatWebAPIHealth_NoSessionNoMesh(t *testing.T) {
	prevHost := mesh.Current()
	mesh.SetCurrent(nil)
	defer mesh.SetCurrent(prevHost)

	prevProvider := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = prevProvider }()

	rec := httptest.NewRecorder()
	HandleChatWebAPIHealth(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIHealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("Content-Type 缺失")
	}
	raw, typed := decodeChatWebHealth(t, rec.Body.Bytes())
	for _, key := range []string{
		"available", "pid", "uptime_sec", "session_active", "busy", "mesh_ready",
	} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("字段 %q 缺失: %s", key, rec.Body.String())
		}
	}
	if !typed.Available {
		t.Fatal("available 必须为 true")
	}
	if typed.PID <= 0 {
		t.Fatalf("pid = %d, want > 0", typed.PID)
	}
	if typed.UptimeSec < 0 {
		t.Fatalf("uptime_sec = %d, want >= 0", typed.UptimeSec)
	}
	if typed.SessionActive || typed.Busy || typed.MeshReady {
		t.Fatalf("无会话/无网格时应全为 false: %+v", typed)
	}
	if typed.NodeID != "" {
		t.Fatalf("网格关闭时不应有 node_id: %q", typed.NodeID)
	}
}

func TestHandleChatWebAPIHealth_MeshSession(t *testing.T) {
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	host := mesh.NewHost(mesh.HostConfig{Warn: func(string, ...any) {}})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	defer host.Close()

	prevHost := mesh.Current()
	mesh.SetCurrent(host)
	defer mesh.SetCurrent(prevHost)

	prevProvider := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = prevProvider }()

	host.SetSession(&mesh.SessionInfo{ID: "session_health", Title: "health"})
	host.SetBusy(true, "turn-0001")

	rec := httptest.NewRecorder()
	HandleChatWebAPIHealth(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIHealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	_, typed := decodeChatWebHealth(t, rec.Body.Bytes())
	if typed.NodeID != host.NodeID() || typed.NodeID == "" {
		t.Fatalf("node_id = %q, want %q", typed.NodeID, host.NodeID())
	}
	if !typed.MeshReady {
		t.Fatal("网格启用时 mesh_ready 必须为 true")
	}
	if !typed.SessionActive || !typed.Busy {
		t.Fatalf("会话/忙碌应来自网格快照: %+v", typed)
	}
}
