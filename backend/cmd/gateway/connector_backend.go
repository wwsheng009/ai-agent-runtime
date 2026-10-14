package main

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

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// 本文件是 gateway 宿主的连接器 Backend（docs/bots/06-target-architecture.md
// §2.2、docs/bots/03-host-integration.md §6）：
//
// Backend=gateway_node 不直接跑 agent，而是把每条 IM 消息经反向隧道投递到某个
// 在线节点的同步远程调用端点 POST /web/api/invoke（aicli 节点实现见
// cmd/aicli/commands/web_invoke.go：注入 prompt、等待 turn 结束并在同一响应返回
// 最终 assistant 回复）。
//
// 节点鉴权：节点侧的隧道代理（internal/gateway/agent/forward.go）在转发时注入
// 节点本机写令牌 X-AICLI-Token，令牌不出本机；因此 gateway 侧不需要（也不应该）
// 持有节点令牌，只需保证请求落在节点注册的 /web/api/{invoke,...} 路由白名单内。
//
// 目标节点解析（优先级）：
//  1. 连接器 settings["node_id"]（管理面提交的未知键会落进 settings，可透传）；
//  2. registry 中恰好一个非 stale（在线）节点时自动选中；
//  3. 0 个在线节点或多于 1 个且未指定时返回明确错误（不猜）。
const (
	// gatewayNodeBackendName 是 Backend.Name() 的取值（配置/日志可见）。
	gatewayNodeBackendName = "gateway_node"
	// gatewayNodeInvokePath 是节点侧同步远程调用端点（必须在节点注册的路由内）。
	gatewayNodeInvokePath = "/web/api/invoke"
	// gatewayNodeSettingKey 是指定目标节点的连接器设置键。
	gatewayNodeSettingKey = "node_id"
	// gatewayNodeInvokeDefaultTimeout 是单次节点调用的默认上限；节点侧
	// /web/api/invoke 自身最长等待 10 分钟，隧道代理转发上限 5 分钟，
	// 这里取 180s 作为网关侧默认（连接器场景不应无限等待）。
	gatewayNodeInvokeDefaultTimeout = 180 * time.Second
	// gatewayNodeInvokeMaxTimeout 是网关侧允许的最长等待上限。
	gatewayNodeInvokeMaxTimeout = 300 * time.Second
	// gatewayNodeInvokeMaxResponseBytes 限制节点响应读取上限。
	gatewayNodeInvokeMaxResponseBytes = 4 << 20
)

// gatewayNodeRegistry 是 Backend 需要的节点注册表视图（*tunnel.Registry 实现）。
type gatewayNodeRegistry interface {
	List() []*tunnel.Node
	Get(nodeID string) (*tunnel.Node, bool)
}

// gatewayNodeProxy 是请求进入隧道的入口（*tunnel.Server 实现）；测试注入假代理。
type gatewayNodeProxy interface {
	ServeProxy(w http.ResponseWriter, r *http.Request, nodeID, path string)
}

// gatewayConnectorStore 是 Backend 读取连接器设置的最小存储视图
// （connectors.Store 实现）。
type gatewayConnectorStore interface {
	GetConfig(id string) (connectors.Config, bool)
}

// gatewayNodeBackend 把连接器消息投递到节点，并把最终 assistant 回复映射成
// connectors.BackendResult（会话归属带 NodeID）。
type gatewayNodeBackend struct {
	registry gatewayNodeRegistry
	proxy    gatewayNodeProxy
	store    gatewayConnectorStore
	timeout  time.Duration
}

// newGatewayNodeBackend 构造 gateway_node Backend（timeout<=0 用默认 180s）。
func newGatewayNodeBackend(registry gatewayNodeRegistry, proxy gatewayNodeProxy, store gatewayConnectorStore) *gatewayNodeBackend {
	return &gatewayNodeBackend{
		registry: registry,
		proxy:    proxy,
		store:    store,
		timeout:  gatewayNodeInvokeDefaultTimeout,
	}
}

