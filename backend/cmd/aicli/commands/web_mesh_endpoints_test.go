package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// GET /web/api/mesh/self 与 /web/api/mesh/peers —— 网格控制面只读端点
// （架构 §5.3 / §5.4）。
//
// 门禁：
//   - self 默认脱敏，只有回环 + ?reveal_token=1 才给令牌原文（M7）；
//   - peers 默认 scope=all（跨工作区全量），counts 恒全量、只有 nodes[] 受过滤；
//   - 网格不可用时 200 降级（available=false / 空视图），绝不 5xx。

// meshTestHost 起一个真实 host（写档案 + 心跳），返回 host 与清理函数。
func meshTestHost(t *testing.T) *mesh.Host {
	t.Helper()
	t.Setenv("AICLI_MESH_DIR", t.TempDir())
	host := mesh.NewHost(mesh.HostConfig{Warn: func(string, ...any) {}})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)
	prev := mesh.Current()
	mesh.SetCurrent(host)
	t.Cleanup(func() { mesh.SetCurrent(prev) })
	return host
}

// seedMeshPeerNode 在网格目录里写一份「另一个进程」的档案，模拟跨工作区节点。
func seedMeshPeerNode(t *testing.T, paths mesh.Paths, nodeID, workspacePath, token string) {
	t.Helper()
	now := mesh.NowUTC()
	record := mesh.NodeRecord{
		SchemaVersion: mesh.SchemaVersion,
		NodeID:        nodeID,
		PID:           os.Getpid(),
		Kind:          "chat",
		Process:       mesh.ProcessInfo{StartedAt: now.Add(-time.Minute), Origin: "cli"},
		Capabilities:  []string{"mesh"},
		Liveness: mesh.LivenessInfo{
			StartedAt:       now.Add(-time.Minute),
			UpdatedAt:       now,
			HeartbeatAt:     now,
			HeartbeatTTLSec: int(mesh.DefaultHeartbeatTTL / time.Second),
			State:           string(mesh.NodeStateLive),
		},
		Session: &mesh.SessionInfo{ID: "session_" + nodeID, Title: "title", ActivatedAt: now},
	}
	if workspacePath != "" {
		record.Workspace = &mesh.WorkspaceInfo{Path: workspacePath, Name: filepath.Base(workspacePath)}
	}
	if token != "" {
		record.Auth = &mesh.AuthInfo{Mode: "loopback", Token: token}
	}
	if err := mesh.WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("write node record %s: %v", nodeID, err)
	}
}

// meshTestEndpoint 是本进程对外自述的控制面地址（仅用于档案字段断言）。
func meshTestEndpoint() mesh.EndpointInfo {
	return mesh.EndpointInfo{
		Scheme:   "http",
		Host:     "127.0.0.1",
		Port:     8899,
		Loopback: true,
		BaseURL:  "http://127.0.0.1:8899",
	}
}

// loopbackRequest 构造一个「来自回环」的请求（httptest 默认 RemoteAddr 非回环）。
func loopbackRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8899"
	return req
}

func TestHandleChatWebAPIMeshSelf_MeshDisabled(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	defer mesh.SetCurrent(prev)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSelf(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshSelfPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（网格关闭也必须降级不报错）", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("网格关闭时 available 必须为 false: %s", rec.Body.String())
	}
	if reason, _ := body["reason"].(string); reason != "mesh disabled" {
		t.Fatalf("reason = %q, want mesh disabled", reason)
	}
}

