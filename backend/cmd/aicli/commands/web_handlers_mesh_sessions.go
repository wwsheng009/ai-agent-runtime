package commands

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// ============================================================================
// GET /web/api/sessions 的网格便捷视图（Web 子方案 §6.2，S11）
//
// 纪律（Web 子方案 §0.2 纪律 2）：本文件不新写聚合——一个会话的
// ownership / endpoint / last_known 与 self / workspaces 全部来自**同一次**
// mesh.BuildView 输出（与 GET /web/api/mesh/peers 同源），会话清单本身仍由
// SessionManager 提供（默认口径不变；?scope=all 才并入 peers 发现的会话）。
//
// 降级（MN1 / §4.7）：网格关闭（--mesh=false / 根不可解析）时 BuildView 不做
// 任何 I/O，本文件的全部字段退化为空值，sessions 仍是 200 + 旧口径清单。
// ============================================================================

// 会话在网格里的归属（§5.3 的 ownership 词汇，与 view.Ownership 同值）。
const (
	chatWebSessionOwnershipNone     = "none"
	chatWebSessionOwnershipOwner    = "owner"
	chatWebSessionOwnershipPeer     = "peer"
	chatWebSessionOwnershipConflict = "conflict"
)

// 侧栏徽标用的会话状态（§5.1 / §6.2）。
const (
	chatWebSessionStateRunning = "running"
	chatWebSessionStateBusy    = "busy"
	chatWebSessionStateIdle    = "idle"
	chatWebSessionStateUnknown = "unknown"
)

// chatWebSessionEndpoint 是 sessions 条目的节点端点段：字段与
// peers.nodes[].endpoint 同源同义（Web 子方案 §6.2），不新增解释。
type chatWebSessionEndpoint struct {
	NodeID       string     `json:"node_id"`
	BaseURL      string     `json:"base_url,omitempty"`
	WebURL       string     `json:"web_url,omitempty"`
	Loopback     bool       `json:"loopback"`
	AuthRequired bool       `json:"auth_required"`
	Reachability string     `json:"reachability,omitempty"`
	Busy         bool       `json:"busy"`
	HeartbeatAt  *time.Time `json:"heartbeat_at,omitempty"`
}

// chatWebSessionLastKnown 是「上次地址」（来自 mesh/bindings/<session>.json）：
// 节点已死、端口已换之后仍可展示，是 §5.1「已停止 · 上次 @host:port」的数据源。
type chatWebSessionLastKnown struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	From string `json:"from"`
}

// chatWebSessionsSelf 是 sessions 响应的本节点自述段（§6.2）：网格关闭时为 null。
type chatWebSessionsSelf struct {
	NodeID        string          `json:"node_id"`
	MeshRoot      string          `json:"mesh_root,omitempty"`
	WorkspacePath string          `json:"workspace_path,omitempty"`
	WorkspaceName string          `json:"workspace_name,omitempty"`
	Counts        mesh.MeshCounts `json:"counts"`
}

// chatWebSessionsWorkspace 是侧栏跨工作区分组用的工作区汇总（§6.2）。
type chatWebSessionsWorkspace struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Nodes 复用 MeshView.Workspaces（视图内该工作区的节点数）。
	Nodes int `json:"nodes"`
	// SessionCount 是本进程会话清单里绑定到该工作区的会话数。
	SessionCount int `json:"session_count"`
	// RunningCount 是该工作区里「活节点且带会话」的节点数（视图派生）。
	RunningCount int `json:"running_count"`
}

// chatWebSessionMeshHints 是一个会话的网格提示（由同一次视图折算）。
type chatWebSessionMeshHints struct {
	State         string
	Ownership     string
	ConflictCount int
	Endpoint      *chatWebSessionEndpoint
	LastKnown     *chatWebSessionLastKnown
}

// chatWebMeshSessionIndex 是一次 BuildView 的会话视角索引。构建成本为
// 一次目录扫描 + 若干次 binding 读取，全部只读。
type chatWebMeshSessionIndex struct {
	enabled bool
	paths   mesh.Paths
	view    mesh.MeshView

	selfNodeID        string
	selfWorkspacePath string
	selfWorkspaceName string

	// claimants 是「活节点 → 它声称正在服务的会话」的反向索引（按 node_id 排序，
	// 保证冲突列出顺序稳定可断言）。一个会话出现 ≥2 个活节点即 §6.5 的冲突。
	claimants map[string][]mesh.NodeView
}

