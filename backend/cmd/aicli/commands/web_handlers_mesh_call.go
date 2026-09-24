package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// ============================================================================
// POST /web/api/mesh/call —— 被调方一侧（架构 §5.6 / §5.9，实施方案 S8）
//
// 契约：
//   - op 白名单 9 项（mesh.CallOpSpecFor）；写操作必须 allow_write=true；
//   - 调用者必须是回环（非回环默认拒绝，§5.8），令牌由统一鉴权层校验；
//   - 分发**复用既有端点实现**（同一批 Handle* 函数，走内部请求 + 记录器），
//     不产生第二套语义：幂等键、错误码、字段结构都与直接调用端点一致；
//   - 审计只记 op 与耗时，绝不记 args 正文（args 可能含用户 prompt）；
//   - 响应统一信封 {status, code, node_id, op, elapsed_ms, duplicate, result}。
//
// 降级契约（§4.7）：网格关闭时端点通常未注册；真被调用到也只回 refused，
// 不 panic、不 5xx。
// ============================================================================

// chatWebMeshCallSwitch 是 §9.3 的跨工作区收敛开关：默认关闭（跨工作区默认
// 放行），仅显式开启时拒绝，且只拒绝**写操作**（只读跨工作区与同工作区无
// 差别）。进程级开关由根命令的 `--mesh-restrict-workspace` 接线（main.go）。
var chatWebMeshCallSwitch = struct {
	mu       sync.RWMutex
	restrict bool
}{}

// SetChatWebMeshRestrictWorkspace 设置跨工作区收敛开关（默认 false）。
func SetChatWebMeshRestrictWorkspace(enabled bool) {
	chatWebMeshCallSwitch.mu.Lock()
	defer chatWebMeshCallSwitch.mu.Unlock()
	chatWebMeshCallSwitch.restrict = enabled
}

// ChatWebMeshRestrictWorkspace 读取跨工作区收敛开关的当前值。
func ChatWebMeshRestrictWorkspace() bool {
	chatWebMeshCallSwitch.mu.RLock()
	defer chatWebMeshCallSwitch.mu.RUnlock()
	return chatWebMeshCallSwitch.restrict
}

