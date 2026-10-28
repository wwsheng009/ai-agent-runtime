package commands

// chat_gateway_window.go 把 gateway 管理端下发的「窗口动作」落到本机执行。
//
// 链路（与控制台的按钮一一对应）：
//
//	console POST /api/nodes/{n}/sessions/{s}/window?action=open-terminal
//	  → gateway 排进该节点的窗口动作队列
//	  → 下一次 HEARTBEAT_ACK 的 Pending 带 window:open-terminal:<sid>
//	  → agent 的 ApplyPending 解析并串行投递到这里
//	  → 本进程用 termopen 在**这台机器**上开一个新终端窗口跑 `aicli resume <sid>`
//	    （参数来自该会话持久化的 launch_args，也就是用户当初的 --web/--yolo/--debug）
//
// 三条边界：
//   - 动作只在节点本机执行：网关从不猜测远端的终端形态，也不跨机器传路径；
//   - 会话工作目录必须跟着走，否则窗口里的会话会绑到错误的工作区（§4.3.1 目录
//     绑定不漂移）；
//   - 执行失败绝不写 stderr——本进程可能正拥有 TUI，裸写会撕裂画面。诊断统一走
//     NotifyChatDiagnostic（有界面就投动态栏，没有再退 stderr）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/termopen"
)

// gatewayCapabilityWindowActions 是本节点向网关通告「我能在新终端里打开会话」。
// 控制台的按钮可见性就挂在这个能力位上：没探测到可用终端后端的机器（纯 SSH、
// CI 容器）不通告，控制台也就不显示——能力位是承诺，不是装饰。
const gatewayCapabilityWindowActions = "window-actions"

// chatGatewayWindowActionsAvailable 报告本机是否存在可用终端后端。
func chatGatewayWindowActionsAvailable() bool {
	return termopen.Available()
}

// chatGatewayWorkspaceInfo 上报会话在**本机**的工作目录（控制台「在新终端打开」
// 必须落在这里，否则窗口里的会话会绑到启动时的 cwd，违反 §4.3.1「目录绑定不随
// 会话漂移」）。未绑定目录时返回 nil，让网关保持字段缺失而不是一个空对象。
func chatGatewayWorkspaceInfo(session *ChatSession) *wire.WorkspaceInfo {
	path := gatewayWindowWorkspace(session)
	if path == "" {
		return nil
	}
	name := ""
	if cleaned, err := filepath.Abs(path); err == nil {
		name = filepath.Base(cleaned)
	} else {
		name = filepath.Base(path)
	}
	return &wire.WorkspaceInfo{Path: path, Name: strings.TrimSpace(name)}
}

// chatGatewayWindowHandler 返回装配进控制器的窗口动作执行器。
//
// 执行器被 agent 的单执行 goroutine 串行调用，因此这里可以直接开进程、调 mesh
// 编排（graceful 停止最长等 30s）而不用担心撞上心跳读循环。
func chatGatewayWindowHandler(session *ChatSession) func(gatewayagent.WindowAction) {
	return func(action gatewayagent.WindowAction) {
		message, err := runGatewayWindowAction(session, action)
		if err != nil {
			note := fmt.Sprintf("网关窗口动作 %s 失败: %v", action.Kind, err)
			if message != "" {
				note += "（" + message + "）"
			}
			notifyGatewayWindowDiagnostic(session, note)
			return
		}
		notifyGatewayWindowDiagnostic(session, fmt.Sprintf("网关窗口动作 %s 已完成: %s", action.Kind, message))
	}
}

// runGatewayWindowAction 是动作到本机执行的唯一映射（无 UI 依赖，可单测）。
func runGatewayWindowAction(session *ChatSession, action gatewayagent.WindowAction) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action.Kind)) {
	case wire.WindowActionOpenTerminal, wire.WindowActionResume:
		return openGatewaySessionWindow(session, action.SessionID)
	case wire.WindowActionNew:
		return openGatewayNewSessionWindow(session)
	case wire.WindowActionClose:
		return closeGatewaySessionWindow(session, action.SessionID)
	default:
		return "", fmt.Errorf("未知的窗口动作 %q", action.Kind)
	}
}

// openGatewaySessionWindow 在节点所在机器的新终端里恢复某个会话。
func openGatewaySessionWindow(session *ChatSession, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("窗口动作缺少会话 ID")
	}
	store, err := gatewayWindowStore(session)
	if err != nil {
		return "", err
	}
	defer store.Stop()

	item, err := store.itemByID(sessionID)
	if err != nil {
		return "", err
	}
	launcher := sessionsTerminalLauncher("")
	if _, err := launcher.Detect(); err != nil {
		return "", fmt.Errorf("本机无法打开终端窗口: %w", err)
	}
	return openSessionInNewTerminal(item, launcher)
}

