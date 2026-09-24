package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// GET /web/api/sessions 的网格便捷视图与 resume 归属检查
// （Web 子方案 §6.2 / §6.3，S11）。
//
// 门禁：
//   - sessions 的网格字段与 /web/api/mesh/peers 同源（同一次 BuildView）；
//   - ?scope=all 才并入 peers 的跨工作区会话，且按 id 去重；
//   - 网格关闭 / 不可读 → 200 + 旧口径（self=null、endpoint=null），绝不 5xx；
//   - resume 在目标被别的活节点占用时先拦下（running_elsewhere / conflict），
//     只有 force=true 才注入队列。

// chatWebSessionsTestResponse 是 sessions 响应的测试解码结构。
type chatWebSessionsTestResponse struct {
	Sessions   []chatWebSessionListItem   `json:"sessions"`
	CurrentID  string                     `json:"current_session_id"`
	Self       *chatWebSessionsSelf       `json:"self"`
	Workspaces []chatWebSessionsWorkspace `json:"workspaces"`
}

// seedMeshSessionsTestNode 写一份自定义的活节点档案：会话 id / 工作区 / 端点 /
// 令牌要求都由调用方给定（seedMeshPeerNode 不带端点，seedMeshSpawnableNode 不带
// 工作区，两者的默认值都不足以覆盖 S11 的断言面）。
func seedMeshSessionsTestNode(t *testing.T, paths mesh.Paths, nodeID, sessionID, workspacePath string, authRequired bool) {
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
		Endpoint: &mesh.EndpointInfo{
			Scheme:     "http",
			Host:       "127.0.0.1",
			Port:       8899,
			Loopback:   true,
			BaseURL:    "http://127.0.0.1:8899",
			WebBaseURL: "http://127.0.0.1:8899/web",
		},
		Session: &mesh.SessionInfo{ID: sessionID, Title: "title", ActivatedAt: now},
		Auth:    &mesh.AuthInfo{Mode: "loopback", Required: authRequired, Token: "peer-token-" + nodeID},
	}
	if workspacePath != "" {
		record.Workspace = &mesh.WorkspaceInfo{Path: workspacePath, Name: filepath.Base(workspacePath)}
	}
	if err := mesh.WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("write node record %s: %v", nodeID, err)
	}
}

// newMeshWebSessionsTestSession 起一个内存 SessionManager 的 ChatSession。
// currentID 是当前活动会话；workspaces 按会话 id 写 Metadata.Context[workspace_path]
// （模拟真实会话的工作区绑定，nil 表示不绑定）；historyIDs 是可恢复的历史会话
// （各带一轮对话，才会进入候选集）。
func newMeshWebSessionsTestSession(t *testing.T, currentID string, workspaces map[string]string, historyIDs ...string) *ChatSession {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	ctx := context.Background()
	seed := func(id string) *runtimechat.Session {
		session := runtimechat.NewSession("mesh-web-user")
		session.ID = id
		if path := workspaces[id]; path != "" {
			session.Metadata.Context = map[string]interface{}{sessionmeta.WorkspacePath: path}
		}
		session.AddMessage(*runtimetypes.NewUserMessage("hello"))
		session.AddMessage(*runtimetypes.NewAssistantMessage("hi"))
		if err := storage.Save(ctx, session); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
		return session
	}
	current := seed(currentID)
	for _, id := range historyIDs {
		seed(id)
	}
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	return &ChatSession{
		InputQueue: newChatInputQueue(nil),
		// 空闲协调器：IsReady()=true，让 /resume 通过队列的命令门（真实进程里
		// 空闲时正是这个状态；Interaction 为 nil 会被门判为「忙」而拒绝）。
		Interaction:    &chatInteractionCoordinator{},
		SessionManager: manager,
		SessionUserID:  "mesh-web-user",
		RuntimeSession: current,
	}
}