// HandleChatWebAPIMeshCall 处理 POST /web/api/mesh/call（架构 §5.6）。
func HandleChatWebAPIMeshCall(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeChatWebMeshCallError(w, http.StatusMethodNotAllowed, mesh.CallStatusError,
			"", "method not allowed: use POST "+ChatWebAPIMeshCallPath, started)
		return
	}
	host := mesh.Current()
	if host == nil {
		writeChatWebMeshCallError(w, http.StatusForbidden, mesh.CallStatusRefused,
			mesh.CallCodeMeshDisabled, "mesh disabled (start with --mesh)", started)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, mesh.CallMaxBodyBytes+1))
	if err != nil {
		writeChatWebMeshCallError(w, http.StatusBadRequest, mesh.CallStatusError,
			"", "read body: "+err.Error(), started)
		return
	}
	if len(payload) > mesh.CallMaxBodyBytes {
		writeChatWebMeshCallError(w, http.StatusRequestEntityTooLarge, mesh.CallStatusError,
			mesh.CallCodeBodyTooLarge, fmt.Sprintf("body exceeds %d bytes", mesh.CallMaxBodyBytes), started)
		return
	}
	var req mesh.CallRequestBody
	if err := json.Unmarshal(payload, &req); err != nil {
		writeChatWebMeshCallError(w, http.StatusBadRequest, mesh.CallStatusError,
			"", "invalid JSON body: "+err.Error(), started)
		return
	}
	op := strings.TrimSpace(req.Op)
	if _, known := mesh.CallOpSpecFor(op); !known {
		writeChatWebMeshCallError(w, http.StatusBadRequest, mesh.CallStatusError,
			mesh.CallCodeUnknownOp, fmt.Sprintf("unknown op %q", op), started)
		return
	}
	// 调用者必须来自回环：跨机默认拒绝，显式 --mesh-allow-nonloopback=true 才
	// 放宽（§5.8 / §9.4）；写操作与逐次 allow_write 的要求都不受影响。
	if !chatWebMeshWritePathAllowed(r) {
		writeChatWebMeshCallError(w, http.StatusForbidden, mesh.CallStatusRefused,
			mesh.CallCodeNonLoopback, "mesh calls are loopback-only (--mesh-allow-nonloopback=true relaxes this)", started)
		return
	}
	// 写操作每次都要显式允许，无隐式放行（§5.6 要点 2）。
	if mesh.CallOpIsWrite(op) && !req.AllowWrite {
		writeChatWebMeshCallError(w, http.StatusForbidden, mesh.CallStatusRefused,
			mesh.CallCodeWriteNotAllowed, fmt.Sprintf("op %s is a write op; set allow_write=true", op), started)
		return
	}
	// 防串线自检：形如 node-* 的 target 必须等于本进程 node_id。
	if target := strings.TrimSpace(req.Target); strings.HasPrefix(target, "node-") && target != host.NodeID() {
		writeChatWebMeshCallError(w, http.StatusNotFound, mesh.CallStatusNotFound,
			mesh.CallCodeTargetMismatch, fmt.Sprintf("this node is %s, not %s", host.NodeID(), target), started)
		return
	}
	callerNodeID := strings.TrimSpace(r.Header.Get(mesh.CallerNodeHeader))
	if code, message, denied := chatWebMeshCallWorkspaceDenied(host, callerNodeID, op); denied {
		writeChatWebMeshCallError(w, http.StatusForbidden, mesh.CallStatusRefused, code, message, started)
		return
	}
	// 审计（收到）：只记 op 与调用者，不记 args（§5.6 要点 4）。
	host.Journal().Append(mesh.JournalCallReceived, "", map[string]any{
		"op":     op,
		"caller": callerNodeID,
	})
	// 实时帧（§5.5）：本节点被调用开始。只带 op 与调用者——args 可能含用户
	// prompt，绝不进帧；扇入禁用/关闭时静默丢弃，绝不阻塞调用路径。
	invoked := map[string]any{"op": op}
	if callerNodeID != "" {
		invoked["caller"] = callerNodeID
	}
	host.Fanin().PublishLocal(mesh.FrameCallInvoked, invoked)

	status, code, body := chatWebMeshCallDispatch(op, req.Args, strings.TrimSpace(req.ClientRequestID))
	elapsed := time.Since(started).Milliseconds()
	duplicate := chatWebMeshCallDuplicate(body)
	detail := map[string]any{"op": op, "status": status, "elapsed_ms": elapsed}
	if code != "" {
		detail["code"] = code
	}
	host.Journal().Append(mesh.JournalCallCompleted, "", detail)
	completed := map[string]any{"op": op, "status": status, "elapsed_ms": elapsed}
	if code != "" {
		completed["code"] = code
	}
	if duplicate {
		completed["duplicate"] = true
	}
	host.Fanin().PublishLocal(mesh.FrameCallCompleted, completed)

	envelope := mesh.CallEnvelope{
		SchemaVersion: mesh.SchemaVersion,
		Status:        status,
		Code:          code,
		NodeID:        host.NodeID(),
		Op:            op,
		ElapsedMs:     elapsed,
		Duplicate:     duplicate,
		Result:        chatWebMeshCallResultBody(body),
	}
	if status != mesh.CallStatusOK {
		envelope.Message = chatWebMeshCallMessage(body)
	}
	writeWebAPIJSON(w, mesh.CallHTTPStatus(status), envelope)
}

// chatWebMeshCallWorkspaceDenied 判定 §9.3 的跨工作区拒绝：默认放行；仅收敛
// 开关开启、且这是**写操作**、且调用方工作区与本进程不同时拒绝（只读跨工作
// 区与同工作区无差别）。写操作下调用方档案不可读（或不是本机节点）时按拒绝
// 处理（fail-closed），因为此时无法证明「同工作区」。
func chatWebMeshCallWorkspaceDenied(host *mesh.Host, callerNodeID, op string) (code, message string, denied bool) {
	if host == nil || !ChatWebMeshRestrictWorkspace() {
		return "", "", false
	}
	if !mesh.CallOpIsWrite(op) {
		return "", "", false
	}
	self := ""
	if record := host.RecordSnapshot(); record.Workspace != nil {
		self = strings.TrimSpace(record.Workspace.Path)
	}
	if callerNodeID == "" {
		return mesh.CallCodeCrossWorkspace, "mesh calls from unknown callers are denied while --mesh-restrict-workspace is on", true
	}
	callerWorkspace := ""
	for _, node := range mesh.BuildView(host.Paths(), mesh.ViewOptions{SelfNodeID: host.NodeID()}).Nodes {
		if node.NodeID == callerNodeID {
			if node.Workspace != nil {
				callerWorkspace = strings.TrimSpace(node.Workspace.Path)
			}
			break
		}
	}
	if callerWorkspace == "" || !strings.EqualFold(callerWorkspace, self) {
		return mesh.CallCodeCrossWorkspace, fmt.Sprintf("caller %s is in a different workspace", callerNodeID), true
	}
	return "", "", false
}