// Name 实现 connectors.Backend。
func (b *gatewayNodeBackend) Name() string { return gatewayNodeBackendName }

// Handle 实现 connectors.Backend：选节点 → POST /web/api/invoke → 解析终态回复。
func (b *gatewayNodeBackend) Handle(ctx context.Context, req connectors.BackendRequest, _ connectors.ReplySink) (connectors.BackendResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return connectors.BackendResult{}, nil
	}
	nodeID, err := b.pickNode(req.ConnectorID)
	if err != nil {
		return connectors.BackendResult{}, err
	}

	// 只在绑定会话仍属于本次选中的节点时透传 session_id：节点切换后旧会话 id
	// 可能不存在（aicli 会 409/404），交给节点按新会话处理更安全。
	sessionID := ""
	if req.Session.SessionID != "" && (req.Session.NodeID == "" || req.Session.NodeID == nodeID) {
		sessionID = req.Session.SessionID
	}

	timeout := b.invokeTimeout()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := b.invoke(callCtx, nodeID, sessionID, text, timeout)
	if err != nil {
		return connectors.BackendResult{Session: connectors.SessionRef{NodeID: nodeID, SessionID: sessionID}}, err
	}

	resultSession := strings.TrimSpace(resp.SessionID)
	if resultSession == "" {
		resultSession = sessionID
	}
	result := connectors.BackendResult{
		Session: connectors.SessionRef{NodeID: nodeID, SessionID: resultSession},
	}
	if resp.Assistant != nil {
		if reply := strings.TrimSpace(resp.Assistant.Content); reply != "" {
			result.Reply = []connectors.OutboundMessage{{Text: reply}}
		}
	}

	switch resp.Status {
	case "completed", "settled":
		return result, nil
	default:
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "无原因说明"
		}
		return result, fmt.Errorf("节点 %s invoke 未完成：status=%s reason=%s", nodeID, orUnknown(resp.Status), reason)
	}
}

// commandNode 解析命令目标节点（与 Handle 同一套 pickNode 语义）。
func (b *gatewayNodeBackend) commandNode(connectorID string) (string, error) {
	return b.pickNode(connectorID)
}

// commandSession 目标会话：绑定会话仅在仍归属本节点时透传（与 Handle 同语义）。
func commandSession(req connectors.CommandRequest, nodeID string) string {
	if req.Session.SessionID != "" && (req.Session.NodeID == "" || req.Session.NodeID == nodeID) {
		return req.Session.SessionID
	}
	return ""
}

// invokePrompt 同步调用节点 invoke 并返回响应（Handle 与命令面共用）。
func (b *gatewayNodeBackend) invokePrompt(ctx context.Context, nodeID, sessionID, text string) (*gatewayNodeInvokeResponse, error) {
	timeout := b.invokeTimeout()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.invoke(callCtx, nodeID, sessionID, text, timeout)
}

// commandReply 把 invoke 响应映射为 CommandResult：completed/settled 视为
// 命令已受理（Handled=true，回复取 assistant 文案或给定默认）；其余状态
// Handled=false + 明确原因（核心会原样回给 IM，不落 agent turn）。
func (b *gatewayNodeBackend) commandReply(nodeID string, resp *gatewayNodeInvokeResponse, okText string) (connectors.CommandResult, error) {
	if resp == nil {
		return connectors.CommandResult{}, errors.New("gateway_node: empty invoke response")
	}
	switch resp.Status {
	case "completed", "settled":
		text := okText
		if resp.Assistant != nil {
			if reply := strings.TrimSpace(resp.Assistant.Content); reply != "" {
				text = reply
			}
		}
		return connectors.CommandResult{Handled: true, Reply: []connectors.OutboundMessage{{Text: text}}}, nil
	default:
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "无原因说明"
		}
		return connectors.CommandResult{Handled: false, Reply: []connectors.OutboundMessage{
			{Text: fmt.Sprintf("节点 %s 命令未完成：status=%s reason=%s", nodeID, orUnknown(resp.Status), reason)},
		}}, nil
	}
}

