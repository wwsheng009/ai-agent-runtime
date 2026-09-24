package commands

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// ChatWebAPIHealthResponse 是 GET /web/api/health 的响应体（架构 §5.2）。
//
// 契约：不依赖会话、不依赖渲染器——无会话时同样返回 200，仅
// session_active=false。字段被网格探活、外部脚本就绪等待与
// aicli-mesh doctor 复用，因此保持稳定且极轻量（不扫盘、不派生重活）。
type ChatWebAPIHealthResponse struct {
	Available     bool   `json:"available"`
	NodeID        string `json:"node_id,omitempty"`
	PID           int    `json:"pid"`
	UptimeSec     int64  `json:"uptime_sec"`
	SessionActive bool   `json:"session_active"`
	Busy          bool   `json:"busy"`
	MeshReady     bool   `json:"mesh_ready"`
}

// HandleChatWebAPIHealth 处理 GET /web/api/health（架构 §5.2 存活探针）。
//
// 数据源是进程内实时态：网格宿主的内存快照（可用时）+ 当前 chat 会话兜底，
// 因此高频探活也不会触盘或依赖 UI 渲染面。
func HandleChatWebAPIHealth(w http.ResponseWriter, r *http.Request) {
	writeWebAPIJSON(w, http.StatusOK, buildChatWebAPIHealth())
}

func buildChatWebAPIHealth() ChatWebAPIHealthResponse {
	response := ChatWebAPIHealthResponse{
		Available: true,
		PID:       os.Getpid(),
		UptimeSec: int64(time.Since(chatDebugProcessStartedAt).Round(time.Second) / time.Second),
	}
	// 网格宿主存在时以档案内存快照为准（与 peers 视图同源，避免两处口径漂移）；
	// 网格关闭（--mesh=false）或会话尚未同步到网格时，退回进程内 chat 会话。
	if host := mesh.Current(); host != nil {
		record := host.RecordSnapshot()
		response.NodeID = record.NodeID
		response.MeshReady = host.Paths().Enabled()
		if record.Session != nil {
			response.SessionActive = strings.TrimSpace(record.Session.ID) != ""
			response.Busy = record.Session.Busy
		}
	}
	if !response.SessionActive {
		if session := chatWebSession(); session != nil {
			response.SessionActive = strings.TrimSpace(currentRuntimeSessionID(session)) != ""
			if actor := chatWebSessionActor(session); actor != nil {
				if state := actor.State(); state != nil {
					response.Busy = state.Summary().Busy()
				}
			}
		}
	}
	return response
}

// ============================================================================
// 网格控制面只读端点（架构 §5.3 / §5.4，实施方案 S5）
//
// 两条端点都是 internal/mesh 聚合层的薄壳：HTTP 与 `aicli-mesh ls` 消费同一份
// BuildView 输出，不各写一套聚合（§7.5）。降级契约：网格未启用 / 网格根不可读
// 时返回 200 + available=false 或空视图，绝不 5xx（§4.7）。
// ============================================================================

// chatWebMeshPeersJournalTail 是 peers 默认 join 的 journal 事件条数：够 UI 与
// 脚本看清「最近发生了什么」，又不至于每次轮询把整份日志读一遍。
const chatWebMeshPeersJournalTail = 5

// ChatWebAPIMeshSelfDerived 是 self 响应的内存实时段（§5.3）：档案随心跳落盘，
// 这里的值可能更新。
type ChatWebAPIMeshSelfDerived struct {
	Busy          bool   `json:"busy"`
	PendingInputs int    `json:"pending_inputs"`
	TurnID        string `json:"turn_id,omitempty"`
	// PeerCount 是除自己以外的 live 节点数（跨工作区全量口径）。
	PeerCount int `json:"peer_count"`
	// Lease 是会话归属标签：owner / conflict / peer / none（§4.4）。
	Lease string `json:"lease"`
}

// ChatWebAPIMeshSelfMesh 自描述网格根与日志位置，让脚本不用猜目录（§5.3）。
type ChatWebAPIMeshSelfMesh struct {
	Enabled bool   `json:"enabled"`
	Root    string `json:"root,omitempty"`
	Journal string `json:"journal,omitempty"`
}

