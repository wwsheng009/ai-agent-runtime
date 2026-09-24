package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ============================================================================
// mesh/stop —— 停止目标节点（架构 §5.7 / §7.2，实施方案 S16）
//
// 两种模式，语义不同、互不兜底：
//
//   - graceful（默认）：把 "/exit" 投给目标的 /web/api/input，让目标像用户在
//     TUI 里敲了退出命令一样自己收尾（保存会话、注销档案、释放租约），然后
//     等进程消失。不发任何信号；目标没有回环控制面时只能 --force。
//   - force：直接终止进程（TerminateProcess / SIGKILL）。只杀进程，不保证
//     目标来得及写 node.stopped——残留档案由 gc 按「可证已死」规则回收
//     （§4.7 降级：审计可以少，用户数据不能丢）。
//
// 拒绝矩阵（§5.8 / §9.2）：停止是**治理动作**，默认关闭（--mesh-allow-stop），
// HTTP 层在开关关闭时直接 refused，走不到这里；调用方 == 目标时一律拒绝
// （进程不该通过网格杀自己——那是 /exit 的语义）。
// ============================================================================

// ChatWebMeshStopPath 是网格停止端点（架构 §5.7）。HTTP 层与调用方共用同一
// 常量，避免两侧漂移（与 ChatWebMeshCallPath 同一约定）。
const ChatWebMeshStopPath = "/web/api/mesh/stop"

// 停止模式（§5.7）。
const (
	StopModeGraceful = "graceful"
	StopModeForce    = "force"
)

// 停止状态取值。四态与 §5.7 的 spawn 同构：stopped 是唯一成功态，且**幂等**
// （目标已经不在运行时也回 stopped，让重试安全）。
const (
	StopStatusStopped  = "stopped"
	StopStatusNotFound = "not_found"
	StopStatusRefused  = "refused"
	StopStatusTimeout  = "timeout"
	StopStatusError    = "error"
)

// 停止原因码（§5.9 词汇表扩展）。
const (
	// StopCodeNotAllowed 由 HTTP 层在 --mesh-allow-stop=false 时给出。
	StopCodeNotAllowed = "mesh_stop_not_allowed"
	// StopCodeSelfRefused 是「调用方 == 目标」：拒绝，且不做任何尝试。
	StopCodeSelfRefused = "mesh_stop_self_refused"
	// StopCodeBadMode 是 mode 不在 {graceful, force} 内（调用方参数问题）。
	StopCodeBadMode = "mesh_stop_bad_mode"
	// StopCodeForceFailed 是终止进程的系统调用失败且进程仍在运行。
	StopCodeForceFailed = "mesh_stop_force_failed"
	// StopCodeTimeout 是命令已投递/信号已发，但进程在 --wait 内没消失。
	StopCodeTimeout = "mesh_stop_timeout"
	// StopCodeAlreadyStopped 是档案自己说「我已停止」（幂等成功）。
	StopCodeAlreadyStopped = "mesh_stop_already_stopped"
)

// StopDefaultWait 是 §7.2 的 --wait 默认值：投递 /exit 后最多等这么久。
const StopDefaultWait = 30 * time.Second

// StopMaxWait 是 --wait 的上限：一个调用不该把连接挂成「永久」。
const StopMaxWait = 5 * time.Minute

// StopPollInterval 是等待进程消失的轮询间隔。
const StopPollInterval = 200 * time.Millisecond

// StopExitPrompt 是优雅停止投递的输入（§5.7）：它就是用户在 TUI 里敲的退出
// 命令，走同一条 slash 命令路径，因此保存/注销/释放租约全部复用。
const StopExitPrompt = "/exit"

// stopExitArgs 是优雅停止的载荷（预编码；%q 对 ASCII 命令名是合法 JSON）。
var stopExitArgs = json.RawMessage(fmt.Sprintf(`{"prompt":%q}`, StopExitPrompt))

// StopRequestBody 是 POST /web/api/mesh/stop 的请求体（§5.7）。
type StopRequestBody struct {
	Target string `json:"target"`
	Mode   string `json:"mode,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// StopRequest 是一次停止的调用方意图。
type StopRequest struct {
	// Target 是节点引用：node_id / 前缀 / 会话 id / 前缀 / pid:<PID>
	// （与 `aicli-mesh` 其它子命令同一解析口径）。
	Target string
	// Mode 是 graceful|force（空 → graceful）。
	Mode string
	// Wait 覆盖等待预算（0 → StopDefaultWait，上限 StopMaxWait）。
	Wait time.Duration
}

// StopResult 是停止的结果视图（同时可直接序列化成 CLI 的 --json 输出）。
type StopResult struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	NodeID  string `json:"node_id,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Mode    string `json:"mode,omitempty"`
	// Graceful 说明这次停止是否真的投递了 /exit（force 恒 false）。
	Graceful  bool  `json:"graceful,omitempty"`
	ElapsedMs int64 `json:"elapsed_ms"`
}