// buildChatWebMeshSessionIndex 用一次 BuildView 建立索引。
// host 为 nil（--mesh=false）时 enabled=false，后续全部退化。
func buildChatWebMeshSessionIndex(host *mesh.Host) chatWebMeshSessionIndex {
	index := chatWebMeshSessionIndex{claimants: map[string][]mesh.NodeView{}}
	if host == nil {
		return index
	}
	index.paths = host.Paths()
	index.enabled = index.paths.Enabled()
	index.selfNodeID = host.NodeID()
	if record := host.RecordSnapshot(); record.Workspace != nil {
		index.selfWorkspacePath = strings.TrimSpace(record.Workspace.Path)
		index.selfWorkspaceName = strings.TrimSpace(record.Workspace.Name)
	}
	if !index.enabled {
		return index
	}
	index.view = mesh.BuildView(index.paths, mesh.ViewOptions{SelfNodeID: index.selfNodeID})
	if index.selfWorkspacePath == "" && index.selfWorkspaceName == "" {
		for _, node := range index.view.Nodes {
			if node.NodeID != index.selfNodeID || node.Workspace == nil {
				continue
			}
			index.selfWorkspacePath = strings.TrimSpace(node.Workspace.Path)
			index.selfWorkspaceName = strings.TrimSpace(node.Workspace.Name)
			break
		}
	}
	for _, node := range index.view.Nodes {
		if node.State != mesh.NodeStateLive || node.Session == nil {
			continue
		}
		id := strings.TrimSpace(node.Session.ID)
		if id == "" {
			continue
		}
		index.claimants[id] = append(index.claimants[id], node)
	}
	for id := range index.claimants {
		nodes := index.claimants[id]
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
		index.claimants[id] = nodes
	}
	return index
}

// selfSummary 返回 self 段；网格关闭时调用方应写 null。
func (idx chatWebMeshSessionIndex) selfSummary() *chatWebSessionsSelf {
	if !idx.enabled {
		return nil
	}
	return &chatWebSessionsSelf{
		NodeID:        idx.selfNodeID,
		MeshRoot:      idx.view.Root,
		WorkspacePath: idx.selfWorkspacePath,
		WorkspaceName: idx.selfWorkspaceName,
		Counts:        idx.view.Counts,
	}
}

// chatWebSessionsResponse 组装 sessions 响应（含 §6.2 的 self / workspaces 段）。
// 网格关闭时 self=null、workspaces=[]：前端据此回退到无徽标视图（MN1）。
func chatWebSessionsResponse(items []chatWebSessionListItem, currentID string, index chatWebMeshSessionIndex) map[string]any {
	workspaces := index.workspaceSummaries(items)
	if workspaces == nil {
		workspaces = []chatWebSessionsWorkspace{}
	}
	return map[string]any{
		"sessions":           items,
		"current_session_id": currentID,
		"self":               index.selfSummary(),
		"workspaces":         workspaces,
	}
}

// hints 折算单个会话的网格提示（§6.2 的 session_state / ownership 词汇）。
//
//   - 有 1 个活节点 → running|busy + owner（本进程）/ peer（别的进程）
//   - 有 ≥2 个活节点 → conflict（节点清单非空，前端禁用原地切换，§5.7）
//   - 无活节点但网格可用 → idle（历史会话，端点行显示「上次 @...」）
//   - 网格不可用 → unknown（endpoint=null；last_known 仍尽力给出）
func (idx chatWebMeshSessionIndex) hints(sessionID string) chatWebSessionMeshHints {
	hints := chatWebSessionMeshHints{
		State:     chatWebSessionStateUnknown,
		Ownership: chatWebSessionOwnershipNone,
	}
	if !idx.enabled {
		return hints
	}
	id := strings.TrimSpace(sessionID)
	claimants := idx.claimants[id]
	if len(claimants) == 0 {
		hints.State = chatWebSessionStateIdle
		hints.LastKnown = idx.lastKnown(id)
		return hints
	}
	primary := claimants[0]
	hints.Endpoint = chatWebSessionEndpointFromNode(primary)
	hints.LastKnown = idx.lastKnown(id)
	hints.State = chatWebSessionStateOf(primary)
	switch len(claimants) {
	case 1:
		hints.Ownership = chatWebSessionOwnershipPeer
		if primary.NodeID != "" && primary.NodeID == idx.selfNodeID {
			hints.Ownership = chatWebSessionOwnershipOwner
		}
	default:
		hints.Ownership = chatWebSessionOwnershipConflict
		hints.ConflictCount = len(claimants)
	}
	return hints
}

// lastKnown 读该会话的 binding（§4.6）：优先用视图里 join 到的同一份内容，
// 没有节点声明时直接读 binding 文件（历史会话的常态）。
// 网格关闭 / 文件缺失 / 端口非法 → nil（LoadBinding 已把三者归一为 ok=false）。
func (idx chatWebMeshSessionIndex) lastKnown(sessionID string) *chatWebSessionLastKnown {
	id := strings.TrimSpace(sessionID)
	if !idx.enabled || id == "" {
		return nil
	}
	binding, ok := mesh.LoadBinding(idx.paths, id)
	if !ok || binding.Preferred == nil {
		return nil
	}
	addr := binding.Preferred
	if strings.TrimSpace(addr.Host) == "" && addr.Port == 0 {
		return nil
	}
	return &chatWebSessionLastKnown{Host: addr.Host, Port: addr.Port, From: "binding"}
}