// openGatewayNewSessionWindow 在本机新起一个会话窗口（`aicli chat`，工作目录沿用
// 当前会话绑定的工作区，避免新窗口落在错误目录里）。
func openGatewayNewSessionWindow(session *ChatSession) (string, error) {
	launcher := sessionsTerminalLauncher("")
	if _, err := launcher.Detect(); err != nil {
		return "", fmt.Errorf("本机无法打开终端窗口: %w", err)
	}
	binary := currentLaunchBinaryOrSelf()
	dir := gatewayWindowWorkspace(session)
	result, err := launcher.Launch(termopen.Request{
		Title:  "aicli · 新会话",
		Dir:    dir,
		Binary: binary,
		Args:   []string{"chat"},
		Env:    []string{envSessionDetachedLauncher + "=gateway"},
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已在新终端打开新会话（后端 %s，pid %d）", result.Plan.Backend, result.PID), nil
}

// closeGatewaySessionWindow 关闭某个会话在本机的窗口进程。
//
// 复用 mesh 的停止编排而不是自己 kill：graceful 模式投的是 `/exit`，因此保存会话、
// 注销节点档案、释放租约都走目标自己的退出路径（与用户在 TUI 里敲 /exit 完全等价）。
// 「本进程关自己」被 mesh 一律拒绝（StopCodeSelfRefused），这里把它翻译成操作者能
// 照做的提示，而不是一个含糊的失败。
func closeGatewaySessionWindow(session *ChatSession, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("窗口动作缺少会话 ID")
	}
	// 「别自杀」的判定排在 mesh 可用性之前：这条原因恒定成立，而 --mesh=false 的
	// 进程根本编排不了停止——先报更具体、更可行动的那一条。
	if currentID := currentRuntimeSessionID(session); currentID != "" && strings.EqualFold(currentID, sessionID) {
		return "", fmt.Errorf("目标就是本窗口，拒绝自杀；请在该窗口的 TUI 里用 /exit 退出")
	}
	host := mesh.Current()
	if host == nil {
		return "", fmt.Errorf("mesh 未启用（本进程以 --mesh=false 启动，无法从网关关闭其它会话窗口；请在窗口内用 /exit 退出）")
	}
	result := mesh.StopNode(context.Background(), host, host.NodeID(), mesh.StopRequest{
		Target: sessionID,
		Mode:   mesh.StopModeGraceful,
	})
	if result == nil {
		return "", fmt.Errorf("停止会话窗口 %s 失败: 无结果", sessionID)
	}
	message := strings.TrimSpace(result.Message)
	if result.OK() {
		return fmt.Sprintf("已关闭会话窗口 %s（node %s, pid %d）", sessionID, result.NodeID, result.PID), nil
	}
	if result.Status == mesh.StopStatusRefused && result.Code == mesh.StopCodeSelfRefused {
		return "", fmt.Errorf("目标窗口是本进程，拒绝自杀；请在该窗口的 TUI 里用 /exit 退出")
	}
	if message == "" {
		message = result.Status
	}
	return fmt.Sprintf("node=%s pid=%d code=%s", result.NodeID, result.PID, result.Code), fmt.Errorf("关闭会话窗口失败: %s", message)
}

// gatewayWindowStore 为窗口动作打开一个会话库句柄（全工作目录范围：控制台上的
// 行本来就可能来自任何目录）。
func gatewayWindowStore(session *ChatSession) (*sessionsBrowserStore, error) {
	var cfg *config.Config
	if session != nil {
		cfg = session.Config
	}
	req := sessionsCommandRequest{
		All:           true,
		SessionDir:    gatewayWindowSessionDir(session),
		User:          gatewayWindowSessionUser(session),
		Limit:         sessionsBrowserPageSize,
		LimitExplicit: true,
	}
	return openSessionsBrowserStore(cfg, req)
}

// gatewayWindowSessionDir/User 优先复用会话自己的解析结果，避免同一个进程里
// 出现两个指向不同目录的会话管理器（那会让「控制台上看到的会话」和「本机真的
// 存在这个会话」不一致）。
func gatewayWindowSessionDir(session *ChatSession) string {
	if session != nil {
		return strings.TrimSpace(session.SessionDir)
	}
	return ""
}

func gatewayWindowSessionUser(session *ChatSession) string {
	if session != nil {
		return strings.TrimSpace(session.SessionUserID)
	}
	return ""
}

// gatewayWindowWorkspace 是新窗口的启动目录：会话锚点优先，其次进程 cwd。
func gatewayWindowWorkspace(session *ChatSession) string {
	if session != nil {
		// 会话自己的权威工作区优先（RuntimeSession 的 workspace_path 是会话身份
		// 的一部分）；锚点次之；都没有才退到 cwd。
		if path := strings.TrimSpace(runtimeSessionWorkspacePath(session.RuntimeSession)); path != "" {
			return path
		}
		if anchor := strings.TrimSpace(session.WorkspaceAnchor); anchor != "" {
			return anchor
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// notifyGatewayWindowDiagnostic 把窗口动作的结果投递到统一诊断通道。
func notifyGatewayWindowDiagnostic(session *ChatSession, note string) {
	note = strings.TrimSpace(note)
	if note == "" {
		return
	}
	if NotifyChatDiagnostic(note) {
		return
	}
	// 没有存活界面（headless / 兼容模式）时才落 stderr：这一条路径上没人拥有画面。
	fmt.Fprintln(os.Stderr, note)
}

// chatGatewayMeshWindow 描述本进程在这台机器上的 mesh 窗口（节点 ID + 端口 + pid），
// 供控制台会话行显示「这个会话此刻在哪个窗口里」。
//
// 只取身份与端点，**绝不取 Auth.Token**：写令牌离开本机的方式只能是本机自己打印。
func chatGatewayMeshWindow(session *ChatSession) string {
	host := mesh.Current()
	if host == nil {
		return ""
	}
	record := host.RecordSnapshot()
	parts := make([]string, 0, 3)
	if nodeID := strings.TrimSpace(record.NodeID); nodeID != "" {
		parts = append(parts, "node "+nodeID)
	}
	if record.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", record.PID))
	}
	if record.Endpoint != nil {
		if url := strings.TrimSpace(firstNonEmptyChatValue(record.Endpoint.WebBaseURL, record.Endpoint.BaseURL)); url != "" {
			parts = append(parts, url)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// currentLaunchBinaryOrSelf 返回新窗口该执行的 aicli 路径。
func currentLaunchBinaryOrSelf() string {
	if binary, _ := currentLaunchBinaryAndCwd(); strings.TrimSpace(binary) != "" {
		return termopen.AbsolutePathForTerminal(binary)
	}
	return "aicli"
}
