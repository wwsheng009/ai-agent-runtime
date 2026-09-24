package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ============================================================================
// 网格调用（架构 §5.6 / §5.9，实施方案 S8）
//
// 本文件是**调用方**一侧的编排：解析目标 → 读目标档案里的令牌 → 以
// X-AICLI-Token + X-AICLI-Mesh-Caller 发起 POST /web/api/mesh/call → 401 时
// 重读一次档案重试一次。被调方一侧的端点实现见
// cmd/aicli/commands/web_handlers_mesh_call.go。
//
// 设计约束：
//   - 网格层**不**实现幂等：client_request_id 原样透传，幂等语义由目标端点
//     （/web/api/invoke 的幂等表）负责（§5.6 实现要点 3）。
//   - journal 只记 op 与耗时，**不记 args 正文**（args 可能含用户 prompt）。
//   - 令牌只出现在请求头里，绝不进 journal / 日志 / 视图（§9.5）。
// ============================================================================

// ChatWebMeshCallPath 是网格调用端点（架构 §5.6）。HTTP 层与调用方共用同一
// 常量，避免两侧漂移（与 ChatWebMeshEventsPath 同一约定）。
const ChatWebMeshCallPath = "/web/api/mesh/call"

// ChatWebMeshSpawnPath 是网格拉起端点（架构 §5.7）：在正确的工作区把某个
// 会话的节点拉起来，并把 §7.3 的窗口 URL 交给调用方。与 HTTP 层共用同一
// 常量（同 ChatWebMeshCallPath 的约定）。
const ChatWebMeshSpawnPath = "/web/api/mesh/spawn"

// CallerNodeHeader 携带调用方 node_id：被调方用它做审计与（可选的）跨工作区
// 判定，不参与鉴权（鉴权是回环 + 令牌，§5.6 / §5.8）。
const CallerNodeHeader = "X-AICLI-Mesh-Caller"

// CallTokenHeader 是目标端点的写令牌头名，与 cmd/aicli/commands 的
// ChatWebAuthTokenHeader 同值（后者以本常量赋值，单一事实来源）。
const CallTokenHeader = "X-AICLI-Token"

// 调用状态取值（§5.6）。调用方与被调方共用同一套词汇。
const (
	CallStatusOK          = "ok"
	CallStatusBusy        = "busy"
	CallStatusNotFound    = "not_found"
	CallStatusUnreachable = "unreachable"
	CallStatusRefused     = "refused"
	CallStatusTimeout     = "timeout"
	CallStatusError       = "error"
)

// 调用细分原因码（§5.6 / §5.9）。只在能给出机器可判原因时出现。
const (
	CallCodeUnknownOp       = "mesh_unknown_op"
	CallCodeWriteNotAllowed = "mesh_write_not_allowed"
	CallCodeCrossWorkspace  = "mesh_cross_workspace_denied"
	CallCodeTokenStale      = "mesh_token_stale"
	CallCodeNonLoopback     = "mesh_nonloopback_denied"
	CallCodeMeshDisabled    = "mesh_disabled"
	CallCodeBodyTooLarge    = "mesh_body_too_large"
	CallCodeTargetNotFound  = "mesh_target_not_found"
	CallCodeTargetStopped   = "mesh_target_stopped"
	CallCodeTargetAmbiguous = "mesh_target_ambiguous"
	CallCodeNoEndpoint      = "mesh_no_endpoint"
	CallCodeSchemaTooNew    = "mesh_schema_unsupported"
	CallCodeTargetMismatch  = "mesh_target_mismatch"
	CallCodeUpstream        = "mesh_upstream_error"
)

// CallDefaultTimeout 是 §5.9 的调用默认超时。
const CallDefaultTimeout = 130 * time.Second

// CallMaxBodyBytes 是请求体上限（§5.9：默认 1 MiB）。
const CallMaxBodyBytes = 1 << 20

// CallMaxResponseBytes 限制被调方响应的读取上限：防止一个失控节点把调用方
// 拖进无界分配。
const CallMaxResponseBytes = 4 << 20

