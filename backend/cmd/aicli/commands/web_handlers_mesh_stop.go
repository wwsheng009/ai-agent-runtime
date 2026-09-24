package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// ============================================================================
// POST /web/api/mesh/stop —— 停止目标节点（架构 §5.7 / §9.2，实施方案 S16）
//
// 契约：
//   - 治理动作，**默认关闭**：--mesh-allow-stop=true 才放行，否则一律
//     refused + mesh_stop_not_allowed（端点仍注册，让调用方读到原因码，
//     而不是 404 那种「假装没有这个能力」）；
//   - 仅回环（§5.8）：非回环默认拒绝，与 mesh/call、mesh/spawn 同一矩阵；
//   - 目标解析、优雅/强制语义、审计全部复用 internal/mesh，Web 层不产生第二
//     套语义：graceful = 投 /exit 等它自己收尾，force = 终止进程；
//   - 调用方 == 目标时拒绝（mesh_stop_self_refused）：进程不该通过网格杀自己，
//     那是 /exit 的语义（本进程、以及 X-AICLI-Mesh-Caller 指名的调用方都算）。
// ============================================================================

// chatWebMeshStopSwitch 是停止开关（§9.2）：默认**关闭**——停止比拉起危险
// 得多（会中断别人的会话，且 force 不给收尾机会），必须显式开启。进程级接线
// 在 main.go 的 `--mesh-allow-stop`。
var chatWebMeshStopSwitch = struct {
	mu      sync.RWMutex
	allowed bool
}{}

// SetChatWebMeshAllowStop 设置停止开关（默认 false）。
func SetChatWebMeshAllowStop(enabled bool) {
	chatWebMeshStopSwitch.mu.Lock()
	defer chatWebMeshStopSwitch.mu.Unlock()
	chatWebMeshStopSwitch.allowed = enabled
}

// ChatWebMeshAllowStop 读取停止开关的当前值。
func ChatWebMeshAllowStop() bool {
	chatWebMeshStopSwitch.mu.RLock()
	defer chatWebMeshStopSwitch.mu.RUnlock()
	return chatWebMeshStopSwitch.allowed
}

// chatWebMeshStopFn 是停止执行的间接层：生产路径为 mesh.StopNode（编排 + 本
// 进程审计），单元测试可临时替换它，避免真的去停进程（与 chatWebMeshSpawnFn
// 同一约定）。
var chatWebMeshStopFn = func(ctx context.Context, host *mesh.Host, callerNodeID string, req mesh.StopRequest) *mesh.StopResult {
	return mesh.StopNode(ctx, host, callerNodeID, req)
}

// meshStopEnvelope 是响应信封：schema_version + StopResult 平铺（与 §5.9 的
// 调用信封同构，字段名与 CLI 的 --json 输出逐字段一致）。
type meshStopEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	*mesh.StopResult
}

