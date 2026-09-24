package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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
		// S7 降级信号（§6.4）：扇入丢弃计数是进程内状态（限流/缓冲溢出），
		// 磁盘档案里没有，只能由本进程注入；无丢弃时 DroppedCounts 返回 nil。
		if fanin := host.Fanin(); fanin != nil {
			opts.DroppedEvents = fanin.DroppedCounts()
		}
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

// ============================================================================
// 网格拉起端点（架构 §5.7 / §8.2，实施方案 S9）
//
// 浏览器点「在新窗口打开」→ 本端点 → internal/mesh.Spawn：复用该会话的活节点，
// 或在它的工作区里拉起一个新进程，并把 §7.3 的窗口 URL 交回调用方。
//
// 安全契约（§5.8 / §9.1）：
//   - 非回环一律拒绝（mesh_nonloopback_denied）；
//   - `--mesh-allow-spawn=false` 时拒绝（mesh_spawn_not_allowed；默认开启）；
//   - 令牌**只**出现在响应体的 url 字段：不进 journal、不进日志、不进视图（M7）。
//
// 降级契约（§4.7）：网格关闭时路由通常不注册；真被调用到也只回 refused，不 panic。
// ============================================================================

// chatWebMeshSpawnMaxWaitMS 是 wait_ms 上限：spawn 在请求内同步等待就绪
// （§5.7 不返回 starting），没有上限就等于让一个 HTTP 请求长期占住连接。
const chatWebMeshSpawnMaxWaitMS = 30000

// chatWebMeshSpawnSwitch 是 §5.7 要点 7 的进程开关：默认开启（浏览器点一下
// 就该能开新窗口），显式 `--mesh-allow-spawn=false` 时整条端点回 refused。
var chatWebMeshSpawnSwitch = struct {
	mu      sync.RWMutex
	allowed bool
}{allowed: true}

// SetChatWebMeshAllowSpawn 设置拉起开关（默认 true）。
func SetChatWebMeshAllowSpawn(enabled bool) {
	chatWebMeshSpawnSwitch.mu.Lock()
	defer chatWebMeshSpawnSwitch.mu.Unlock()
	chatWebMeshSpawnSwitch.allowed = enabled
}

// ChatWebMeshAllowSpawn 读取拉起开关的当前值。
func ChatWebMeshAllowSpawn() bool {
	chatWebMeshSpawnSwitch.mu.RLock()
	defer chatWebMeshSpawnSwitch.mu.RUnlock()
	return chatWebMeshSpawnSwitch.allowed
}

// chatWebMeshSpawnRequestBody 是 §5.7 的请求体。
type chatWebMeshSpawnRequestBody struct {
	SessionID string `json:"session_id"`
	// Port 是期望端口；0 = 用会话绑定的粘性端口，再退到随机空闲端口。
	Port int `json:"port"`
	// Detach 只有 true 一种合法值（§5.7 的 body 固定 detach=true）：false 会被
	// 显式拒绝，而不是静默忽略——静默忽略会把「同进程打开」的预期变成另一个进程。
	Detach *bool `json:"detach"`
	// WaitMS 是就绪等待预算；0 = 默认 8000ms。
	WaitMS int `json:"wait_ms"`
	// Origin 是审计标签（默认 web）。
	Origin string `json:"origin"`
}

// ChatWebAPIMeshSpawnResponse 是 POST /web/api/mesh/spawn 的响应体。
//
// status 复用 §5.7 的四态（reused / started / not_running / failed）；错误路径
// 用 §5.9 的信封词汇（refused / error）+ code，HTTP 状态见
// chatWebMeshSpawnHTTPStatus。
type ChatWebAPIMeshSpawnResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	PID           int    `json:"pid,omitempty"`
	Port          int    `json:"port,omitempty"`
	// URL 是 §7.3 的窗口地址，**含令牌原文**：调用方拿到后必须立即
	// window.location.replace，绝不写入 localStorage/sessionStorage/DOM
	// （Web 子方案 §10.2；M7 里唯一允许出现令牌的位置）。
	URL string `json:"url,omitempty"`
	// Reason / LogTail 解释 not_running / failed（日志尾部已脱敏）。
	Reason    string   `json:"reason,omitempty"`
	LogTail   []string `json:"log_tail,omitempty"`
	Lease     string   `json:"lease,omitempty"`
	ElapsedMS int64    `json:"elapsed_ms"`
}

// chatWebMeshSpawnFn 是端点实际调用的拉起实现：生产路径就是 mesh.Spawn。
// 测试替换它来验证 §5.9 的状态映射与参数翻译——HTTP 层不该真的去 fork 一个
// aicli，进程启动本身由 internal/mesh 的 spawn 测试覆盖。
var chatWebMeshSpawnFn = mesh.Spawn