// CallOpSpec 描述一个白名单 op（§5.6 的表格）：是否写操作、对应目标端点。
type CallOpSpec struct {
	Op       string
	Write    bool
	Endpoint string
}

// callOpSpecs 是 §5.6 的 op 白名单（9 项，顺序与文档表格一致）。
var callOpSpecs = []CallOpSpec{
	{Op: "node.info", Endpoint: "/web/api/mesh/self"},
	{Op: "status", Endpoint: "/web/api/status"},
	{Op: "screen", Endpoint: "/web/api/screen"},
	{Op: "turn", Endpoint: "/web/api/turn"},
	{Op: "sessions.list", Endpoint: "/web/api/sessions"},
	{Op: "invoke", Write: true, Endpoint: "/web/api/invoke"},
	{Op: "input", Write: true, Endpoint: "/web/api/input"},
	{Op: "cancel", Write: true, Endpoint: "/web/api/input"},
	{Op: "sessions.resume", Write: true, Endpoint: "/web/api/sessions/resume"},
}

// CallOps 返回白名单副本：调用方（CLI 帮助、Web 前端）据此渲染可选 op，
// 修改返回值不影响白名单本身。
func CallOps() []CallOpSpec {
	out := make([]CallOpSpec, len(callOpSpecs))
	copy(out, callOpSpecs)
	return out
}

// CallOpSpecFor 查询单个 op 的规格；未知 op 返回 ok=false。
func CallOpSpecFor(op string) (CallOpSpec, bool) {
	op = strings.TrimSpace(op)
	for _, spec := range callOpSpecs {
		if spec.Op == op {
			return spec, true
		}
	}
	return CallOpSpec{}, false
}

// CallOpKnown 判断 op 是否在白名单内。
func CallOpKnown(op string) bool {
	_, ok := CallOpSpecFor(op)
	return ok
}

// CallOpIsWrite 判断 op 是否属于写操作（§5.6：写操作必须显式 allow_write）。
func CallOpIsWrite(op string) bool {
	spec, ok := CallOpSpecFor(op)
	return ok && spec.Write
}