// HandleChatWebAPIMeshStop 处理 POST /web/api/mesh/stop（架构 §5.7）。
func HandleChatWebAPIMeshStop(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeChatWebMeshStopError(w, http.StatusMethodNotAllowed, mesh.StopStatusError,
			"", "method not allowed: use POST "+ChatWebAPIMeshStopPath, started)
		return
	}
	host := mesh.Current()
	if host == nil {
		writeChatWebMeshStopError(w, http.StatusForbidden, mesh.StopStatusRefused,
			mesh.CallCodeMeshDisabled, "mesh disabled (start with --mesh)", started)
		return
	}
	if !ChatWebMeshAllowStop() {
		writeChatWebMeshStopError(w, http.StatusForbidden, mesh.StopStatusRefused,
			mesh.StopCodeNotAllowed, "stopping is disabled (--mesh-allow-stop=false)", started)
		return
	}
	// 调用者必须来自回环：跨机默认拒绝，显式 --mesh-allow-nonloopback=true 才
	// 放宽（§5.8 / §9.4）；--mesh-allow-stop 与令牌要求都不受影响。
	if !chatWebMeshWritePathAllowed(r) {
		writeChatWebMeshStopError(w, http.StatusForbidden, mesh.StopStatusRefused,
			mesh.CallCodeNonLoopback, "mesh stop is loopback-only (--mesh-allow-nonloopback=true relaxes this)", started)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, mesh.CallMaxBodyBytes+1))
	if err != nil {
		writeChatWebMeshStopError(w, http.StatusBadRequest, mesh.StopStatusError,
			"", "read body: "+err.Error(), started)
		return
	}
	if len(payload) > mesh.CallMaxBodyBytes {
		writeChatWebMeshStopError(w, http.StatusRequestEntityTooLarge, mesh.StopStatusError,
			mesh.CallCodeBodyTooLarge, fmt.Sprintf("body exceeds %d bytes", mesh.CallMaxBodyBytes), started)
		return
	}
	var req mesh.StopRequestBody
	if err := json.Unmarshal(payload, &req); err != nil {
		writeChatWebMeshStopError(w, http.StatusBadRequest, mesh.StopStatusError,
			"", "invalid JSON body: "+err.Error(), started)
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		writeChatWebMeshStopError(w, http.StatusBadRequest, mesh.StopStatusError,
			"", "target is required", started)
		return
	}
	mode, ok := mesh.NormalizeStopMode(req.Mode)
	if !ok {
		writeChatWebMeshStopError(w, http.StatusBadRequest, mesh.StopStatusError,
			mesh.StopCodeBadMode, fmt.Sprintf("unknown mode %q (use graceful|force)", req.Mode), started)
		return
	}
	// 自停拒绝（两道）：本进程（无论谁在请求），以及指名要停自己的调用方。
	// 只做精确串比较，解析后的真实 node_id 由 mesh.Stop 再查一次（防前缀/
	// 会话 ID 绕道）。
	callerNodeID := strings.TrimSpace(r.Header.Get(mesh.CallerNodeHeader))
	if callerNodeID == "" {
		callerNodeID = host.NodeID()
	}
	if target == host.NodeID() {
		writeChatWebMeshStopError(w, http.StatusForbidden, mesh.StopStatusRefused,
			mesh.StopCodeSelfRefused, "refusing to stop this node itself; use /exit", started)
		return
	}
	if target == callerNodeID {
		writeChatWebMeshStopError(w, http.StatusForbidden, mesh.StopStatusRefused,
			mesh.StopCodeSelfRefused, "refusing to stop the calling node itself; use /exit", started)
		return
	}
	result := chatWebMeshStopFn(r.Context(), host, callerNodeID, mesh.StopRequest{
		Target: target,
		Mode:   mode,
		Wait:   time.Duration(req.WaitMS) * time.Millisecond,
	})
	if result == nil {
		result = &mesh.StopResult{
			Status:    mesh.StopStatusError,
			Code:      mesh.StopCodeForceFailed,
			Message:   "stop returned no result",
			Mode:      mode,
			ElapsedMs: time.Since(started).Milliseconds(),
		}
	}
	writeWebAPIJSON(w, mesh.StopHTTPStatus(result.Status), meshStopEnvelope{
		SchemaVersion: mesh.SchemaVersion,
		StopResult:    result,
	})
}

// writeChatWebMeshStopError 写统一错误信封（非 2xx 与 refused/error 同形：
// {schema_version, status, code, message, node_id?}）。
func writeChatWebMeshStopError(w http.ResponseWriter, httpStatus int, status, code, message string, started time.Time) {
	nodeID := ""
	if host := mesh.Current(); host != nil {
		nodeID = host.NodeID()
	}
	writeWebAPIJSON(w, httpStatus, meshStopEnvelope{
		SchemaVersion: mesh.SchemaVersion,
		StopResult: &mesh.StopResult{
			Status:    status,
			Code:      code,
			Message:   message,
			NodeID:    nodeID,
			ElapsedMs: time.Since(started).Milliseconds(),
		},
	})
}
