package commands

import (
	"fmt"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件是 chat 进程与 mesh（多进程网格）的接入点，对应实施方案 S2：
//
//   - 会话激活 / 切换 / 恢复 → 更新本进程节点档案的 session 段；
//   - 每个 turn 开始 / 结束 → 翻转 session.busy（低频率，不做防抖）；
//   - loopback 服务器就绪 → 由 cmd/aicli 调用 mesh.SetCurrentEndpoint 补
//     endpoint/auth/capabilities（见 cmd/aicli/mesh_bootstrap.go）。
//
// 所有调用都必须容忍"没有网格"：--mesh=false、无法解析网格根目录、非 chat
// 进程都只是让 mesh.Current() 为 nil，这些函数随即退化成 no-op。网格侧的
// 任何失败都不会上报为 chat 错误（MN1）。

// meshTurnMu guards the nested-turn counter below. Turns may overlap (a
// foreground turn plus an auto-continue, or a goal continuation), so busy is
// reference counted: only the outermost turn end flips the flag back.
var (
	meshTurnMu    sync.Mutex
	meshTurnDepth int
)

// meshTurnIDForSession derives the stable turn id published in the node record
// ("turn-0007"), mirroring the chat log scope naming.
func meshTurnIDForSession(session *ChatSession) string {
	if session == nil {
		return ""
	}
	index := session.MsgCount
	if index <= 0 {
		index = 1
	}
	return turnIDForIndex(index)
}

// turnIDForIndex renders the canonical turn id ("turn-0007") shared by the chat
// log scope and the mesh node record.
func turnIDForIndex(index int) string {
	if index <= 0 {
		index = 1
	}
	return fmt.Sprintf("turn-%04d", index)
}

// syncChatMeshSession publishes the session currently bound to this process.
// It is called from the two session-binding funnels (createNewRuntimeConversation
// / restoreChatStateFromRuntimeSession), so /new, resume, web session switch and
// goal reconciliation are all covered by one hook.
func syncChatMeshSession(session *ChatSession) {
	host := mesh.Current()
	if host == nil || session == nil || session.RuntimeSession == nil {
		return
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return
	}
	// S15：`aicli-mesh open --takeover` 拉起的进程带 AICLI_MESH_TAKEOVER=1，
	// 在首个会话激活时显式回收租约（§4.4）。旧节点不会被杀：它在下一次心跳
	// 发现租约易主，把自己的档案标成 orphaned 并提示。
	if meshTakeoverRequested() {
		if status := host.TakeoverSession(sessionID); !status.OK {
			notifyMeshTakeoverFailure(sessionID, status)
		}
	}
	mesh.SetCurrentSession(&mesh.SessionInfo{
		ID:          sessionID,
		Title:       chatMeshSessionTitle(session),
		ActivatedAt: mesh.NowUTC(),
	})
}

// clearChatMeshSession drops the session section (node back to "no session").
func clearChatMeshSession() {
	if mesh.Current() == nil {
		return
	}
	mesh.SetCurrentSession(nil)
}

// chatMeshSessionTitle mirrors the title resolution used for chat logs.
func chatMeshSessionTitle(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	title := strings.TrimSpace(session.RuntimeSession.Metadata.Title)
	if preview := session.RuntimeSession.BuildPreview(); preview != nil && strings.TrimSpace(preview.Title) != "" {
		title = preview.Title
	}
	return title
}

// beginChatMeshTurn marks the node busy for one turn and returns the release
// function. Callers use it as:
//
//	releaseMeshTurn := beginChatMeshTurn(session)
//	defer releaseMeshTurn()
//
// The returned function is idempotent and safe on the no-mesh path.
func beginChatMeshTurn(session *ChatSession) func() {
	if mesh.Current() == nil {
		return func() {}
	}
	turnID := meshTurnIDForSession(session)
	meshTurnMu.Lock()
	meshTurnDepth++
	if meshTurnDepth == 1 {
		mesh.SetCurrentBusy(true, turnID)
	}
	meshTurnMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			meshTurnMu.Lock()
			meshTurnDepth--
			if meshTurnDepth <= 0 {
				meshTurnDepth = 0
				mesh.SetCurrentBusy(false, "")
			}
			meshTurnMu.Unlock()
		})
	}
}

// persistChatMeshBinding publishes this process's loopback address as the
// preferred address of sessionID (mesh/bindings/<session-id>.json). It is the
// S3 replacement for persistChatWebPortForSession: the sticky-port behaviour is
// unchanged, only the storage moved from ~/.aicli/web-ports/ to the mesh.
//
// best-effort：没有 loopback 服务器、网格根目录不可用或写盘失败都只是静默
// 跳过，绝不影响会话流程（MN1 / 降级矩阵 §4.7）。
func persistChatMeshBinding(sessionID, workspacePath string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	host, port, ok := mesh.ProcessEndpoint()
	if !ok {
		return
	}
	update := mesh.BindingUpdate{
		SessionID:     sessionID,
		Host:          host,
		Port:          port,
		WorkspacePath: strings.TrimSpace(workspacePath),
	}
	if current := mesh.Current(); current != nil {
		update.NodeID = current.NodeID()
	}
	_ = mesh.TouchBinding(mesh.ResolvePaths(), update)
}

// persistChatMeshBindingForSession is the session-funnel wrapper: it derives the
// session id and workspace from the active runtime session.
func persistChatMeshBindingForSession(session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}
	persistChatMeshBinding(
		session.RuntimeSession.ID,
		runtimeSessionWorkspacePath(session.RuntimeSession),
	)
}