// CallHTTPStatus 把调用状态映射成 HTTP 状态码（§5.9）。
func CallHTTPStatus(status string) int {
	switch status {
	case CallStatusOK:
		return http.StatusOK
	case CallStatusBusy:
		return http.StatusConflict
	case CallStatusNotFound:
		return http.StatusNotFound
	case CallStatusRefused:
		return http.StatusForbidden
	case CallStatusUnreachable, CallStatusTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// CallStatusFromHTTP 把被调方（或中间层）的 HTTP 状态码折回调用状态词汇。
func CallStatusFromHTTP(code int) string {
	switch {
	case code >= 200 && code < 300:
		return CallStatusOK
	case code == http.StatusConflict:
		return CallStatusBusy
	case code == http.StatusNotFound:
		return CallStatusNotFound
	case code == http.StatusForbidden, code == http.StatusUnauthorized:
		return CallStatusRefused
	case code == http.StatusGatewayTimeout || code == http.StatusRequestTimeout:
		return CallStatusTimeout
	case code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge:
		return CallStatusError
	default:
		return CallStatusError
	}
}

// CallRequestBody 是 POST /web/api/mesh/call 的请求体（§5.6 示例）。
//
// target 只对**调用方**有意义（被调方用它做防串线自检：形如 node-* 的
// target 必须等于自己的 node_id）。
type CallRequestBody struct {
	Target          string          `json:"target,omitempty"`
	Op              string          `json:"op"`
	Args            json.RawMessage `json:"args,omitempty"`
	ClientRequestID string          `json:"client_request_id,omitempty"`
	TimeoutMs       int             `json:"timeout_ms,omitempty"`
	AllowWrite      bool            `json:"allow_write,omitempty"`
}

// CallEnvelope 是 POST /web/api/mesh/call 的响应信封（§5.6）。
//
// result 是目标端点的**原始响应**：调用方拿到的是被调方端点自己的结构
// （invoke 的 turn_id/assistant、screen 的快照…），网格层不重写语义。
type CallEnvelope struct {
	SchemaVersion int             `json:"schema_version,omitempty"`
	Status        string          `json:"status"`
	Code          string          `json:"code,omitempty"`
	Message       string          `json:"message,omitempty"`
	NodeID        string          `json:"node_id,omitempty"`
	Op            string          `json:"op,omitempty"`
	ElapsedMs     int64           `json:"elapsed_ms"`
	Duplicate     bool            `json:"duplicate,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
}

// CallRequest 是一次网格调用的调用方意图。
type CallRequest struct {
	// Target 是节点引用：node_id / node_id 前缀 / 会话 id（与 `aicli-mesh`
	// 其它子命令同一解析口径）。
	Target string
	Op     string
	// Args 是目标端点的参数（与端点请求体同形）；nil 表示无参数。
	Args json.RawMessage
	// ClientRequestID 透传给目标端点的幂等键（仅 invoke 使用，§5.6 要点 3）。
	ClientRequestID string
	// Timeout 覆盖默认调用超时（0 → CallDefaultTimeout）。
	Timeout time.Duration
	// AllowWrite 必须为 true 才允许写 op（§5.6 要点 2：无隐式放行）。
	AllowWrite bool
}

// CallResult 是调用方的结果视图（同时可直接序列化成 CLI 的 --json 输出）。
type CallResult struct {
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	Op         string `json:"op,omitempty"`
	ElapsedMs  int64  `json:"elapsed_ms"`
	Duplicate  bool   `json:"duplicate,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	// Attempts 是实际发出的 HTTP 尝试次数（1，或 401 后重试的 2）。
	Attempts int `json:"attempts,omitempty"`
	// Result 是被调方端点的原始响应体（JSON）。
	Result json.RawMessage `json:"result,omitempty"`
}

// OK 表示调用成功（status=ok）。
func (r *CallResult) OK() bool { return r != nil && r.Status == CallStatusOK }

// CallTargetError 是目标解析失败：Code 用 §5.6 的原因码，便于 CLI 直接映射
// 到退出码与提示。
type CallTargetError struct {
	Code    string
	Message string
}

func (e *CallTargetError) Error() string { return e.Message }

// CallTarget 是解析成功的目标：节点档案 + 记录文件路径。
type CallTarget struct {
	NodeID    string
	Record    NodeRecord
	Path      string
	State     NodeState
	MatchedBy string
}

// ResolveCallTarget 解析节点引用（pid:<PID> / node_id / 前缀 / 会话 id / 会话前缀）
// 到一条可调用的节点档案。与 `aicli-mesh ls|show` 共用 BuildView 与
// matchTargetNodes 的口径（目标解析、状态、归属全部同源），但令牌从档案原文读取
// （视图永远脱敏，§9.1）。
func ResolveCallTarget(paths Paths, ref string) (*CallTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &CallTargetError{Code: CallCodeTargetNotFound, Message: "target is required"}
	}
	view := BuildView(paths, ViewOptions{})
	// 与 CLI（show/url/screen）共用同一解析口径：call/send 曾漏掉 `pid:<PID>`，
	// 同一个引用在 `show` 下可用、在 `call` 下报 not_found（E2E-DEBUG-03 M4 抓到）。
	matches, rule, badRef := matchTargetNodes(view, ref)
	if badRef {
		return nil, &CallTargetError{
			Code:    CallCodeTargetNotFound,
			Message: fmt.Sprintf("target %q is not a valid reference (use <node id>, <session id> or pid:<PID>)", ref),
		}
	}
	if len(matches) == 0 {
		return nil, &CallTargetError{
			Code:    CallCodeTargetNotFound,
			Message: fmt.Sprintf("no node matches %q (see `aicli-mesh ls`)", ref),
		}
	}
	if len(matches) > 1 {
		// 精确 node_id 命中优先；否则视为歧义（不猜）——与 CLI 的 pickTarget 同口径。
		exact := make([]NodeView, 0, 1)
		for _, node := range matches {
			if strings.EqualFold(node.NodeID, ref) {
				exact = append(exact, node)
			}
		}
		if len(exact) == 1 {
			matches = exact
		} else {
			return nil, &CallTargetError{
				Code:    CallCodeTargetAmbiguous,
				Message: fmt.Sprintf("%q matches %d nodes; use the full node id", ref, len(matches)),
			}
		}
	}
	chosen := matches[0]
	if chosen.Err != "" {
		code := CallCodeTargetNotFound
		if strings.Contains(chosen.Err, "schema") {
			code = CallCodeSchemaTooNew
		}
		return nil, &CallTargetError{
			Code:    code,
			Message: fmt.Sprintf("node %s record unusable: %s", chosen.NodeID, chosen.Err),
		}
	}
	if chosen.Record == nil {
		return nil, &CallTargetError{
			Code:    CallCodeTargetNotFound,
			Message: fmt.Sprintf("node %s record unavailable", chosen.NodeID),
		}
	}
	if chosen.State == NodeStateStopped {
		return nil, &CallTargetError{
			Code:    CallCodeTargetStopped,
			Message: fmt.Sprintf("node %s is stopped", chosen.NodeID),
		}
	}
	record := *chosen.Record
	if record.Endpoint == nil || strings.TrimSpace(record.Endpoint.BaseURL) == "" || record.Endpoint.Port <= 0 {
		return nil, &CallTargetError{
			Code:    CallCodeNoEndpoint,
			Message: fmt.Sprintf("node %s has no loopback control plane (start it with --pprof)", chosen.NodeID),
		}
	}
	return &CallTarget{
		NodeID:    chosen.NodeID,
		Record:    record,
		Path:      chosen.Path,
		State:     chosen.State,
		MatchedBy: rule,
	}, nil
}

// Call 是节点侧的调用入口：编排一次网格调用（§5.6），并写调用方一侧的
// journal（§5.6 要点 4）。CLI 等「不是节点」的调用方用包级 Call。
func (h *Host) Call(ctx context.Context, req CallRequest) *CallResult {
	paths := ResolvePaths()
	callerID := ""
	if h != nil {
		paths = h.Paths()
		callerID = h.NodeID()
	}
	result := Call(ctx, paths, callerID, req)
	// 审计：只记 op / 目标 / 结果 / 耗时，**不记 args 正文**（§5.6 要点 4）。
	h.appendCallSent(strings.TrimSpace(req.Op), result.NodeID, result)
	return result
}

// Call 执行一次网格调用（调用方编排，§5.6）。paths 是网格根（CLI 传
// c.paths()），callerID 是调用方 node_id（CLI 不是节点，传空串）。
//
// 返回的 CallResult 永远非 nil：传输层/协议层失败也会折成 §5.6 的状态词汇，
// 只有「调用方自身的参数问题」（未知 op、写操作未显式允许）才以 status=error /
// refused + 原因码提前返回，且不发起任何网络请求。
func Call(ctx context.Context, paths Paths, callerID string, req CallRequest) *CallResult {
	started := time.Now()
	op := strings.TrimSpace(req.Op)
	spec, ok := CallOpSpecFor(op)
	if !ok {
		return &CallResult{
			Status:     CallStatusError,
			Code:       CallCodeUnknownOp,
			Message:    fmt.Sprintf("unknown op %q", op),
			Op:         op,
			ElapsedMs:  elapsedMs(started),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if spec.Write && !req.AllowWrite {
		return &CallResult{
			Status:     CallStatusRefused,
			Code:       CallCodeWriteNotAllowed,
			Message:    fmt.Sprintf("op %s is a write op; pass --allow-write to confirm", op),
			Op:         op,
			ElapsedMs:  elapsedMs(started),
			HTTPStatus: http.StatusForbidden,
		}
	}
	target, err := ResolveCallTarget(paths, req.Target)
	if err != nil {
		result := &CallResult{
			Status:    CallStatusNotFound,
			Op:        op,
			ElapsedMs: elapsedMs(started),
		}
		if targetErr, ok := err.(*CallTargetError); ok {
			result.Code = targetErr.Code
			result.Message = targetErr.Message
			if targetErr.Code == CallCodeSchemaTooNew {
				result.Status = CallStatusRefused
			}
		} else {
			result.Code = CallCodeTargetNotFound
			result.Message = err.Error()
		}
		result.HTTPStatus = CallHTTPStatus(result.Status)
		return result
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = CallDefaultTimeout
	}
	body, marshalErr := buildCallRequestBody(target, req, timeout)
	if marshalErr != nil {
		return &CallResult{
			Status:     CallStatusError,
			Code:       CallCodeUpstream,
			Message:    "encode request: " + marshalErr.Error(),
			Op:         op,
			NodeID:     target.NodeID,
			ElapsedMs:  elapsedMs(started),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	result := postCall(ctx, target, body, timeout, callerID, op)
	result.NodeID = target.NodeID
	result.Op = op
	result.ElapsedMs = elapsedMs(started)
	return result
}

// CallTargetField 兼容 §5.6 的请求示例：target 透传为解析后的 node_id，
// 便于被调方做防串线自检。
func buildCallRequestBody(target *CallTarget, req CallRequest, timeout time.Duration) ([]byte, error) {
	body := CallRequestBody{
		Target:          target.NodeID,
		Op:              strings.TrimSpace(req.Op),
		Args:            req.Args,
		ClientRequestID: strings.TrimSpace(req.ClientRequestID),
		TimeoutMs:       int(timeout / time.Millisecond),
		AllowWrite:      req.AllowWrite,
	}
	return json.Marshal(body)
}

// postCall 发出请求并在 401（目标重启导致令牌轮换）时重读一次档案、重试一次
// （§5.6 要点 5）。仍失败则 refused + mesh_token_stale，不做无限重试。
func postCall(ctx context.Context, target *CallTarget, body []byte, timeout time.Duration, callerID, op string) *CallResult {
	record := target.Record
	attempts := 0
	var lastErr error
	for {
		attempts++
		result, retry, err := postCallOnce(ctx, target, record, body, timeout, callerID)
		// retry 只由 401（令牌轮换）置位，此时 err 为 nil 且 result 是 refused
		// 信封：必须走重试分支，而不是当成成功返回。
		if err == nil && !retry {
			result.Attempts = attempts
			return result
		}
		if err != nil {
			lastErr = err
		}
		if !retry || attempts >= 2 {
			if result != nil {
				result.Attempts = attempts
				return result
			}
			break
		}
		// 令牌轮换恢复：目标 401 → 重读档案一次（令牌可能已随重启轮换）。
		fresh, readErr := readCallRecord(target.Path, target.NodeID)
		if readErr != nil {
			// 档案不可读：无法证明新令牌，接受这次 refused 结果（不再重试）。
			if result != nil {
				result.Attempts = attempts
				return result
			}
			break
		}
		record = fresh
	}
	status := CallStatusUnreachable
	if isTimeoutErr(lastErr) {
		status = CallStatusTimeout
	}
	message := "call failed"
	if lastErr != nil {
		message = lastErr.Error()
	}
	return &CallResult{
		Status:     status,
		Code:       CallCodeUpstream,
		Message:    message,
		Attempts:   attempts,
		HTTPStatus: CallHTTPStatus(status),
	}
}

// postCallOnce 发一次请求。retry=true 表示调用方应当重读档案后重试一次
// （仅 401）。
func postCallOnce(ctx context.Context, target *CallTarget, record NodeRecord, body []byte, timeout time.Duration, callerID string) (*CallResult, bool, error) {
	base := strings.TrimSpace(record.Endpoint.BaseURL)
	url := joinURLPath(base, ChatWebMeshCallPath)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if callerID != "" {
		request.Header.Set(CallerNodeHeader, callerID)
	}
	if token := callTokenOf(record); token != "" {
		request.Header.Set(CallTokenHeader, token)
	}
	response, err := callHTTPClient().Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, CallMaxResponseBytes))
	if readErr != nil {
		return nil, false, readErr
	}
	result := decodeCallResponse(response.StatusCode, payload)
	if response.StatusCode == http.StatusUnauthorized {
		result.Status = CallStatusRefused
		result.Code = CallCodeTokenStale
		if result.Message == "" {
			result.Message = "target rejected the write token (rotated?)"
		}
		result.HTTPStatus = http.StatusForbidden
		return result, true, nil
	}
	return result, false, nil
}

// decodeCallResponse 把被调方响应折成 CallResult：优先读信封（status/code/
// elapsed/duplicate/result），信封不可解析时退回 HTTP 状态映射 + 原文。
func decodeCallResponse(httpStatus int, payload []byte) *CallResult {
	result := &CallResult{HTTPStatus: httpStatus}
	var envelope CallEnvelope
	if err := json.Unmarshal(payload, &envelope); err == nil && strings.TrimSpace(envelope.Status) != "" {
		result.Status = envelope.Status
		result.Code = envelope.Code
		result.Message = envelope.Message
		result.Duplicate = envelope.Duplicate
		result.Result = envelope.Result
		if result.Result == nil && len(payload) > 0 {
			result.Result = append(json.RawMessage(nil), payload...)
		}
		return result
	}
	result.Status = CallStatusFromHTTP(httpStatus)
	if result.Status != CallStatusOK {
		result.Code = CallCodeUpstream
	}
	result.Message = summarizeCallBody(payload)
	result.Result = json.RawMessage(nil)
	return result
}

// summarizeCallBody 把非信封响应体压成一行提示（不含令牌：被调方的错误体里
// 不会有令牌，这里也只取前若干字符）。
func summarizeCallBody(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return "empty response"
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// callTokenOf 读取档案里的令牌原文（0600 文件；空令牌表示开发模式免令牌）。
func callTokenOf(record NodeRecord) string {
	if record.Auth == nil {
		return ""
	}
	return strings.TrimSpace(record.Auth.Token)
}

// readCallRecord 重读一次目标档案（令牌轮换恢复路径）。
func readCallRecord(path, nodeID string) (NodeRecord, error) {
	file := ReadNodeFile(path)
	if file.Record == nil {
		if file.Err != nil {
			return NodeRecord{}, file.Err
		}
		return NodeRecord{}, fmt.Errorf("node %s record unreadable", nodeID)
	}
	if file.Record.NodeID != nodeID {
		return NodeRecord{}, fmt.Errorf("node %s record replaced by %s", nodeID, file.Record.NodeID)
	}
	return *file.Record, nil
}

// appendCallSent 写调用方一侧的 journal（§5.6 要点 4）：只记 op、目标、状态、
// 耗时与尝试次数，绝不记 args。
func (h *Host) appendCallSent(op, targetNodeID string, result *CallResult) {
	if h == nil || result == nil {
		return
	}
	detail := map[string]any{
		"op":         op,
		"target":     targetNodeID,
		"status":     result.Status,
		"elapsed_ms": result.ElapsedMs,
	}
	if result.Code != "" {
		detail["code"] = result.Code
	}
	if result.Attempts > 1 {
		detail["attempts"] = result.Attempts
	}
	if result.Duplicate {
		detail["duplicate"] = true
	}
	h.Journal().Append(JournalCallSent, "", detail)
}

// elapsedMs 是 §5.6 信封里的 elapsed_ms（毫秒）。
func elapsedMs(started time.Time) int64 {
	return time.Since(started).Milliseconds()
}

// isTimeoutErr 判定传输层错误是否属于超时（含 context deadline）。
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) {
		return timeoutErr.Timeout()
	}
	return false
}

// callHTTPClient 是调用方使用的 HTTP 客户端：与 peer 订阅共用同一传输层
// 配置（拨号/空闲超时），但**不设** Client.Timeout——每次调用用自己的
// context 截止时间（§5.9：call 默认 130s，可覆盖）。
func callHTTPClient() *http.Client {
	return &http.Client{Transport: defaultPeerTransport()}
}