// decodeChatWebSessionsTestResponse 解码 sessions 响应（含 S11 的 self / workspaces 段）。
func decodeChatWebSessionsTestResponse(t *testing.T, rec *httptest.ResponseRecorder) chatWebSessionsTestResponse {
	t.Helper()
	var body chatWebSessionsTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("sessions 响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// decodeChatWebJSONMap 解码任意 JSON 对象响应（resume 的状态体）。
func decodeChatWebJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, rec.Body.String())
	}
	return body
}

// TestChatWebMeshSessionIndexHints 锁定 §6.2 的状态折算表：owner / peer /
// conflict / idle（含 last_known）四种形态各断言一次。
func TestChatWebMeshSessionIndexHints(t *testing.T) {
	host := meshTestHost(t)
	host.SetSession(&mesh.SessionInfo{ID: "session_self", Title: "self", Busy: true})
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-101", "session_peer", `E:\ws\two`, true)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-102", "session_conflict", `E:\ws\two`, false)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-103", "session_conflict", `E:\ws\three`, false)
	if err := mesh.TouchBinding(host.Paths(), mesh.BindingUpdate{
		SessionID:     "session_hist",
		Host:          "127.0.0.1",
		Port:          51234,
		NodeID:        "node-20260101T000000Z-104",
		WorkspacePath: `E:\ws\one`,
	}); err != nil {
		t.Fatalf("TouchBinding: %v", err)
	}

	index := buildChatWebMeshSessionIndex(host)
	if !index.enabled {
		t.Fatal("host 已启动，索引必须 enabled")
	}

	// 本进程占用 → owner + busy（会话 Busy 透传）。
	self := index.hints("session_self")
	if self.Ownership != chatWebSessionOwnershipOwner || self.State != chatWebSessionStateBusy {
		t.Fatalf("self hints = %+v, want owner/busy", self)
	}
	if self.Endpoint == nil || self.Endpoint.NodeID != host.NodeID() {
		t.Fatalf("self endpoint = %+v, want node_id=%q", self.Endpoint, host.NodeID())
	}

	// 别的活节点占用 → peer + running（含端点、令牌要求与未探测的 reachability）。
	peer := index.hints("session_peer")
	if peer.Ownership != chatWebSessionOwnershipPeer || peer.State != chatWebSessionStateRunning {
		t.Fatalf("peer hints = %+v, want peer/running", peer)
	}
	if peer.Endpoint == nil || peer.Endpoint.NodeID != "node-20260101T000000Z-101" {
		t.Fatalf("peer endpoint = %+v, want peer node id", peer.Endpoint)
	}
	if peer.Endpoint.BaseURL != "http://127.0.0.1:8899" || !peer.Endpoint.AuthRequired {
		t.Fatalf("peer endpoint = %+v, want base_url + auth_required", peer.Endpoint)
	}
	if peer.Endpoint.Reachability != string(mesh.ReachabilitySkipped) {
		t.Fatalf("reachability = %q, want skipped（不探测，与 peers 同源）", peer.Endpoint.Reachability)
	}

	// ≥2 个活节点声称同一会话 → conflict（节点数供徽标「冲突（N 个节点）」）。
	conflict := index.hints("session_conflict")
	if conflict.Ownership != chatWebSessionOwnershipConflict || conflict.ConflictCount != 2 {
		t.Fatalf("conflict hints = %+v, want conflict/2", conflict)
	}

	// 无活节点占用 → idle + last_known（binding 是唯一数据源，endpoint 必须为空）。
	hist := index.hints("session_hist")
	if hist.State != chatWebSessionStateIdle || hist.Ownership != chatWebSessionOwnershipNone {
		t.Fatalf("history hints = %+v, want idle/none", hist)
	}
	if hist.Endpoint != nil {
		t.Fatalf("idle 会话不得有 endpoint: %+v", hist.Endpoint)
	}
	if hist.LastKnown == nil || hist.LastKnown.Host != "127.0.0.1" || hist.LastKnown.Port != 51234 || hist.LastKnown.From != "binding" {
		t.Fatalf("last_known = %+v, want binding 的 127.0.0.1:51234", hist.LastKnown)
	}
}