// HandleChatWebAPIMeshSelf 处理 GET /web/api/mesh/self（架构 §5.3）。
//
// 响应 = 本进程档案（与磁盘 nodes/<id>.json 同形：schema_version/node_id/
// endpoint/session/auth…）+ derived（内存实时值）+ mesh（根目录自描述）。
// auth.token 默认脱敏；只有回环同源 + ?reveal_token=1 才返回原文（§9.1）。
func HandleChatWebAPIMeshSelf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed: use GET " + ChatWebAPIMeshSelfPath,
		})
		return
	}
	host := mesh.Current()
	if host == nil {
		// --mesh=false 时路由通常不注册（§9.7）；真被调用到也只降级不报错。
		writeWebAPIJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "mesh disabled",
		})
		return
	}
	body, ok := chatWebMeshRecordBody(host)
	if !ok {
		writeWebAPIJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "mesh record unavailable",
		})
		return
	}
	body["available"] = true
	body["derived"] = buildChatWebMeshSelfDerived(host)
	body["mesh"] = chatWebMeshSelfMesh(host)
	// 默认脱敏（§9.1）：auth.token → 0f3a…；只有回环同源的显式 reveal 才给原文。
	redactChatWebMeshAuthToken(body, chatWebQueryTruthy(r, "reveal_token") && chatWebRequestIsLoopback(r))
	writeWebAPIJSON(w, http.StatusOK, body)
}

// HandleChatWebAPIMeshPeers 处理 GET /web/api/mesh/peers（架构 §5.4）。
//
// 硬契约：默认 scope=all（跨工作区全量）；counts 恒全量口径，只有 nodes[] 受
// scope/workspace/state 过滤影响；probe=0（默认）不发任何网络请求；网格根不可读
// 时返回 200 + 空视图（绝不 5xx）。
func HandleChatWebAPIMeshPeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed: use GET " + ChatWebAPIMeshPeersPath,
		})
		return
	}
	opts := mesh.ViewOptions{
		Probe:            chatWebQueryTruthy(r, "probe"),
		Filter:           chatWebMeshPeersFilter(r),
		JournalTailLimit: chatWebMeshPeersJournalTail,
	}
	paths := mesh.ResolvePaths()
	if host := mesh.Current(); host != nil {
		opts.SelfNodeID = host.NodeID()
		paths = host.Paths()
	}
	view := mesh.BuildView(paths, opts)
	// redact_token=1 是默认值（M7）；只有显式 redact_token=0 / reveal_token=1
	// 且来自回环时才换成原文（§9.1）。
	if chatWebMeshPeersWantsTokens(r) {
		mesh.RevealTokens(&view)
	}
	writeWebAPIJSON(w, http.StatusOK, view)
}

// chatWebMeshRecordBody 把内存档案快照转成可增补的 JSON 对象：字段名与磁盘档案
// 完全一致（schema_version / node_id / …），不另立一套线上结构。
func chatWebMeshRecordBody(host *mesh.Host) (map[string]any, bool) {
	if host == nil || strings.TrimSpace(host.NodeID()) == "" {
		// 网格根不可用时的 inert host 没有 node_id：按「档案不可用」降级。
		return nil, false
	}
	raw, err := json.Marshal(host.RecordSnapshot())
	if err != nil {
		return nil, false
	}
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false
	}
	return body, true
}

// buildChatWebMeshSelfDerived 汇总内存实时值（§5.3）：会话 actor / 输入队列优先
// （比心跳落盘的档案更新），档案兜底；peer_count 与 lease 走同一份聚合。
func buildChatWebMeshSelfDerived(host *mesh.Host) ChatWebAPIMeshSelfDerived {
	derived := ChatWebAPIMeshSelfDerived{Lease: chatWebMeshLeaseLabel(host, "")}
	record := host.RecordSnapshot()
	if record.Session != nil {
		derived.Busy = record.Session.Busy
		derived.TurnID = strings.TrimSpace(record.Session.TurnID)
	}
	if session := chatWebSession(); session != nil {
		_, turnID, busy, _, _ := chatWebInvokeProbeFn(session)
		derived.Busy = busy
		if turnID = strings.TrimSpace(turnID); turnID != "" {
			derived.TurnID = turnID
		}
		if session.InputQueue != nil {
			derived.PendingInputs = session.InputQueue.queuedSubmissionCount()
		}
	}
	view := mesh.BuildView(host.Paths(), mesh.ViewOptions{SelfNodeID: host.NodeID()})
	if view.Counts.Live > 0 {
		derived.PeerCount = view.Counts.Live - 1
	}
	for _, node := range view.Nodes {
		if node.NodeID == host.NodeID() {
			derived.Lease = chatWebMeshLeaseLabel(host, node.Ownership)
			break
		}
	}
	return derived
}

