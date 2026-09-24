package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// POST /web/api/mesh/stop —— 被调方一侧（架构 §5.7 / §9.2，实施方案 S16）。
//
// 门禁与契约：
//   - 默认关闭：--mesh-allow-stop=false → 403 + mesh_stop_not_allowed，且不执行；
//   - 非回环一律拒绝 → 403 + mesh_nonloopback_denied（治理动作属写路径，§5.8）；
//   - 网格关闭 → 403 + mesh_disabled（降级不 panic，§4.7）；
//   - target 必填、mode 只认 graceful|force → 400；
//   - 自停拒绝（本进程 / 指名调用方）→ 403 + mesh_stop_self_refused；
//   - 状态映射：stopped → 200，refused → 403，not_found → 404，timeout → 504。
//
// 真正的进程终止由 internal/mesh 的 stop 测试用真实靶进程覆盖；这里通过
// chatWebMeshStopFn 替换执行体，验证 HTTP 层的门禁顺序、参数翻译与状态映射。

// meshStopRequestBody 序列化一次停止的请求体。
func meshStopRequestBody(t *testing.T, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return payload
}

// meshStopRequest 构造一个「来自回环」的 POST /web/api/mesh/stop 请求。
func meshStopRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, ChatWebAPIMeshStopPath, bytes.NewReader(meshStopRequestBody(t, body)))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8899"
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeMeshStopResponse 解析停止响应。
func decodeMeshStopResponse(t *testing.T, rec *httptest.ResponseRecorder) meshStopEnvelope {
	t.Helper()
	var resp meshStopEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// stubMeshStop 把停止执行替换为 fn（生产路径是 mesh.StopNode）。
func stubMeshStop(t *testing.T, fn func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult) {
	t.Helper()
	prev := chatWebMeshStopFn
	chatWebMeshStopFn = fn
	t.Cleanup(func() { chatWebMeshStopFn = prev })
}

// enableMeshStop 打开停止开关并登记恢复（默认关闭是刻意的，见 §9.2）。
func enableMeshStop(t *testing.T) {
	t.Helper()
	prev := ChatWebMeshAllowStop()
	SetChatWebMeshAllowStop(true)
	t.Cleanup(func() { SetChatWebMeshAllowStop(prev) })
}

func TestHandleChatWebAPIMeshStop_MethodNotAllowed(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, loopbackRequest(ChatWebAPIMeshStopPath))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	if resp := decodeMeshStopResponse(t, rec); resp.Status != mesh.StopStatusError {
		t.Fatalf("status = %q, want error", resp.Status)
	}
}

func TestHandleChatWebAPIMeshStop_MeshDisabled(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	t.Cleanup(func() { mesh.SetCurrent(prev) })
	enableMeshStop(t)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, meshStopRequest(t, map[string]any{"target": "node-x"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（网格关闭只降级不报错）", rec.Code)
	}
	resp := decodeMeshStopResponse(t, rec)
	if resp.Status != mesh.StopStatusRefused || resp.Code != mesh.CallCodeMeshDisabled {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.CallCodeMeshDisabled)
	}
}