func TestHandleChatWebAPIMeshSelf_RedactsTokenByDefault(t *testing.T) {
	host := meshTestHost(t)
	const raw = "secret-token-abcdef"
	host.SetEndpoint(meshTestEndpoint(), mesh.AuthInfo{Mode: "loopback", Token: raw})
	host.SetSession(&mesh.SessionInfo{ID: "session_self", Title: "self"})
	host.SetBusy(true, "turn-0007")

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSelf(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshSelfPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), raw) {
		t.Fatalf("默认响应泄露令牌原文（M7）: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if available, _ := body["available"].(bool); !available {
		t.Fatalf("网格可用时 available 必须为 true: %s", rec.Body.String())
	}
	if nodeID, _ := body["node_id"].(string); nodeID != host.NodeID() {
		t.Fatalf("node_id = %q, want %q", nodeID, host.NodeID())
	}
	auth, _ := body["auth"].(map[string]any)
	if auth == nil {
		t.Fatalf("auth 段缺失: %s", rec.Body.String())
	}
	token, _ := auth["token"].(string)
	if token == "" || token == raw {
		t.Fatalf("auth.token = %q，默认必须是脱敏提示而不是原文", token)
	}
	if token != mesh.HintToken(raw) {
		t.Fatalf("auth.token = %q, want %q", token, mesh.HintToken(raw))
	}
	// derived 段：内存实时值（会话忙碌与 turn 号来自本进程状态）。
	derived, _ := body["derived"].(map[string]any)
	if derived == nil {
		t.Fatalf("derived 段缺失: %s", rec.Body.String())
	}
	if busy, _ := derived["busy"].(bool); !busy {
		t.Fatalf("derived.busy = false, want true: %s", rec.Body.String())
	}
	if turnID, _ := derived["turn_id"].(string); turnID != "turn-0007" {
		t.Fatalf("derived.turn_id = %q, want turn-0007", turnID)
	}
	// mesh 段：自描述网格根（脚本不用猜目录）。
	section, _ := body["mesh"].(map[string]any)
	if section == nil {
		t.Fatalf("mesh 段缺失: %s", rec.Body.String())
	}
	if enabled, _ := section["enabled"].(bool); !enabled {
		t.Fatalf("mesh.enabled = false, want true")
	}
	root, _ := section["root"].(string)
	if strings.TrimSpace(root) == "" || root != host.Paths().Root {
		t.Fatalf("mesh.root = %q, want %q", root, host.Paths().Root)
	}
	if journal, _ := section["journal"].(string); !strings.HasSuffix(journal, host.NodeID()+".ndjson") {
		t.Fatalf("mesh.journal = %q, want …%s.ndjson", journal, host.NodeID())
	}
}

func TestHandleChatWebAPIMeshSelf_RevealTokenLoopbackOnly(t *testing.T) {
	host := meshTestHost(t)
	const raw = "secret-token-abcdef"
	host.SetEndpoint(meshTestEndpoint(), mesh.AuthInfo{Mode: "loopback", Token: raw})

	decodeToken := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		auth, _ := body["auth"].(map[string]any)
		token, _ := auth["token"].(string)
		return token
	}

	// 回环 + ?reveal_token=1 → 原文。
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSelf(rec, loopbackRequest(ChatWebAPIMeshSelfPath+"?reveal_token=1"))
	if got := decodeToken(t, rec); got != raw {
		t.Fatalf("回环 reveal 应返回原文: %q", got)
	}

	// 非回环（RemoteAddr/Host 都不是回环）→ 仍然脱敏。
	rec = httptest.NewRecorder()
	remote := httptest.NewRequest(http.MethodGet, ChatWebAPIMeshSelfPath+"?reveal_token=1", nil)
	remote.RemoteAddr = "192.0.2.10:4444"
	remote.Host = "192.0.2.10:8899"
	HandleChatWebAPIMeshSelf(rec, remote)
	if got := decodeToken(t, rec); got != mesh.HintToken(raw) {
		t.Fatalf("非回环 reveal 必须被拒绝: %q", got)
	}
	if strings.Contains(rec.Body.String(), raw) {
		t.Fatalf("非回环响应泄露令牌原文（M7）: %s", rec.Body.String())
	}

	// 非回环鉴权模式（--web-host 0.0.0.0）→ 即使是回环地址也不给原文。
	setChatWebLoopbackModeForTest(false)
	defer setChatWebLoopbackModeForTest(true)
	rec = httptest.NewRecorder()
	HandleChatWebAPIMeshSelf(rec, loopbackRequest(ChatWebAPIMeshSelfPath+"?reveal_token=1"))
	if got := decodeToken(t, rec); got != mesh.HintToken(raw) {
		t.Fatalf("非回环模式 reveal 必须被拒绝: %q", got)
	}
}

func TestHandleChatWebAPIMeshSelf_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshSelf(rec, httptest.NewRequest(http.MethodPost, ChatWebAPIMeshSelfPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func decodeMeshView(t *testing.T, body []byte) (map[string]any, mesh.MeshView) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, string(body))
	}
	var typed mesh.MeshView
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("响应无法解码为 MeshView: %v", err)
	}
	return raw, typed
}