// DecideApproval 实现 connectors.CommandBackend（FR-7）：/approve /deny 经隧道
// 转发为节点 invoke prompt（节点侧 aicli/runtime-server 由命令解析走各自审批
// 通道——runtimeapi connector 命令契约与 IM 命令同形：/approve [request_id]）。
// 无 request_id 时转发原样参数（节点侧解析当前挂起项）。
func (b *gatewayNodeBackend) DecideApproval(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	nodeID, err := b.commandNode(req.ConnectorID)
	if err != nil {
		return connectors.CommandResult{}, err
	}
	prompt := "/" + strings.TrimSpace(strings.ToLower(req.Command))
	for _, arg := range req.Args {
		prompt += " " + strings.TrimSpace(arg)
	}
	resp, err := b.invokePrompt(ctx, nodeID, commandSession(req, nodeID), prompt)
	if err != nil {
		return connectors.CommandResult{}, err
	}
	allow := !strings.EqualFold(strings.TrimSpace(req.Command), "deny")
	okText := "已拒绝。"
	if allow {
		okText = "已批准。"
	}
	return b.commandReply(nodeID, resp, okText)
}

// AnswerQuestion 实现 connectors.CommandBackend（FR-7）：/answer <序号|id> <内容>
// 经隧道转发为节点 invoke prompt（与 runtimeapi connector 命令契约同形）。
func (b *gatewayNodeBackend) AnswerQuestion(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	nodeID, err := b.commandNode(req.ConnectorID)
	if err != nil {
		return connectors.CommandResult{}, err
	}
	prompt := "/answer"
	for _, arg := range req.Args {
		prompt += " " + strings.TrimSpace(arg)
	}
	resp, err := b.invokePrompt(ctx, nodeID, commandSession(req, nodeID), prompt)
	if err != nil {
		return connectors.CommandResult{}, err
	}
	return b.commandReply(nodeID, resp, "已回答。")
}

// pickNode 解析目标节点（语义见文件头注释）。
func (b *gatewayNodeBackend) pickNode(connectorID string) (string, error) {
	if b == nil || b.registry == nil || b.proxy == nil {
		return "", errors.New("gateway_node 未接线：缺少节点注册表或隧道代理")
	}
	explicit := ""
	if b.store != nil {
		if cfg, ok := b.store.GetConfig(strings.TrimSpace(connectorID)); ok {
			explicit = strings.TrimSpace(cfg.Settings[gatewayNodeSettingKey])
		}
	}
	if explicit != "" {
		node, ok := b.registry.Get(explicit)
		if !ok {
			return "", fmt.Errorf("连接器 %s 指定的 node_id=%q 未注册（节点不在线）", connectorID, explicit)
		}
		if node == nil || node.Stale {
			return "", fmt.Errorf("连接器 %s 指定的 node_id=%q 是离线快照，节点不在线", connectorID, explicit)
		}
		return explicit, nil
	}

	live := make([]string, 0, 2)
	for _, node := range b.registry.List() {
		if node != nil && !node.Stale && strings.TrimSpace(node.NodeID) != "" {
			live = append(live, node.NodeID)
		}
	}
	switch len(live) {
	case 0:
		return "", errors.New("无在线节点：连接器消息没有可投递的 agent 节点（请启动 aicli/runtime-server 节点并连上网关）")
	case 1:
		return live[0], nil
	default:
		return "", fmt.Errorf("有 %d 个在线节点（%s）：请在连接器设置中指定 node_id", len(live), strings.Join(live, "、"))
	}
}