// peerSessionItems 合成 ?scope=all 才并入的跨工作区会话条目（§5.3）：
// 只取 live / stale 节点档案里的 session 段，已在本进程清单里的 id 跳过
// （本进程条目信息更全，且是唯一能原地切换的目标）。
func (idx chatWebMeshSessionIndex) peerSessionItems(seen map[string]bool) []chatWebSessionListItem {
	if !idx.enabled {
		return nil
	}
	items := make([]chatWebSessionListItem, 0, len(idx.view.Nodes))
	added := make(map[string]bool, len(idx.view.Nodes))
	for _, node := range idx.view.Nodes {
		if node.Session == nil || node.State == mesh.NodeStateStopped || node.State == mesh.NodeStateUnknown {
			continue
		}
		id := strings.TrimSpace(node.Session.ID)
		if id == "" || seen[id] || added[id] {
			continue
		}
		added[id] = true
		item := chatWebSessionListItem{
			ID:    id,
			Title: strings.TrimSpace(node.Session.Title),
			// 节点档案只带 activated_at：跨工作区条目用它顶替两个时间戳（仅展示，
			// 前端对「过于久远的时间」不渲染），消息数未知按 0。
			CreatedAt: node.Session.ActivatedAt,
			UpdatedAt: node.Session.ActivatedAt,
		}
		if item.Title == "" {
			item.Title = "(untitled)"
		}
		if node.Workspace != nil {
			item.WorkspacePath = strings.TrimSpace(node.Workspace.Path)
			item.WorkspaceName = strings.TrimSpace(node.Workspace.Name)
		}
		if item.WorkspaceName == "" {
			item.WorkspaceName = chatWebWorkspaceName(item.WorkspacePath)
		}
		items = append(items, item)
	}
	return items
}

// decorate 把网格提示写回条目，并给没有工作区记录的本地会话补上本进程工作区
// （元数据缺失时的兜底：否则它们会在侧栏被误分到「其他工作区」）。
func (idx chatWebMeshSessionIndex) decorate(items []chatWebSessionListItem) {
	for i := range items {
		hints := idx.hints(items[i].ID)
		items[i].SessionState = hints.State
		items[i].Ownership = hints.Ownership
		items[i].ConflictCount = hints.ConflictCount
		items[i].Endpoint = hints.Endpoint
		items[i].LastKnown = hints.LastKnown
		if items[i].WorkspacePath == "" && idx.selfWorkspacePath != "" {
			items[i].WorkspacePath = idx.selfWorkspacePath
			items[i].WorkspaceName = idx.selfWorkspaceName
		}
		if items[i].WorkspaceName == "" {
			items[i].WorkspaceName = chatWebWorkspaceName(items[i].WorkspacePath)
		}
	}
}

// workspaceSummaries 汇总侧栏分组用的工作区表：path/name/nodes 复用视图，
// session_count 来自本进程清单，running_count 来自视图里的活节点。
func (idx chatWebMeshSessionIndex) workspaceSummaries(items []chatWebSessionListItem) []chatWebSessionsWorkspace {
	groups := make([]chatWebSessionsWorkspace, 0, len(idx.view.Workspaces))
	byPath := make(map[string]int, len(idx.view.Workspaces))
	ensure := func(path, name string) int {
		path = strings.TrimSpace(path)
		if path == "" {
			return -1
		}
		if slot, ok := byPath[path]; ok {
			if groups[slot].Name == "" {
				groups[slot].Name = strings.TrimSpace(name)
			}
			return slot
		}
		if strings.TrimSpace(name) == "" {
			name = chatWebWorkspaceName(path)
		}
		byPath[path] = len(groups)
		groups = append(groups, chatWebSessionsWorkspace{Path: path, Name: strings.TrimSpace(name)})
		return len(groups) - 1
	}
	for _, group := range idx.view.Workspaces {
		slot := ensure(group.Path, group.Name)
		if slot >= 0 {
			groups[slot].Nodes = group.Nodes
		}
	}
	for _, item := range items {
		slot := ensure(item.WorkspacePath, item.WorkspaceName)
		if slot < 0 {
			continue
		}
		groups[slot].SessionCount++
	}
	for _, node := range idx.view.Nodes {
		if node.State != mesh.NodeStateLive || node.Session == nil || node.Workspace == nil {
			continue
		}
		if slot, ok := byPath[strings.TrimSpace(node.Workspace.Path)]; ok {
			groups[slot].RunningCount++
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].RunningCount != groups[j].RunningCount {
			return groups[i].RunningCount > groups[j].RunningCount
		}
		return groups[i].Path < groups[j].Path
	})
	return groups
}