// HandleChatWebAPIMeshSpawn 处理 POST /web/api/mesh/spawn（架构 §5.7）。
func HandleChatWebAPIMeshSpawn(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeChatWebMeshSpawnError(w, http.StatusMethodNotAllowed, "error", "",
			"method not allowed: use POST "+ChatWebAPIMeshSpawnPath, started)
		return
	}
	host := mesh.Current()
	if host == nil {
		writeChatWebMeshSpawnError(w, http.StatusForbidden, "refused", mesh.SpawnCodeMeshDisabled,
			"mesh disabled (start with --mesh)", started)
		return
	}
	if !ChatWebMeshAllowSpawn() {
		writeChatWebMeshSpawnError(w, http.StatusForbidden, "refused", mesh.SpawnCodeNotAllowed,
			"spawning is disabled (--mesh-allow-spawn=false)", started)
		return
	}
	// 拉起进程属于写路径：默认拒绝跨机（§5.8）。
	if !chatWebRequestIsLoopback(r) {
		writeChatWebMeshSpawnError(w, http.StatusForbidden, "refused", mesh.CallCodeNonLoopback,
			"mesh spawn is loopback-only", started)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, mesh.CallMaxBodyBytes+1))
	if err != nil {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			"read body: "+err.Error(), started)
		return
	}
	if len(payload) > mesh.CallMaxBodyBytes {
		writeChatWebMeshSpawnError(w, http.StatusRequestEntityTooLarge, "error", mesh.CallCodeBodyTooLarge,
			fmt.Sprintf("body exceeds %d bytes", mesh.CallMaxBodyBytes), started)
		return
	}
	var req chatWebMeshSpawnRequestBody
	if err := json.Unmarshal(payload, &req); err != nil {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			"invalid JSON body: "+err.Error(), started)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			"session_id is required", started)
		return
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			fmt.Sprintf("invalid port %d: must be 0 (auto) or 1-65535", req.Port), started)
		return
	}
	if req.WaitMS < 0 || req.WaitMS > chatWebMeshSpawnMaxWaitMS {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			fmt.Sprintf("invalid wait_ms %d: must be 0-%d", req.WaitMS, chatWebMeshSpawnMaxWaitMS), started)
		return
	}
	if req.Detach != nil && !*req.Detach {
		writeChatWebMeshSpawnError(w, http.StatusBadRequest, "error", "",
			"detach=false is not supported: the node always runs as a detached process", started)
		return
	}
	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		origin = "web"
	}
	result := chatWebMeshSpawnFn(
		mesh.SpawnRequest{SessionID: sessionID, Port: req.Port, WaitMS: req.WaitMS, Origin: origin},
		mesh.SpawnOptions{
			// 与 self/peers 同一份解析结果：spawn 读写的目录必须就是视图看到的目录。
			Paths:      host.Paths(),
			Now:        mesh.NowUTC,
			SelfNodeID: host.NodeID(),
			PID:        os.Getpid(),
			Journal:    host.Journal(),
			Wait:       time.Duration(req.WaitMS) * time.Millisecond, // 0 → mesh 默认 8s
			Context:    r.Context(),
		})
	writeWebAPIJSON(w, chatWebMeshSpawnHTTPStatus(result.Status), ChatWebAPIMeshSpawnResponse{
		SchemaVersion: mesh.SchemaVersion,
		Status:        result.Status,
		// Code 由 spawn 层给出（§5.9）：前端按 code 分支，不去解析 Reason 文案。
		Code:      result.Code,
		SessionID: result.SessionID,
		NodeID:    result.NodeID,
		PID:       result.PID,
		Port:      result.Port,
		URL:       result.URL,
		Reason:    result.Reason,
		LogTail:   result.LogTail,
		Lease:     result.Lease,
		ElapsedMS: result.ElapsedMS,
	})
}

// chatWebMeshSpawnHTTPStatus 按 §5.9 的映射把四态翻成 HTTP 状态：
// 复用/拉起成功 → 200；超时未见节点 → 504；进程启动失败 → 500。
func chatWebMeshSpawnHTTPStatus(status string) int {
	switch status {
	case mesh.SpawnStatusReused, mesh.SpawnStatusStarted:
		return http.StatusOK
	case mesh.SpawnStatusNotRunning:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// writeChatWebMeshSpawnError 写 §5.9 的统一错误信封（与 call 的错误同形）。
func writeChatWebMeshSpawnError(w http.ResponseWriter, httpStatus int, status, code, message string, started time.Time) {
	nodeID := ""
	if host := mesh.Current(); host != nil {
		nodeID = host.NodeID()
	}
	writeWebAPIJSON(w, httpStatus, ChatWebAPIMeshSpawnResponse{
		SchemaVersion: mesh.SchemaVersion,
		Status:        status,
		Code:          code,
		Message:       message,
		NodeID:        nodeID,
		ElapsedMS:     time.Since(started).Milliseconds(),
	})
}
