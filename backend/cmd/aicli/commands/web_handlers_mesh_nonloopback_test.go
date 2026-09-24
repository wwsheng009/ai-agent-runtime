package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 跨机逃生门（--mesh-allow-nonloopback，架构 §9.4 / §10）的机器化契约：
// 默认关闭时非回环一律拒绝（三条拒绝用例在各自端点的测试里）；显式开启后只
// 放宽「跨机拒绝」这一层——写 op 仍要逐次 allow_write、停止仍要
// --mesh-allow-stop、令牌仍由鉴权中间件强制（开关不是把默认值变虚）。

// enableMeshNonLoopback 打开跨机开关并登记恢复。
func enableMeshNonLoopback(t *testing.T) {
	t.Helper()
	prev := ChatWebMeshAllowNonLoopback()
	SetChatWebMeshAllowNonLoopback(true)
	t.Cleanup(func() { SetChatWebMeshAllowNonLoopback(prev) })
}

// nonLoopbackRequest 把请求伪装成来自另一台机器：§9.4 的判定依据是
// RemoteAddr（首选）与 Host（httptest 回退）。
func nonLoopbackRequest(req *http.Request) *http.Request {
	req.RemoteAddr = "192.0.2.10:4444"
	req.Host = "192.0.2.10:8899"
	return req
}

// TestChatWebMeshWritePathAllowed_LoopbackAlwaysAllowed 验证开关只影响跨机：
// 回环请求在开关的两种取值下都必须放行（本机信任模型不变）。
func TestChatWebMeshWritePathAllowed_LoopbackAlwaysAllowed(t *testing.T) {
	prev := ChatWebMeshAllowNonLoopback()
	t.Cleanup(func() { SetChatWebMeshAllowNonLoopback(prev) })

	for _, enabled := range []bool{false, true} {
		SetChatWebMeshAllowNonLoopback(enabled)
		if !chatWebMeshWritePathAllowed(meshCallRequest(t, map[string]any{"op": "node.info"})) {
			t.Fatalf("回环请求必须放行（开关=%v）：开关只放宽跨机，不改变本机信任模型", enabled)
		}
	}
}

// TestHandleChatWebAPIMeshCall_NonLoopbackAllowedBySwitch 验证开启后非回环
// 只读调用真抵达端点，而写 op 的逐次 allow_write 要求不受影响。
func TestHandleChatWebAPIMeshCall_NonLoopbackAllowedBySwitch(t *testing.T) {
	meshTestHost(t)
	enableMeshNonLoopback(t)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(rec, nonLoopbackRequest(meshCallRequest(t, map[string]any{"op": "node.info"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（开关开启后非回环只读调用放行）: %s", rec.Code, rec.Body.String())
	}
	if envelope := decodeMeshCallEnvelope(t, rec); envelope.Status != mesh.CallStatusOK {
		t.Fatalf("envelope = %+v, want status=%s", envelope, mesh.CallStatusOK)
	}

	// 写 op 不带 allow_write：仍然 403 —— 开关不放宽逐次显式。
	writeRec := httptest.NewRecorder()
	HandleChatWebAPIMeshCall(writeRec, nonLoopbackRequest(meshCallRequest(t, map[string]any{
		"op":   "invoke",
		"args": map[string]any{"prompt": "只回复两个字：收到"},
	})))
	if writeRec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（写 op 无 allow_write）", writeRec.Code)
	}
	if envelope := decodeMeshCallEnvelope(t, writeRec); envelope.Code != mesh.CallCodeWriteNotAllowed {
		t.Fatalf("code = %q, want %s", envelope.Code, mesh.CallCodeWriteNotAllowed)
	}
}

// TestHandleChatWebAPIMeshSpawn_NonLoopbackAllowedBySwitch 验证 spawn 门禁同样
// 服从跨机开关（§9.4 明确把 spawn 列入写路径）。
func TestHandleChatWebAPIMeshSpawn_NonLoopbackAllowedBySwitch(t *testing.T) {
	meshTestHost(t)
	enableMeshNonLoopback(t)

	called := false
	stubMeshSpawn(t, func(req mesh.SpawnRequest, _ mesh.SpawnOptions) mesh.SpawnResult {
		called = true
		return mesh.SpawnResult{Status: mesh.SpawnStatusReused, SessionID: req.SessionID}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSpawn(rec, nonLoopbackRequest(meshSpawnRequest(t, map[string]any{"session_id": "s1"})))
	if !called {
		t.Fatalf("开关开启后非回环 spawn 必须抵达拉起实现: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestHandleChatWebAPIMeshStop_NonLoopbackAllowedBySwitch 验证 stop 门禁同样
// 服从跨机开关，但 --mesh-allow-stop 的要求不受影响（本用例显式打开它）。
func TestHandleChatWebAPIMeshStop_NonLoopbackAllowedBySwitch(t *testing.T) {
	meshTestHost(t)
	enableMeshStop(t)
	enableMeshNonLoopback(t)

	called := false
	stubMeshStop(t, func(context.Context, *mesh.Host, string, mesh.StopRequest) *mesh.StopResult {
		called = true
		return &mesh.StopResult{Status: mesh.StopStatusStopped}
	})

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshStop(rec, nonLoopbackRequest(meshStopRequest(t, map[string]any{"target": "node-x"})))
	if !called {
		t.Fatalf("开关开启后非回环 stop 必须抵达停止实现: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