// chatWebSessionEndpointFromNode 把 NodeView 折算成 sessions 条目的端点段。
// reachability 原样透传（未 probe 时与 peers 一致为空/ skipped），保证
// 「同一会话在 sessions 与 peers 里的 endpoint 字段逐字一致」可断言。
func chatWebSessionEndpointFromNode(node mesh.NodeView) *chatWebSessionEndpoint {
	endpoint := &chatWebSessionEndpoint{
		NodeID:       node.NodeID,
		Loopback:     true,
		Reachability: string(node.Reachability),
	}
	if node.Endpoint != nil {
		endpoint.BaseURL = node.Endpoint.BaseURL
		endpoint.WebURL = node.Endpoint.WebBaseURL
		endpoint.Loopback = node.Endpoint.Loopback
	}
	if node.Auth != nil {
		endpoint.AuthRequired = node.Auth.Required
	}
	if node.Session != nil {
		endpoint.Busy = node.Session.Busy
	}
	if node.HeartbeatAt != nil {
		endpoint.HeartbeatAt = node.HeartbeatAt
	}
	return endpoint
}

// chatWebSessionStateOf 读视图折算出的会话状态（live 节点上只会是 running / busy）。
func chatWebSessionStateOf(node mesh.NodeView) string {
	if node.Session == nil {
		return chatWebSessionStateUnknown
	}
	if state := strings.TrimSpace(node.Session.State); state != "" {
		return state
	}
	if node.Session.Busy {
		return chatWebSessionStateBusy
	}
	return chatWebSessionStateRunning
}

// chatWebSessionWorkspacePath 读会话元数据里的工作区绑定
// （与 skills API 的会话工作区口径同源：Metadata.Context[workspace_path]）。
func chatWebSessionWorkspacePath(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	if value, ok := session.Metadata.Context[sessionmeta.WorkspacePath].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// chatWebWorkspaceName 是工作区的展示名（路径末段）。
func chatWebWorkspaceName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(path))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return path
	}
	return name
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/resume 的网格归属前置检查（§6.3，S11 的 D12 落地）
// ---------------------------------------------------------------------------

// chatWebResumeMeshGuard 判定 resume 是否必须先拦下。
//
// 返回空 status 表示放行：网格关闭、无活节点占用、或占用者就是本进程
// （target 已被前面 already_current 分支处理，这里只是兜底）。
// 非空时返回 (status, 响应体)：
//   - running_elsewhere：另一个活节点正在服务该会话，前端给三段式选择
//     （打开那个窗口 / 仍在本进程切换 → force=true / 取消）；
//   - conflict：≥2 个活节点声称同一会话（§6.5），拒绝原地切换并列出节点。
//
// 判定只读且不探测网络（§4.3）：网格不可读时 claimants 为空 → 直接放行，
// 与网格关闭同一条路径（MN1）。
func chatWebResumeMeshGuard(targetID string) (string, map[string]any) {
	host := mesh.Current()
	if host == nil {
		return "", nil
	}
	index := buildChatWebMeshSessionIndex(host)
	if !index.enabled {
		return "", nil
	}
	id := strings.TrimSpace(targetID)
	claimants := index.claimants[id]
	if len(claimants) == 0 {
		return "", nil
	}
	if len(claimants) == 1 && claimants[0].NodeID == index.selfNodeID {
		return "", nil
	}
	if len(claimants) >= 2 {
		nodes := make([]map[string]any, 0, len(claimants))
		for _, node := range claimants {
			entry := map[string]any{
				"node_id": node.NodeID,
				"pid":     node.PID,
			}
			if node.Workspace != nil {
				entry["workspace"] = node.Workspace.Path
			}
			if node.HeartbeatAt != nil {
				entry["heartbeat_at"] = node.HeartbeatAt
			}
			nodes = append(nodes, entry)
		}
		return chatWebSessionOwnershipConflict, map[string]any{
			"status":     "conflict",
			"session_id": id,
			"nodes":      nodes,
			"reason":     "session is claimed by multiple live mesh nodes",
		}
	}
	node := claimants[0]
	payload := map[string]any{
		"status":     "running_elsewhere",
		"session_id": id,
		"node_id":    node.NodeID,
		"endpoint":   chatWebSessionEndpointFromNode(node),
		// 接管（--takeover）是 P2 项：入口存在前恒为 false，前端据此不显示
		// 「接管」动作（显示无效按钮比不显示更糟）。
		"takeover_available": false,
	}
	if node.Endpoint != nil {
		payload["web_url"] = node.Endpoint.WebBaseURL
	}
	if node.Workspace != nil {
		payload["workspace"] = node.Workspace.Path
	}
	return "running_elsewhere", payload
}