// writeChatWebMeshCallError 写 §5.9 的统一错误信封（非 2xx 与 refused/error
// 同形：{status, code, message, node_id?}）。
func writeChatWebMeshCallError(w http.ResponseWriter, httpStatus int, status, code, message string, started time.Time) {
	nodeID := ""
	if host := mesh.Current(); host != nil {
		nodeID = host.NodeID()
	}
	writeWebAPIJSON(w, httpStatus, mesh.CallEnvelope{
		SchemaVersion: mesh.SchemaVersion,
		Status:        status,
		Code:          code,
		Message:       message,
		NodeID:        nodeID,
		ElapsedMs:     time.Since(started).Milliseconds(),
	})
}

// ============================================================================
// 本地分发：复用既有端点实现
// ============================================================================

// chatWebMeshCallPlan 是一次本地分发的请求计划：方法 + 目标路径 + 查询参数 +
// 请求体。目标端点自己的处理器仍是唯一实现，网格层只做转发。
type chatWebMeshCallPlan struct {
	method string
	path   string
	query  url.Values
	body   []byte
}

// chatWebMeshCallDispatch 把一次网格调用分发到本地端点，返回 §5.6 的状态、
// 原因码与端点原始响应体。
func chatWebMeshCallDispatch(op string, args json.RawMessage, clientRequestID string) (status, code string, body []byte) {
	plan, err := chatWebMeshCallPlanFor(op, args, clientRequestID)
	if err != nil {
		return mesh.CallStatusError, mesh.CallCodeUpstream, []byte(err.Error())
	}
	handler, ok := chatWebMeshCallHandler(plan.path)
	if !ok {
		return mesh.CallStatusError, mesh.CallCodeUpstream, []byte("no local handler for " + plan.path)
	}
	recorder := newChatWebMeshCallRecorder()
	handler(recorder, chatWebMeshCallInternalRequest(plan))
	responseBody := recorder.body.Bytes()
	return chatWebMeshCallStatusFromResponse(recorder.statusCode(), responseBody), "", responseBody
}

// chatWebMeshCallHandler 返回目标端点的处理器（与 mux 注册的是同一个函数）。
func chatWebMeshCallHandler(path string) (func(http.ResponseWriter, *http.Request), bool) {
	switch path {
	case ChatWebAPIMeshSelfPath:
		return HandleChatWebAPIMeshSelf, true
	case ChatWebAPIStatusPath:
		return HandleChatWebAPIStatus, true
	case ChatWebAPIScreenPath:
		return HandleChatWebAPIScreen, true
	case ChatWebAPITurnPath:
		return HandleChatWebAPITurn, true
	case ChatWebAPISessionsPath:
		return HandleChatWebAPISessions, true
	case ChatWebAPIInvokePath:
		return HandleChatWebAPIInvoke, true
	case ChatWebAPIInputPath:
		return HandleChatWebAPIInput, true
	case ChatWebAPISessionsResumePath:
		return HandleChatWebAPISessionsResume, true
	default:
		return nil, false
	}
}