func TestHandleChatWebAPIMeshPeers_DefaultAllScope(t *testing.T) {
	host := meshTestHost(t)
	host.SetSession(&mesh.SessionInfo{ID: "session_self", Title: "self"})
	seedMeshPeerNode(t, host.Paths(), "node-peer-two", `E:\ws\two`, "peer-token-9999")

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshPeersPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw, view := decodeMeshView(t, rec.Body.Bytes())
	// 默认跨工作区全量：本进程 + 另一工作区的 peer 都在列。
	if view.Counts.Live != 2 {
		t.Fatalf("counts.live = %d, want 2 (%s)", view.Counts.Live, rec.Body.String())
	}
	if len(view.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (%s)", len(view.Nodes), rec.Body.String())
	}
	if view.Self == nil || view.Self.NodeID != host.NodeID() {
		t.Fatalf("视图 self 段必须指向本节点: %+v", view.Self)
	}
	// 归属（§4.2）：本节点独占自己的会话 → owner；peer 的会话与本节点无关 → peer。
	ownership := map[string]mesh.Ownership{}
	for _, node := range view.Nodes {
		ownership[node.NodeID] = node.Ownership
		// 默认 probe 关闭：不探测即不产生网络请求，全部标 skipped（§4.3）。
		if node.Reachability != mesh.ReachabilitySkipped {
			t.Fatalf("默认 probe 必须关闭，node %s reachability=%s", node.NodeID, node.Reachability)
		}
	}
	if ownership[host.NodeID()] != mesh.OwnershipOwner {
		t.Fatalf("本节点 ownership = %s, want owner: %s", ownership[host.NodeID()], rec.Body.String())
	}
	if ownership["node-peer-two"] != mesh.OwnershipPeer {
		t.Fatalf("peer ownership = %s, want peer: %s", ownership["node-peer-two"], rec.Body.String())
	}
	// 默认过滤条件回显：scope=all / state=all。
	filter, _ := raw["filter"].(map[string]any)
	if filter == nil {
		t.Fatalf("filter 段缺失: %s", rec.Body.String())
	}
	if scope, _ := filter["scope"].(string); scope != mesh.FilterScopeAll {
		t.Fatalf("filter.scope = %q, want all", scope)
	}
	if state, _ := filter["state"].(string); state != mesh.FilterStateAll {
		t.Fatalf("filter.state = %q, want all", state)
	}
	// M7：视图默认不含任何令牌原文。
	if strings.Contains(rec.Body.String(), "peer-token-9999") {
		t.Fatalf("peers 视图泄露令牌原文: %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIMeshPeers_FilterTrimsNodesOnly(t *testing.T) {
	host := meshTestHost(t)
	seedMeshPeerNode(t, host.Paths(), "node-peer-two", `E:\ws\two`, "")

	rec := httptest.NewRecorder()
	target := ChatWebAPIMeshPeersPath + "?workspace=" + `E:\ws\two` + "&state=live"
	HandleChatWebAPIMeshPeers(rec, httptest.NewRequest(http.MethodGet, target, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw, view := decodeMeshView(t, rec.Body.Bytes())
	// counts 恒全量口径：过滤只裁剪 nodes[]。
	if view.Counts.Live != 2 {
		t.Fatalf("counts.live = %d, want 2（过滤不得改变全量口径）", view.Counts.Live)
	}
	if len(view.Nodes) != 1 || view.Nodes[0].NodeID != "node-peer-two" {
		t.Fatalf("nodes 未被 workspace 过滤: %+v", view.Nodes)
	}
	filter, _ := raw["filter"].(map[string]any)
	workspaces, _ := filter["workspace"].([]any)
	if len(workspaces) != 1 || workspaces[0] != `E:\ws\two` {
		t.Fatalf("filter.workspace = %v, want [E:\\ws\\two]", filter["workspace"])
	}
	if state, _ := filter["state"].(string); state != mesh.FilterStateLive {
		t.Fatalf("filter.state = %q, want live", state)
	}
}

func TestHandleChatWebAPIMeshPeers_RevealTokenLoopbackOnly(t *testing.T) {
	host := meshTestHost(t)
	const raw = "peer-token-9999"
	seedMeshPeerNode(t, host.Paths(), "node-peer-two", `E:\ws\two`, raw)

	// 默认（无 reveal）：脱敏。
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshPeersPath, nil))
	if strings.Contains(rec.Body.String(), raw) {
		t.Fatalf("默认视图泄露令牌原文（M7）: %s", rec.Body.String())
	}

	// 回环 + ?redact_token=0 → 原文。
	rec = httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, loopbackRequest(ChatWebAPIMeshPeersPath+"?redact_token=0"))
	if !strings.Contains(rec.Body.String(), raw) {
		t.Fatalf("回环显式 reveal 应返回原文: %s", rec.Body.String())
	}

	// 非回环 → 仍然脱敏（即使显式要求）。
	rec = httptest.NewRecorder()
	remote := httptest.NewRequest(http.MethodGet, ChatWebAPIMeshPeersPath+"?redact_token=0", nil)
	remote.RemoteAddr = "192.0.2.10:4444"
	remote.Host = "192.0.2.10:8899"
	HandleChatWebAPIMeshPeers(rec, remote)
	if strings.Contains(rec.Body.String(), raw) {
		t.Fatalf("非回环 reveal 必须被拒绝: %s", rec.Body.String())
	}
}

func TestHandleChatWebAPIMeshPeers_MeshUnavailableDegrades(t *testing.T) {
	// 网格根不可读（路径被文件占用）→ 200 + 空视图，而不是 5xx。
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a dir\n"), 0o600); err != nil {
		t.Fatalf("seed blocked path: %v", err)
	}
	t.Setenv("AICLI_MESH_DIR", blocked)

	prev := mesh.Current()
	mesh.SetCurrent(nil)
	defer mesh.SetCurrent(prev)

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, httptest.NewRequest(http.MethodGet, ChatWebAPIMeshPeersPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（网格不可读必须降级）", rec.Code)
	}
	_, view := decodeMeshView(t, rec.Body.Bytes())
	if view.Counts.Live != 0 || len(view.Nodes) != 0 {
		t.Fatalf("网格不可读时应为空视图: %+v", view)
	}
}