// OK 表示停止成功（目标已不在运行）。
func (r *StopResult) OK() bool { return r != nil && r.Status == StopStatusStopped }

// NormalizeStopMode 归一化模式：空 → graceful；未知模式返回 ok=false。
func NormalizeStopMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", StopModeGraceful:
		return StopModeGraceful, true
	case StopModeForce:
		return StopModeForce, true
	default:
		return "", false
	}
}

// StopHTTPStatus 把停止状态映射成 HTTP 状态码（与 §5.9 的调用映射同构：
// 成功 200、目标不存在 404、被拒绝 403、等待超时 504、其余 500）。
func StopHTTPStatus(status string) int {
	switch status {
	case StopStatusStopped:
		return http.StatusOK
	case StopStatusNotFound:
		return http.StatusNotFound
	case StopStatusRefused:
		return http.StatusForbidden
	case StopStatusTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// Stop 执行一次停止（调用方编排，§5.7）。paths 是网格根（CLI 传 c.paths()），
// callerNodeID 是调用方 node_id（CLI 不是节点，传空串）。
//
// 返回的 StopResult 永远非 nil：解析失败、策略拒绝、超时都折成 §5.7 的状态
// 词汇，只有「调用方自身的参数问题」（未知 mode）以 status=error 提前返回，
// 且不发起任何网络请求、不发任何信号。
func Stop(ctx context.Context, paths Paths, callerNodeID string, req StopRequest) *StopResult {
	started := time.Now()
	mode, ok := NormalizeStopMode(req.Mode)
	if !ok {
		return &StopResult{
			Status:    StopStatusError,
			Code:      StopCodeBadMode,
			Message:   fmt.Sprintf("unknown mode %q (use graceful|force)", strings.TrimSpace(req.Mode)),
			Mode:      strings.TrimSpace(req.Mode),
			ElapsedMs: elapsedMs(started),
		}
	}
	wait := req.Wait
	if wait <= 0 {
		wait = StopDefaultWait
	}
	if wait > StopMaxWait {
		wait = StopMaxWait
	}

	target, err := ResolveTargetNode(paths, req.Target)
	if err != nil {
		code := ""
		var targetErr *CallTargetError
		if errors.As(err, &targetErr) {
			code = targetErr.Code
		}
		if code == CallCodeTargetStopped {
			// 幂等：档案自己说它已经停了。不猜 PID、不发信号。
			return &StopResult{
				Status:    StopStatusStopped,
				Code:      StopCodeAlreadyStopped,
				Message:   err.Error(),
				Mode:      mode,
				ElapsedMs: elapsedMs(started),
			}
		}
		return &StopResult{
			Status:    StopStatusNotFound,
			Code:      code,
			Message:   err.Error(),
			Mode:      mode,
			ElapsedMs: elapsedMs(started),
		}
	}
	if caller := strings.TrimSpace(callerNodeID); caller != "" && caller == target.NodeID {
		return &StopResult{
			Status:    StopStatusRefused,
			Code:      StopCodeSelfRefused,
			Message:   "refusing to stop the calling node itself; use /exit",
			NodeID:    target.NodeID,
			Mode:      mode,
			ElapsedMs: elapsedMs(started),
		}
	}

	pid := target.Record.PID
	result := &StopResult{NodeID: target.NodeID, PID: pid, Mode: mode}
	if pid <= 0 || !processAlive(pid) {
		// 已经不在运行：成功且幂等（重试安全），但不谎称是我们停的。
		result.Status = StopStatusStopped
		result.Message = "process is not running"
		result.ElapsedMs = elapsedMs(started)
		return result
	}

	switch mode {
	case StopModeForce:
		if err := terminateProcess(pid); err != nil && processAlive(pid) {
			result.Status = StopStatusError
			result.Code = StopCodeForceFailed
			result.Message = fmt.Sprintf("terminate pid %d: %v", pid, err)
			result.ElapsedMs = elapsedMs(started)
			return result
		}
		if !waitForProcessExit(ctx, pid, wait) {
			result.Status = StopStatusTimeout
			result.Code = StopCodeTimeout
			result.Message = fmt.Sprintf("pid %d is still running %s after terminate", pid, wait)
			result.ElapsedMs = elapsedMs(started)
			return result
		}
	default:
		// graceful：目标必须能收命令。没有回环控制面就直说，别偷偷改杀进程。
		if target.Record.Endpoint == nil || strings.TrimSpace(target.Record.Endpoint.BaseURL) == "" || target.Record.Endpoint.Port <= 0 {
			result.Status = StopStatusRefused
			result.Code = CallCodeNoEndpoint
			result.Message = "node has no loopback control plane; retry with mode=force"
			result.ElapsedMs = elapsedMs(started)
			return result
		}
		call := Call(ctx, paths, callerNodeID, CallRequest{
			Target:     target.NodeID,
			Op:         "input",
			Args:       stopExitArgs,
			Timeout:    wait,
			AllowWrite: true,
		})
		if !call.OK() {
			result.Status = stopStatusFromCall(call.Status)
			result.Code = call.Code
			result.Message = fmt.Sprintf("deliver %s failed: %s", StopExitPrompt, call.Message)
			result.ElapsedMs = elapsedMs(started)
			return result
		}
		result.Graceful = true
		if !waitForProcessExit(ctx, pid, wait) {
			result.Status = StopStatusTimeout
			result.Code = StopCodeTimeout
			result.Message = fmt.Sprintf("node %s accepted %s but is still running after %s; retry with mode=force",
				target.NodeID, StopExitPrompt, wait)
			result.ElapsedMs = elapsedMs(started)
			return result
		}
	}

	result.Status = StopStatusStopped
	if result.Graceful {
		result.Message = fmt.Sprintf("delivered %s and the process exited", StopExitPrompt)
	} else {
		result.Message = "process terminated"
	}
	result.ElapsedMs = elapsedMs(started)
	return result
}

// StopNode 是节点侧的停止入口（§5.7）：编排一次停止，并写本进程的 journal。
// callerNodeID 是**发起方**的 node_id（HTTP 层取 X-AICLI-Mesh-Caller，缺省
// 视为本进程）：它既用于自停拒绝，也用于审计归属。CLI 等「不是节点」的调用方
// 用包级 Stop（callerNodeID 传空串）。
func StopNode(ctx context.Context, host *Host, callerNodeID string, req StopRequest) *StopResult {
	paths := ResolvePaths()
	if host != nil {
		paths = host.Paths()
	}
	result := Stop(ctx, paths, callerNodeID, req)
	host.appendStopAudit(req.Target, result)
	return result
}

// Stop 是 Host 上的便捷包装：调用方即本进程（自停一律拒绝）。
func (h *Host) Stop(ctx context.Context, req StopRequest) *StopResult {
	callerID := ""
	if h != nil {
		callerID = h.NodeID()
	}
	return StopNode(ctx, h, callerID, req)
}

// appendStopAudit 写调用方一侧的 journal（§5.7 审计）：requested 记意图，
// completed 记结果。只记模式/目标/状态/耗时与 PID，不记任何正文。
func (h *Host) appendStopAudit(target string, result *StopResult) {
	if h == nil || result == nil {
		return
	}
	journal := h.Journal()
	requested := map[string]any{"target": strings.TrimSpace(target), "mode": result.Mode}
	if result.NodeID != "" {
		requested["node"] = result.NodeID
	}
	if result.PID > 0 {
		requested["pid"] = result.PID
	}
	journal.Append(JournalStopRequested, "", requested)
	detail := map[string]any{
		"target":     strings.TrimSpace(target),
		"mode":       result.Mode,
		"status":     result.Status,
		"elapsed_ms": result.ElapsedMs,
	}
	if result.NodeID != "" {
		detail["node"] = result.NodeID
	}
	if result.PID > 0 {
		detail["pid"] = result.PID
	}
	if result.Code != "" {
		detail["code"] = result.Code
	}
	if result.Graceful {
		detail["graceful"] = true
	}
	journal.Append(JournalStopCompleted, "", detail)
}

// stopStatusFromCall 把「投递 /exit」失败的调用状态折回停止状态词汇。
func stopStatusFromCall(status string) string {
	switch status {
	case CallStatusNotFound, CallStatusUnreachable:
		return StopStatusNotFound
	case CallStatusRefused, CallStatusBusy:
		return StopStatusRefused
	case CallStatusTimeout:
		return StopStatusTimeout
	default:
		return StopStatusError
	}
}

// waitForProcessExit 在 wait 预算内轮询进程是否消失。ctx 取消时返回当前
// 事实（不假装成功）：调用方据此拿到 timeout 而不是假 stopped。
func waitForProcessExit(ctx context.Context, pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if !processAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return !processAlive(pid)
			case <-time.After(StopPollInterval):
			}
			continue
		}
		time.Sleep(StopPollInterval)
	}
}