// chatWebMeshCallPlanFor 按 op 白名单构造分发计划（§5.6 表格的 args 列）。
//
// 参数按白名单逐项搬运：未列出的键被丢弃而不是透传，避免网格层成为「任意
// 端点参数」的旁路。
func chatWebMeshCallPlanFor(op string, args json.RawMessage, clientRequestID string) (chatWebMeshCallPlan, error) {
	switch op {
	case "node.info":
		return chatWebMeshCallPlan{method: http.MethodGet, path: ChatWebAPIMeshSelfPath}, nil
	case "status":
		return chatWebMeshCallPlan{method: http.MethodGet, path: ChatWebAPIStatusPath}, nil
	case "sessions.list":
		return chatWebMeshCallPlan{method: http.MethodGet, path: ChatWebAPISessionsPath}, nil
	case "screen":
		query := url.Values{}
		copyChatWebMeshCallArg(query, args, "view")
		copyChatWebMeshCallArg(query, args, "tail")
		copyChatWebMeshCallArg(query, args, "format")
		return chatWebMeshCallPlan{method: http.MethodGet, path: ChatWebAPIScreenPath, query: query}, nil
	case "turn":
		query := url.Values{}
		copyChatWebMeshCallArg(query, args, "id")
		return chatWebMeshCallPlan{method: http.MethodGet, path: ChatWebAPITurnPath, query: query}, nil
	case "invoke":
		body, err := chatWebMeshCallSubset(args, "prompt", "wait_only", "timeout_ms", "session_id")
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		// 幂等键透传（§5.6 要点 3）：网格层不重复实现幂等。
		if clientRequestID != "" {
			body["client_request_id"] = clientRequestID
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		return chatWebMeshCallPlan{method: http.MethodPost, path: ChatWebAPIInvokePath, body: encoded}, nil
	case "input":
		body, err := chatWebMeshCallSubset(args, "type", "prompt", "request_id", "allow", "question_id", "answer", "discard_pending")
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		return chatWebMeshCallPlan{method: http.MethodPost, path: ChatWebAPIInputPath, body: encoded}, nil
	case "cancel":
		body := map[string]any{"type": "interrupt"}
		if raw, ok := chatWebMeshCallArg(args, "discard_pending"); ok {
			body["discard_pending"] = json.RawMessage(raw)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		return chatWebMeshCallPlan{method: http.MethodPost, path: ChatWebAPIInputPath, body: encoded}, nil
	case "sessions.resume":
		body, err := chatWebMeshCallSubset(args, "session_id")
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return chatWebMeshCallPlan{}, err
		}
		return chatWebMeshCallPlan{method: http.MethodPost, path: ChatWebAPISessionsResumePath, body: encoded}, nil
	default:
		return chatWebMeshCallPlan{}, fmt.Errorf("unknown op %q", op)
	}
}

// chatWebMeshCallArg 取一个原始参数（缺失返回 ok=false）。非法 JSON 对象按
// 「无参数」处理：参数形状问题由目标端点自己判（400 由端点语义决定）。
func chatWebMeshCallArg(args json.RawMessage, key string) (json.RawMessage, bool) {
	if len(args) == 0 {
		return nil, false
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(args, &object); err != nil {
		return nil, false
	}
	raw, ok := object[key]
	if !ok {
		return nil, false
	}
	return raw, true
}

// copyChatWebMeshCallArg 把字符串参数搬进查询串（仅接受 JSON 字符串/数字，
// 其它类型忽略）。
func copyChatWebMeshCallArg(query url.Values, args json.RawMessage, key string) {
	raw, ok := chatWebMeshCallArg(args, key)
	if !ok {
		return
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		var number json.Number
		if numberErr := json.Unmarshal(raw, &number); numberErr != nil {
			return
		}
		text = number.String()
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	query.Set(key, text)
}

// chatWebMeshCallSubset 按白名单搬运请求体参数（未列出的键丢弃）。
func chatWebMeshCallSubset(args json.RawMessage, keys ...string) (map[string]any, error) {
	body := map[string]any{}
	if len(args) == 0 {
		return body, nil
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(args, &object); err != nil {
		return nil, fmt.Errorf("args must be a JSON object: %w", err)
	}
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			body[key] = json.RawMessage(raw)
		}
	}
	return body, nil
}

// chatWebMeshCallInternalRequest 构造内部请求：回环 RemoteAddr + 本进程令牌。
//
// 走到这里时调用已在网格层完成鉴权（回环 + 令牌 + op 白名单 + allow_write），
// 因此内部请求按「本机可信」构造，端点自身的回环判定（§9.1 reveal_token 等）
// 仍按回环处理，但网格层从不传 reveal_token——令牌绝不因 mesh 调用外泄。
func chatWebMeshCallInternalRequest(plan chatWebMeshCallPlan) *http.Request {
	target := "http://127.0.0.1" + plan.path
	if len(plan.query) > 0 {
		target += "?" + plan.query.Encode()
	}
	var body io.Reader
	if len(plan.body) > 0 {
		body = bytes.NewReader(plan.body)
	}
	request, err := http.NewRequest(plan.method, target, body)
	if err != nil {
		// 只可能因方法/URL 非法失败：构造一个空请求让目标端点回 4xx。
		request, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1"+plan.path, nil)
	}
	if request == nil {
		request = &http.Request{Method: http.MethodGet, URL: &url.URL{Path: plan.path}, Header: http.Header{}}
	}
	request.RemoteAddr = "127.0.0.1:0"
	request.Host = "127.0.0.1"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Requested-With", "aicli-mesh-call")
	if token := strings.TrimSpace(EnsureChatWebAuthToken()); token != "" {
		request.Header.Set(mesh.CallTokenHeader, token)
	}
	return request
}

// chatWebMeshCallStatusFromResponse 把端点响应折成 §5.6 状态（§5.9 的 HTTP
// 映射：2xx→ok、409→busy、404→not_found、403→refused、其余→error）。
func chatWebMeshCallStatusFromResponse(code int, body []byte) string {
	switch {
	case code >= 200 && code < 300:
		return mesh.CallStatusOK
	case code == http.StatusConflict:
		return mesh.CallStatusBusy
	case code == http.StatusNotFound:
		return mesh.CallStatusNotFound
	case code == http.StatusForbidden || code == http.StatusUnauthorized:
		return mesh.CallStatusRefused
	default:
		return mesh.CallStatusError
	}
}

// chatWebMeshCallDuplicate 从端点响应里提取 duplicate（仅 invoke 会带）。
func chatWebMeshCallDuplicate(body []byte) bool {
	object := map[string]any{}
	if err := json.Unmarshal(body, &object); err != nil {
		return false
	}
	duplicate, _ := object["duplicate"].(bool)
	return duplicate
}

// chatWebMeshCallMessage 从端点错误体里提取人读提示（reason/message 优先）。
func chatWebMeshCallMessage(body []byte) string {
	object := map[string]any{}
	if err := json.Unmarshal(body, &object); err != nil {
		return strings.TrimSpace(string(body))
	}
	for _, key := range []string{"reason", "message", "error"} {
		if text, ok := object[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return strings.TrimSpace(string(body))
}

// chatWebMeshCallResultBody 把端点响应包成信封的 result：JSON 原样透传，
// 非 JSON 体折成 JSON 字符串（信封必须是合法 JSON）。
func chatWebMeshCallResultBody(body []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if json.Valid(trimmed) {
		return append(json.RawMessage(nil), trimmed...)
	}
	encoded, err := json.Marshal(string(trimmed))
	if err != nil {
		return nil
	}
	return json.RawMessage(encoded)
}

// ============================================================================
// 内部记录器
// ============================================================================

// chatWebMeshCallRecorder 是最小的 http.ResponseWriter 记录器：内部转发不
// 经过网络，只关心端点写了什么状态码与响应体。
//
// 同时实现 http.Flusher（空操作）：SSE 端点不会走到这里（内部请求不带
// Accept: text/event-stream），但实现它可避免未来误接流式端点时 panic。
type chatWebMeshCallRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newChatWebMeshCallRecorder() *chatWebMeshCallRecorder {
	return &chatWebMeshCallRecorder{header: http.Header{}}
}

func (rec *chatWebMeshCallRecorder) Header() http.Header {
	if rec.header == nil {
		rec.header = http.Header{}
	}
	return rec.header
}

func (rec *chatWebMeshCallRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
}

func (rec *chatWebMeshCallRecorder) Write(payload []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.body.Write(payload)
}

// Flush 是 http.Flusher 的空实现（内部转发无下游连接可刷）。
func (rec *chatWebMeshCallRecorder) Flush() {}

// statusCode 返回端点写下的状态码（未显式写头时按 200）。
func (rec *chatWebMeshCallRecorder) statusCode() int {
	if rec.status == 0 {
		return http.StatusOK
	}
	return rec.status
}