func TestHandleChatWebAPIMeshStop_DisabledByFlag(t *testing.T) {
	meshTestHost(t)
	prev := ChatWebMeshAllowStop()
	SetChatWebMeshAllowStop(false)
	t.Cleanup(func() { SetChatWebMeshAllowStop(prev) })

	called := false
	stubMeshStop(t, func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult {
		called = true
		return &mesh.StopResult{Status: mesh.StopStatusStopped}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, meshStopRequest(t, map[string]any{"target": "node-x"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	resp := decodeMeshStopResponse(t, rec)
	if resp.Status != mesh.StopStatusRefused || resp.Code != mesh.StopCodeNotAllowed {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.StopCodeNotAllowed)
	}
	if called {
		t.Fatal("开关关闭时不得真的去停进程")
	}
}

func TestHandleChatWebAPIMeshStop_NonLoopbackDenied(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)

	called := false
	stubMeshStop(t, func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult {
		called = true
		return &mesh.StopResult{Status: mesh.StopStatusStopped}
	})

	req := meshStopRequest(t, map[string]any{"target": "node-x"})
	req.RemoteAddr = "192.0.2.10:4444"
	req.Host = "192.0.2.10:8899"
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	resp := decodeMeshStopResponse(t, rec)
	if resp.Status != mesh.StopStatusRefused || resp.Code != mesh.CallCodeNonLoopback {
		t.Fatalf("resp = %+v, want refused/%s", resp, mesh.CallCodeNonLoopback)
	}
	if called {
		t.Fatal("非回环请求不得走到停止执行")
	}
}

func TestHandleChatWebAPIMeshStop_BadRequest(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)

	cases := []struct {
		name     string
		body     any
		wantCode string
	}{
		{"缺 target", map[string]any{"mode": "graceful"}, ""},
		{"mode 非法", map[string]any{"target": "node-x", "mode": "kill"}, mesh.StopCodeBadMode},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		HandleChatWebAPIMeshStop(rec, meshStopRequest(t, tc.body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, rec.Code)
		}
		resp := decodeMeshStopResponse(t, rec)
		if resp.Code != tc.wantCode {
			t.Fatalf("%s: code = %q, want %q", tc.name, resp.Code, tc.wantCode)
		}
	}
}

func TestHandleChatWebAPIMeshStop_SelfRefused(t *testing.T) {
	host := meshTestHost(t)
	enableMeshStop(t)

	called := false
	stubMeshStop(t, func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult {
		called = true
		return &mesh.StopResult{Status: mesh.StopStatusStopped}
	})

	// ① 目标就是本进程：拒绝。
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, meshStopRequest(t, map[string]any{"target": host.NodeID()}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if resp := decodeMeshStopResponse(t, rec); resp.Code != mesh.StopCodeSelfRefused {
		t.Fatalf("code = %q, want %q", resp.Code, mesh.StopCodeSelfRefused)
	}

	// ② 调用方指名要停自己（X-AICLI-Mesh-Caller）：同样拒绝。
	req := meshStopRequest(t, map[string]any{"target": "node-caller"})
	req.Header.Set(mesh.CallerNodeHeader, "node-caller")
	rec = httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if resp := decodeMeshStopResponse(t, rec); resp.Code != mesh.StopCodeSelfRefused {
		t.Fatalf("code = %q, want %q", resp.Code, mesh.StopCodeSelfRefused)
	}
	if called {
		t.Fatal("自停请求不得走到停止执行")
	}
}

func TestHandleChatWebAPIMeshStop_StatusMappingAndParams(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)

	var gotCaller string
	var gotReq mesh.StopRequest
	stubMeshStop(t, func(_ context.Context, _ *mesh.Host, caller string, req mesh.StopRequest) *mesh.StopResult {
		gotCaller = caller
		gotReq = req
		return &mesh.StopResult{
			Status:    mesh.StopStatusStopped,
			NodeID:    req.Target,
			PID:       4321,
			Mode:      req.Mode,
			Graceful:  true,
			ElapsedMs: 12,
		}
	})

	req := meshStopRequest(t, map[string]any{"target": "node-other", "mode": "force", "wait_ms": 1500})
	req.Header.Set(mesh.CallerNodeHeader, "node-caller")
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	resp := decodeMeshStopResponse(t, rec)
	if resp.SchemaVersion != mesh.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", resp.SchemaVersion, mesh.SchemaVersion)
	}
	if resp.Status != mesh.StopStatusStopped || resp.PID != 4321 || !resp.Graceful {
		t.Fatalf("resp = %+v, want stopped/pid=4321/graceful", resp)
	}
	if gotCaller != "node-caller" {
		t.Fatalf("caller = %q, want node-caller（审计与自停判定都用它）", gotCaller)
	}
	if gotReq.Mode != mesh.StopModeForce || gotReq.Wait != 1500*time.Millisecond {
		t.Fatalf("req = %+v, want force/1.5s", gotReq)
	}
}

func TestHandleChatWebAPIMeshStop_TimeoutMapping(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)
	stubMeshStop(t, func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult {
		return &mesh.StopResult{Status: mesh.StopStatusTimeout, Code: mesh.StopCodeTimeout, Message: "still running"}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, meshStopRequest(t, map[string]any{"target": "node-x"}))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	if resp := decodeMeshStopResponse(t, rec); resp.Status != mesh.StopStatusTimeout {
		t.Fatalf("resp = %+v, want timeout", resp)
	}
}