func TestHandleChatWebAPIMeshPeers_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, httptest.NewRequest(http.MethodPost, ChatWebAPIMeshPeersPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// 清单登记（架构 §5.1）：mesh 分组的 enabled 标记必须与「本进程是否参与网格」一致。
func TestChatDebugEndpointList_MeshGroup(t *testing.T) {
	host := meshTestHost(t)
	_ = host
	prevProvider := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return &ChatSession{} }
	defer func() { chatDebugDisplaySessionProvider = prevProvider }()

	snap := BuildChatDebugEndpointsSnapshot()
	want := map[string]bool{
		"/web/api/health":     false,
		"/web/api/mesh/self":  false,
		"/web/api/mesh/peers": false,
	}
	for _, info := range snap.Endpoints {
		if info.Scheme != "mesh" {
			continue
		}
		if _, ok := want[info.Path]; ok {
			want[info.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("清单缺少 mesh 分组端点 %s", path)
		}
	}

	// 网格关闭时同组端点必须为 [disabled]（路由不注册，§9.7）。
	mesh.SetCurrent(nil)
	defer mesh.SetCurrent(host)
	offline := BuildChatDebugEndpointsSnapshot()
	for _, info := range offline.Endpoints {
		if info.Scheme == "mesh" && info.Enabled {
			t.Fatalf("网格关闭时 %s 不应为 enabled", info.Path)
		}
	}
}

// S7（§6.4）：扇入丢弃计数是进程内状态，必须出现在 peers 视图的 dropped_events 里——
// 否则「限流静默丢帧」就成了不可观测的降级，消费方无从知道实时覆盖打了折扣。
func TestHandleChatWebAPIMeshPeers_ReportsFaninDroppedEvents(t *testing.T) {
	host := meshTestHost(t)
	seedMeshPeerNode(t, host.Paths(), "node-peer-two", `E:\ws\two`, "")
	fanin := meshFanin(t, host)

	// 每 peer 令牌桶突发 = DefaultFaninPeerBurst；多发几帧必然触发丢弃并计数。
	over := 5
	for i := 0; i < mesh.DefaultFaninPeerBurst+over; i++ {
		fanin.PublishPeerFrame("node-peer-two", mesh.Frame{
			Type:         mesh.FramePeerUpdated,
			SourceNodeID: "node-peer-two",
			Seq:          uint64(i + 1),
			Data:         map[string]any{"state": "busy"},
		})
	}

	rec := httptest.NewRecorder()
	HandleChatWebAPIMeshPeers(rec, loopbackRequest(ChatWebAPIMeshPeersPath))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	_, view := decodeMeshView(t, rec.Body.Bytes())

	found := false
	for _, node := range view.Nodes {
		if node.NodeID != "node-peer-two" {
			continue
		}
		found = true
		if node.DroppedEvents != uint64(over) {
			t.Fatalf("dropped_events = %d, want %d（超限帧必须计入该 peer）", node.DroppedEvents, over)
		}
	}
	if !found {
		t.Fatal("视图缺少 peer 节点 node-peer-two")
	}
	// 未丢弃的节点不出现该字段（omitempty：它是降级信号，不是常规指标）。
	if !strings.Contains(rec.Body.String(), `"dropped_events":`+strconv.Itoa(over)) {
		t.Fatalf("视图缺少 dropped_events 字段: %s", rec.Body.String())
	}
	if strings.Count(rec.Body.String(), `"dropped_events"`) != 1 {
		t.Fatalf("dropped_events 只应出现在被丢弃的 peer 上: %s", rec.Body.String())
	}
}