// chatWebMeshLeaseLabel 把「本进程租约状态 + 视图归属」折算成 §5.3 的 lease 标签。
// 归属冲突或租约被夺都算 conflict：视图与租约是同一事实的两个视角。
func chatWebMeshLeaseLabel(host *mesh.Host, ownership mesh.Ownership) string {
	if host == nil {
		return string(mesh.OwnershipNone)
	}
	status := host.SessionLeaseStatus()
	switch {
	case status.Lost || ownership == mesh.OwnershipConflict:
		return string(mesh.OwnershipConflict)
	case status.Held:
		return string(mesh.OwnershipOwner)
	case ownership != "" && ownership != mesh.OwnershipNone:
		return string(ownership)
	default:
		return string(mesh.OwnershipNone)
	}
}

// chatWebMeshSelfMesh 自描述网格根（§5.3）：脚本不必猜 ~/.aicli/mesh。
func chatWebMeshSelfMesh(host *mesh.Host) ChatWebAPIMeshSelfMesh {
	paths := host.Paths()
	section := ChatWebAPIMeshSelfMesh{Enabled: paths.Enabled(), Root: paths.Root}
	if nodeID := strings.TrimSpace(host.NodeID()); nodeID != "" {
		section.Journal = paths.JournalPath(nodeID)
	}
	return section
}

// redactChatWebMeshAuthToken 落实 §9.1 的默认脱敏：token → `0f3a…` 提示；显式
// reveal（回环同源）时保持原文；空令牌直接删键，避免出现 `"token": ""`。
func redactChatWebMeshAuthToken(body map[string]any, reveal bool) {
	auth, ok := body["auth"].(map[string]any)
	if !ok || auth == nil {
		return
	}
	token, _ := auth["token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		delete(auth, "token")
		return
	}
	if !reveal {
		auth["token"] = mesh.HintToken(token)
	}
}

// chatWebRequestIsLoopback 判断请求是否来自本机回环（GET /web/api/token 的同一
// 信任模型）：只有回环才谈得上「原文可见」（§5.3 / §9.1）。
func chatWebRequestIsLoopback(r *http.Request) bool {
	if r == nil || !IsChatWebLoopbackMode() {
		return false
	}
	if isClientLoopbackIP(chatWebClientIP(r)) {
		return true
	}
	// httptest / 反向代理场景没有回环 RemoteAddr 时退回 Host 判定。
	host := strings.TrimSpace(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return ChatWebHostIsLoopback(host)
}

// chatWebQueryTruthy 解析 1/true/yes/on 形式的布尔查询参数（缺省 false）。
func chatWebQueryTruthy(r *http.Request, key string) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// chatWebMeshPeersFilter 解析 §5.4 查询参数：scope（默认 all）、workspace
// （可重复 / 逗号分隔）、state（默认 all）。非法取值按默认处理而不 400：网格
// 视图是只读诊断面，容错优先（§4.7）。
func chatWebMeshPeersFilter(r *http.Request) *mesh.ViewFilter {
	filter := &mesh.ViewFilter{Scope: mesh.FilterScopeAll, State: mesh.FilterStateAll}
	if r == nil {
		return filter
	}
	query := r.URL.Query()
	if scope := strings.ToLower(strings.TrimSpace(query.Get("scope"))); scope != "" {
		filter.Scope = scope
	}
	if state := strings.ToLower(strings.TrimSpace(query.Get("state"))); state != "" {
		filter.State = state
	}
	for _, raw := range query["workspace"] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				filter.Workspace = append(filter.Workspace, part)
			}
		}
	}
	return filter
}

// chatWebMeshPeersWantsTokens 判断调用方是否显式要求令牌原文（§9.1）：
// reveal_token=1 或 redact_token=0，且必须来自回环。
func chatWebMeshPeersWantsTokens(r *http.Request) bool {
	if !chatWebRequestIsLoopback(r) {
		return false
	}
	if chatWebQueryTruthy(r, "reveal_token") {
		return true
	}
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("redact_token"))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}