// TestHandleChatWebAPISessions_MeshDisabledDegrades 锁定 MN1：网格关闭时
// sessions 仍是 200 + 旧口径清单，网格字段整体退化为 null / unknown。
func TestHandleChatWebAPISessions_MeshDisabledDegrades(t *testing.T) {
	prev := mesh.Current()
	mesh.SetCurrent(nil)
	t.Cleanup(func() { mesh.SetCurrent(prev) })

	session := newMeshWebSessionsTestSession(t, "session-current", nil, "session-history")
	withWebTestSession(t, session)

	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?scope=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（网格关闭必须降级）: %s", rec.Code, rec.Body.String())
	}
	body := decodeChatWebSessionsTestResponse(t, rec)
	if body.Self != nil {
		t.Fatalf("网格关闭时 self 必须是 null: %+v", body.Self)
	}
	if len(body.Workspaces) != 0 {
		t.Fatalf("网格关闭时 workspaces 必须是空数组: %+v", body.Workspaces)
	}
	// 旧口径：当前会话 + 历史会话，一条不多一条不少（?scope=all 不得凭空造条目）。
	if len(body.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want 当前会话 + 历史会话", body.Sessions)
	}
	for _, item := range body.Sessions {
		if item.SessionState != chatWebSessionStateUnknown {
			t.Fatalf("网格关闭时 session_state = %q, want unknown", item.SessionState)
		}
		if item.Endpoint != nil || item.LastKnown != nil {
			t.Fatalf("网格关闭时 endpoint/last_known 必须为 null: %+v / %+v", item.Endpoint, item.LastKnown)
		}
	}
	// 原始 JSON 里两个字段必须是显式 null（前端按 null 判定回退），不能省略。
	raw := rec.Body.String()
	if !strings.Contains(raw, `"endpoint":null`) || !strings.Contains(raw, `"last_known":null`) {
		t.Fatalf("sessions 条目必须显式输出 endpoint/last_known=null: %s", raw)
	}
}

// queuedInputCount 读输入队列里已排队的条目数（断言「未注入 / 已注入」用）。
func queuedInputCount(q *chatInputQueue) int {
	if q == nil {
		return 0
	}
	q.queuedMu.Lock()
	defer q.queuedMu.Unlock()
	return len(q.queuedFront) + len(q.queuedPreview)
}

// postChatWebResume 走一遍 resume 端点（force 控制是否跳过归属检查）。
func postChatWebResume(t *testing.T, sessionID string, force bool) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]any{"session_id": sessionID}
	if force {
		payload["force"] = true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal resume 请求: %v", err)
	}
	rec := httptest.NewRecorder()
	HandleChatWebAPISessionsResume(rec, httptest.NewRequest(http.MethodPost, ChatWebAPISessionsResumePath, bytes.NewReader(raw)))
	return rec
}

// findChatWebSessionItem 按 id 找条目（列表顺序由排序决定，断言不该依赖它）。
func findChatWebSessionItem(items []chatWebSessionListItem, id string) (chatWebSessionListItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return chatWebSessionListItem{}, false
}