// invokeTimeout 返回本次调用的时间上限（上限 5m；<=0 用默认 180s）。
func (b *gatewayNodeBackend) invokeTimeout() time.Duration {
	timeout := gatewayNodeInvokeDefaultTimeout
	if b != nil && b.timeout > 0 {
		timeout = b.timeout
	}
	if timeout > gatewayNodeInvokeMaxTimeout {
		timeout = gatewayNodeInvokeMaxTimeout
	}
	return timeout
}

// gatewayNodeInvokeRequest 是 POST /web/api/invoke 的请求体（aicli 契约子集）。
type gatewayNodeInvokeRequest struct {
	Prompt    string `json:"prompt"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	// SessionID 指定节点上的目标会话；空 = 节点使用当前活动会话。
	SessionID string `json:"session_id,omitempty"`
}

// gatewayNodeInvokeResponse 是 /web/api/invoke 响应体的解析子集
// （完整契约见 cmd/aicli/commands/web_invoke.go）。
type gatewayNodeInvokeResponse struct {
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Assistant *struct {
		Role    string `json:"role,omitempty"`
		Content string `json:"content,omitempty"`
	} `json:"assistant,omitempty"`
}

// invoke 经隧道反向代理同步调用节点端点，并解析 JSON 响应。
func (b *gatewayNodeBackend) invoke(ctx context.Context, nodeID, sessionID, prompt string, timeout time.Duration) (*gatewayNodeInvokeResponse, error) {
	payload, err := json.Marshal(gatewayNodeInvokeRequest{
		Prompt:    prompt,
		TimeoutMs: int(timeout / time.Millisecond),
		SessionID: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("编码节点请求失败：%w", err)
	}
	// URL 里的主机名不参与转发（ServeProxy 只使用 path/query/header/body）；
	// 使用固定占位主机而不是拼接网关自身地址，避免凭据/来源混淆。
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gateway-node.gateway"+gatewayNodeInvokePath, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造节点请求失败：%w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	recorder := &proxyRecorder{header: make(http.Header)}
	b.proxy.ServeProxy(recorder, httpReq, nodeID, gatewayNodeInvokePath)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("节点 %s 调用超时/取消（上限 %s）：%w", nodeID, timeout, ctxErr)
	}
	if recorder.status == 0 {
		return nil, fmt.Errorf("节点 %s 未返回任何响应（代理中断）", nodeID)
	}
	if recorder.status != http.StatusOK {
		return nil, fmt.Errorf("节点 %s 返回 HTTP %d：%s", nodeID, recorder.status, firstResponseLine(recorder.body.String()))
	}
	var resp gatewayNodeInvokeResponse
	if err := json.Unmarshal(recorder.body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("节点 %s 响应不是合法 JSON：%w", nodeID, err)
	}
	if strings.TrimSpace(resp.Status) == "" {
		return nil, fmt.Errorf("节点 %s 响应缺少 status 字段", nodeID)
	}
	return &resp, nil
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// firstResponseLine 把错误响应体压成单行短文本（避免把整页错误塞进 IM 回复）。
func firstResponseLine(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "（空响应体）"
	}
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(line)
	const maxLen = 200
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	return line
}

// proxyRecorder 是 ServeProxy 的最小 ResponseWriter：收集状态/头/响应体。
// *tunnel.Server.ServeProxy 是同步的（stream 结束才返回），因此调用方可直接读取。
type proxyRecorder struct {
	header  http.Header
	status  int
	body    bytes.Buffer
	written int64
}

func (r *proxyRecorder) Header() http.Header { return r.header }

func (r *proxyRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *proxyRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.written+int64(len(data)) > gatewayNodeInvokeMaxResponseBytes {
		return 0, io.ErrShortWrite
	}
	n, err := r.body.Write(data)
	r.written += int64(n)
	return n, err
}

// Flush 满足 http.Flusher（ServeProxy 对流式响应调用）；本实现是全量缓冲。
func (r *proxyRecorder) Flush() {}