// TestHandleChatWebAPISessions_ScopeAllMergesPeers 锁定 §5.3 / §6.2 的口径：
// 默认只列本进程清单（逐字不变），?scope=all 才并入 peers 发现的跨工作区会话，
// 且按会话 id 去重（本地条目优先，绝不出现两条同 id）。
func TestHandleChatWebAPISessions_ScopeAllMergesPeers(t *testing.T) {
	host := meshTestHost(t)
	host.SetSession(&mesh.SessionInfo{ID: "session-current", Title: "self", Busy: true})
	peerID := "node-20260101T000000Z-201"
	seedMeshSessionsTestNode(t, host.Paths(), peerID, "session-peer", `E:\ws\two`, true)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-202", "session-history", `E:\ws\two`, false)

	session := newMeshWebSessionsTestSession(t, "session-current",
		map[string]string{"session-history": `E:\ws\one`}, "session-history")
	withWebTestSession(t, session)

	// --- 默认口径：只有本进程清单，peer 会话不出现 ---
	rec := httptest.NewRecorder()
	HandleChatWebAPISessions(rec, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeChatWebSessionsTestResponse(t, rec)
	if len(body.Sessions) != 2 {
		t.Fatalf("默认口径 sessions = %+v, want 当前 + 历史（不含 peer）", body.Sessions)
	}
	if _, ok := findChatWebSessionItem(body.Sessions, "session-peer"); ok {
		t.Fatalf("默认口径不得并入 peer 会话: %+v", body.Sessions)
	}
	if body.Self == nil || body.Self.NodeID != host.NodeID() {
		t.Fatalf("self = %+v, want node_id=%q", body.Self, host.NodeID())
	}
	if body.Self.Counts.Live != 3 {
		t.Fatalf("counts.live = %d, want 3（本进程 + 2 个 peer）", body.Self.Counts.Live)
	}

	// 本进程占用的当前会话 → owner；会话 Busy 透传为 busy。
	current, ok := findChatWebSessionItem(body.Sessions, "session-current")
	if !ok {
		t.Fatalf("当前会话缺失: %+v", body.Sessions)
	}
	if !current.Current || current.Ownership != chatWebSessionOwnershipOwner || current.SessionState != chatWebSessionStateBusy {
		t.Fatalf("当前会话 = %+v, want current/owner/busy", current)
	}
	if current.Endpoint == nil || current.Endpoint.NodeID != host.NodeID() {
		t.Fatalf("当前会话 endpoint = %+v, want self node", current.Endpoint)
	}

	// 被别的活节点占用的历史会话 → peer/running，工作区来自会话元数据绑定。
	history, ok := findChatWebSessionItem(body.Sessions, "session-history")
	if !ok {
		t.Fatalf("历史会话缺失: %+v", body.Sessions)
	}
	if history.Ownership != chatWebSessionOwnershipPeer || history.SessionState != chatWebSessionStateRunning {
		t.Fatalf("历史会话 = %+v, want peer/running", history)
	}
	if history.Endpoint == nil || history.Endpoint.NodeID != "node-20260101T000000Z-202" {
		t.Fatalf("历史会话 endpoint = %+v, want peer node", history.Endpoint)
	}
	if history.WorkspacePath != `E:\ws\one` || history.WorkspaceName != "one" {
		t.Fatalf("workspace = %q/%q, want 会话元数据的绑定", history.WorkspacePath, history.WorkspaceName)
	}

	// --- ?scope=all：并入 peer 会话，且同 id 只出现一次 ---
	recAll := httptest.NewRecorder()
	HandleChatWebAPISessions(recAll, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?scope=all", nil))
	if recAll.Code != http.StatusOK {
		t.Fatalf("scope=all status = %d, want 200: %s", recAll.Code, recAll.Body.String())
	}
	all := decodeChatWebSessionsTestResponse(t, recAll)
	if len(all.Sessions) != 3 {
		t.Fatalf("scope=all sessions = %+v, want 当前 + 历史 + peer", all.Sessions)
	}
	ids := map[string]int{}
	for _, item := range all.Sessions {
		ids[item.ID]++
	}
	for id, count := range ids {
		if count != 1 {
			t.Fatalf("会话 %q 出现 %d 次，去重失效: %+v", id, count, all.Sessions)
		}
	}
	// 去重时本地条目优先：历史会话仍是本地那条（有消息数），不是合成的空条目。
	historyAll, _ := findChatWebSessionItem(all.Sessions, "session-history")
	if historyAll.MessageCount == 0 {
		t.Fatalf("去重后应保留本地条目（message_count>0）: %+v", historyAll)
	}
	peerItem, ok := findChatWebSessionItem(all.Sessions, "session-peer")
	if !ok {
		t.Fatalf("scope=all 必须并入 peer 会话: %+v", all.Sessions)
	}
	if peerItem.Ownership != chatWebSessionOwnershipPeer || peerItem.SessionState != chatWebSessionStateRunning {
		t.Fatalf("peer 条目 = %+v, want peer/running", peerItem)
	}
	if peerItem.WorkspacePath != `E:\ws\two` || peerItem.WorkspaceName != "two" {
		t.Fatalf("peer 条目的工作区 = %q/%q, want E:\\ws\\two/two", peerItem.WorkspacePath, peerItem.WorkspaceName)
	}
	if peerItem.Endpoint == nil || peerItem.Endpoint.BaseURL != "http://127.0.0.1:8899" ||
		peerItem.Endpoint.WebURL != "http://127.0.0.1:8899/web" || !peerItem.Endpoint.AuthRequired {
		t.Fatalf("peer endpoint = %+v, want base_url + web_url + auth_required", peerItem.Endpoint)
	}

	// 与 /web/api/mesh/peers 同源（§0.2 纪律 2）：同一节点在同一视图里的端点字段逐字一致。
	view := mesh.BuildView(host.Paths(), mesh.ViewOptions{SelfNodeID: host.NodeID()})
	var viewNode *mesh.NodeView
	for i := range view.Nodes {
		if view.Nodes[i].NodeID == peerID {
			viewNode = &view.Nodes[i]
			break
		}
	}
	if viewNode == nil || viewNode.Endpoint == nil {
		t.Fatalf("视图里找不到带端点的 %s: %+v", peerID, view.Nodes)
	}
	if peerItem.Endpoint.BaseURL != viewNode.Endpoint.BaseURL || peerItem.Endpoint.WebURL != viewNode.Endpoint.WebBaseURL {
		t.Fatalf("sessions 端点与 peers 视图不同源: %+v vs %+v", peerItem.Endpoint, viewNode.Endpoint)
	}
	if peerItem.Endpoint.Reachability != string(viewNode.Reachability) {
		t.Fatalf("reachability 不同源: %q vs %q", peerItem.Endpoint.Reachability, viewNode.Reachability)
	}

	// workspaces 段：peer 工作区（nodes/running_count 来自视图）与本进程会话工作区都在。
	byWorkspace := map[string]chatWebSessionsWorkspace{}
	for _, ws := range all.Workspaces {
		byWorkspace[ws.Path] = ws
	}
	two, ok := byWorkspace[`E:\ws\two`]
	if !ok {
		t.Fatalf("workspaces 缺少 peer 工作区: %+v", all.Workspaces)
	}
	if two.Nodes != 2 || two.RunningCount != 2 {
		t.Fatalf("E:\\ws\\two = %+v, want nodes=2 running_count=2", two)
	}
	one, ok := byWorkspace[`E:\ws\one`]
	if !ok || one.SessionCount != 1 {
		t.Fatalf("workspaces 缺少本地会话工作区: %+v", all.Workspaces)
	}
}

// TestHandleChatWebAPISessionsResume_MeshOwnershipGuard 锁定 §6.3 的归属前置检查：
// 别的活节点占用 → running_elsewhere（不注入队列）；≥2 个活节点 → conflict；
// force=true 才允许原地切换；目标就是当前会话 → already_current（先于归属检查）。
func TestHandleChatWebAPISessionsResume_MeshOwnershipGuard(t *testing.T) {
	host := meshTestHost(t)
	host.SetSession(&mesh.SessionInfo{ID: "session-current", Title: "self"})
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-301", "session-history", `E:\ws\two`, true)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-302", "session-conflict", `E:\ws\two`, false)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-303", "session-conflict", `E:\ws\three`, false)

	session := newMeshWebSessionsTestSession(t, "session-current", nil, "session-history", "session-conflict")
	withWebTestSession(t, session)

	// 1) 目标就是当前会话：切换是空操作，不该被归属检查误报。
	if got := decodeChatWebJSONMap(t, postChatWebResume(t, "session-current", false)); got["status"] != "already_current" {
		t.Fatalf("already_current 分支失效: %+v", got)
	}

	// 2) 被别的活节点占用：拦下并给出可跳转的端点信息，队列里不能有命令。
	rec := postChatWebResume(t, "session-history", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	blocked := decodeChatWebJSONMap(t, rec)
	if blocked["status"] != "running_elsewhere" {
		t.Fatalf("status = %v, want running_elsewhere: %+v", blocked["status"], blocked)
	}
	if blocked["node_id"] != "node-20260101T000000Z-301" || blocked["web_url"] != "http://127.0.0.1:8899/web" {
		t.Fatalf("running_elsewhere 缺少节点信息: %+v", blocked)
	}
	if blocked["workspace"] != `E:\ws\two` {
		t.Fatalf("running_elsewhere 缺少工作区: %+v", blocked)
	}
	if blocked["takeover_available"] != false {
		t.Fatalf("takeover_available 恒为 false（--takeover 是 P2 项）: %+v", blocked)
	}
	if count := queuedInputCount(session.InputQueue); count != 0 {
		t.Fatalf("被拦下的 resume 不得注入队列，queued=%d", count)
	}

	// 3) force=true：用户确认「仍在本进程切换」，跳过检查并真正注入。
	forced := decodeChatWebJSONMap(t, postChatWebResume(t, "session-history", true))
	if forced["status"] != "queued" || forced["session_id"] != "session-history" {
		t.Fatalf("force=true 必须注入队列: %+v", forced)
	}
	if count := queuedInputCount(session.InputQueue); count != 1 {
		t.Fatalf("force=true 后队列应有 1 条命令，queued=%d", count)
	}

	// 4) ≥2 个活节点声称同一会话：冲突清单按 node_id 稳定排序。
	conflict := decodeChatWebJSONMap(t, postChatWebResume(t, "session-conflict", false))
	if conflict["status"] != "conflict" {
		t.Fatalf("status = %v, want conflict: %+v", conflict["status"], conflict)
	}
	nodes, ok := conflict["nodes"].([]any)
	if !ok || len(nodes) != 2 {
		t.Fatalf("conflict 必须列出 2 个节点: %+v", conflict["nodes"])
	}
	first, _ := nodes[0].(map[string]any)
	if first["node_id"] != "node-20260101T000000Z-302" {
		t.Fatalf("冲突节点顺序不稳定（应按 node_id 升序）: %+v", nodes)
	}
}

// TestHandleChatWebAPISessionsResume_MeshDisabledKeepsLegacySemantics 锁定 MN1：
// 网格关闭（--mesh=false）时 resume 不做归属检查、sessions 也不并入 peer，
// 全部走旧语义——即使磁盘上还留着声称该会话的活节点档案。
func TestHandleChatWebAPISessionsResume_MeshDisabledKeepsLegacySemantics(t *testing.T) {
	host := meshTestHost(t)
	seedMeshSessionsTestNode(t, host.Paths(), "node-20260101T000000Z-401", "session-history", `E:\ws\two`, true)
	session := newMeshWebSessionsTestSession(t, "session-current", nil, "session-history")
	withWebTestSession(t, session)

	mesh.SetCurrent(nil) // 等价于 --mesh=false

	rec := postChatWebResume(t, "session-history", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := decodeChatWebJSONMap(t, rec); got["status"] != "queued" {
		t.Fatalf("网格关闭时 resume 必须保持旧语义（queued）: %+v", got)
	}
	if count := queuedInputCount(session.InputQueue); count != 1 {
		t.Fatalf("网格关闭时 resume 必须注入队列，queued=%d", count)
	}

	// 旧口径清单：scope=all 也不得凭空造 peer 条目，网格字段整体退化。
	listRec := httptest.NewRecorder()
	HandleChatWebAPISessions(listRec, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?scope=all", nil))
	body := decodeChatWebSessionsTestResponse(t, listRec)
	if len(body.Sessions) != 2 {
		t.Fatalf("网格关闭时 scope=all 不得并入 peer: %+v", body.Sessions)
	}
	if body.Self != nil {
		t.Fatalf("网格关闭时 self 必须为 null: %+v", body.Self)
	}
}

// TestChatWebSessionsAssetHasMeshOwnershipView 是 S11 的前端契约门禁（§19.3）。
//
// 前端无构建步骤，沿用 web_handlers_mesh_spawn_test.go 的 asset 字符串断言：
// 徽标 / 端点行、跨工作区分组、打开方式开关、resume 冲突弹窗、关于页网格小节。
// 同时锁定前端红线（Web 子方案 §5.5）：sessions.js 不得出现令牌名，
// 也不得自行拼接 peer 调用路径（跨节点动作只能经本进程的 /web/api/mesh/*）。
func TestChatWebSessionsAssetHasMeshOwnershipView(t *testing.T) {
	cases := []struct {
		name string
		path string
		want []string
		deny []string
	}{
		{
			name: "会话列表（徽标 / 分组 / 开关 / 冲突）",
			path: ChatWebPath + "js/sessions.js",
			want: []string{
				// P0 ① 徽标 + 端点行（§5.1）：形状 + 文本双编码（Q12）。
				"session-badge",
				"session-endpoint",
				"session_state",
				"ownership",
				"conflict_count",
				"workspace_name",
				// P0 ③ / P1 ⑥ 打开方式开关（§5.1 / Q13）。
				"sessions-open-mode",
				"webSessionOpenMode",
				// P1 ④ 跨工作区分组（§5.3）：数据来自 scope=all，分组只是呈现方式。
				"scope=all",
				"session-group",
				"webOtherWorkspacesCollapsed",
				// P1 ③ resume 冲突（§5.7 / §6.3，D12）：三段式 + force 逃生门。
				"session-conflict-overlay",
				"session-conflict-open-btn",
				"session-conflict-force-btn",
				"running_elsewhere",
				"payload.force = true",
			},
			// 前端红线：令牌不进任何浏览器存储 / 不经前端转发（M7）。
			deny: []string{"aicli-web-token", "localStorage.setItem(\"webToken\""},
		},
		{
			name: "页面骨架（开关 + 冲突弹窗 + 关于页网格小节）",
			path: ChatWebPath,
			want: []string{
				`id="sessions-open-mode"`,
				`id="session-conflict-overlay"`,
				`id="session-conflict-text"`,
				`id="session-conflict-open-btn"`,
				`id="session-conflict-force-btn"`,
				`id="about-mesh"`,
			},
		},
		{
			name: "关于页网格小节（只读数据源）",
			path: ChatWebPath + "js/ui.js",
			want: []string{
				"about-mesh",
				"/web/api/mesh/self",
				"/web/api/mesh/peers",
				"loadAboutMesh",
			},
			// §5.8：关于页只读，不提供 gc / stop / spawn 按钮。
			deny: []string{"mesh/gc", "mesh/stop", "mesh/spawn"},
		},
		{
			name: "样式（徽标 / 分组 / 开关 / 冲突弹窗）",
			path: ChatWebPath + "style.css",
			want: []string{
				".session-badge",
				".session-endpoint",
				".session-ws-other",
				".sb-conflict",
				".session-group-body",
				".session-open-mode",
				"#session-conflict-overlay",
				"#session-conflict-footer",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200", tc.path, rec.Code)
			}
			body := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("%s 缺少契约标识 %q", tc.path, want)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(body, deny) {
					t.Errorf("%s 出现禁止内容 %q（前端红线）", tc.path, deny)
				}
			}
		})
	}
}
