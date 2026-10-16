package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	chatRuntimeContextPermissionMode = sessionmeta.LegacyAICLIPermissionMode
	chatRuntimeContextActiveTeamID   = sessionmeta.LegacyAICLIActiveTeamID
	chatRuntimeContextActiveAgentID  = sessionmeta.LegacyAICLIActiveTeamAgentID
	chatRuntimeContextActiveTaskID   = sessionmeta.LegacyAICLIActiveTeamTaskID
	chatRuntimeContextSelectedAgent  = sessionmeta.LegacyAICLISelectedAgent
)

type chatTeamBinding struct {
	TeamID         string
	AgentID        string
	TaskID         string
	PermissionMode runtimepolicy.Mode
}

func (b *chatTeamBinding) Clone() *chatTeamBinding {
	if b == nil {
		return nil
	}
	clone := *b
	return &clone
}

// chatRuntimeContextSnapshot is an immutable point-in-time view of the
// runtime-context fields shared with the actor event bridge.
type chatRuntimeContextSnapshot struct {
	DebugMode               bool
	PermissionMode          runtimepolicy.Mode
	RequestedPermissionMode string
	EffectivePermissionMode string
	ApprovalReuseMode       chatApprovalReuseMode
	SelectedAgentTarget     string
	ActiveTeam              *chatTeamBinding
}

func snapshotChatRuntimeContext(session *ChatSession) chatRuntimeContextSnapshot {
	if session == nil {
		return chatRuntimeContextSnapshot{}
	}
	session.runtimeCtxMu.RLock()
	defer session.runtimeCtxMu.RUnlock()
	return chatRuntimeContextSnapshot{
		DebugMode:               session.DebugMode,
		PermissionMode:          session.PermissionMode,
		RequestedPermissionMode: session.RequestedPermissionMode,
		EffectivePermissionMode: session.EffectivePermissionMode,
		ApprovalReuseMode:       session.ApprovalReuseMode,
		SelectedAgentTarget:     session.SelectedAgentTarget,
		ActiveTeam:              session.ActiveTeam.Clone(),
	}
}

func chatSessionDebugMode(session *ChatSession) bool {
	return snapshotChatRuntimeContext(session).DebugMode
}

func chatSessionPermissionMode(session *ChatSession) runtimepolicy.Mode {
	return snapshotChatRuntimeContext(session).PermissionMode
}

func chatSessionApprovalReuseMode(session *ChatSession) chatApprovalReuseMode {
	return snapshotChatRuntimeContext(session).ApprovalReuseMode
}

func chatSessionSelectedAgentTarget(session *ChatSession) string {
	return snapshotChatRuntimeContext(session).SelectedAgentTarget
}

func chatSessionActiveTeam(session *ChatSession) *chatTeamBinding {
	return snapshotChatRuntimeContext(session).ActiveTeam
}

func chatSessionRequestedPermissionMode(session *ChatSession) string {
	return snapshotChatRuntimeContext(session).RequestedPermissionMode
}

func chatSessionEffectivePermissionMode(session *ChatSession) string {
	return snapshotChatRuntimeContext(session).EffectivePermissionMode
}

func setChatDebugModeState(session *ChatSession, enabled bool) {
	if session == nil {
		return
	}
	session.runtimeCtxMu.Lock()
	session.DebugMode = enabled
	session.runtimeCtxMu.Unlock()
}

func setChatPermissionMode(session *ChatSession, mode runtimepolicy.Mode) {
	if session == nil {
		return
	}
	// disable_bypass (§4.9) is enforced here as well as in the engine so the
	// session never reports a bypass mode the policy refuses to honor.
	if mode == runtimepolicy.ModeBypassPermissions && session.PermissionsOverlay.DisableBypass {
		printfChatCommandOutput(session, "提示: disable_bypass 已启用（permissions 分层），无法切换到 bypass_permissions")
		return
	}
	session.runtimeCtxMu.Lock()
	session.PermissionMode = mode
	session.RequestedPermissionMode = string(mode)
	session.EffectivePermissionMode = string(mode)
	if session.ActiveTeam != nil {
		session.ActiveTeam.PermissionMode = mode
	}
	session.runtimeCtxMu.Unlock()
}

func setChatApprovalReuseMode(session *ChatSession, mode chatApprovalReuseMode) {
	if session == nil {
		return
	}
	session.runtimeCtxMu.Lock()
	session.ApprovalReuseMode = mode
	session.runtimeCtxMu.Unlock()
}

func setChatSelectedAgentTarget(session *ChatSession, target string) {
	if session == nil {
		return
	}
	session.runtimeCtxMu.Lock()
	session.SelectedAgentTarget = target
	session.runtimeCtxMu.Unlock()
}

func setChatActiveTeam(session *ChatSession, binding *chatTeamBinding) {
	if session == nil {
		return
	}
	session.runtimeCtxMu.Lock()
	session.ActiveTeam = binding.Clone()
	session.runtimeCtxMu.Unlock()
}

// chatRestoredRuntimeContext 是一次「读锁内」取出的运行时会话上下文字段。
type chatRestoredRuntimeContext struct {
	approvalReuse  string
	permissionMode string
	debugMode      bool
	debugModeOK    bool
	selectedAgent  string
	activeTeamID   string
	activeAgentID  string
	activeTaskID   string
}

// readChatRestoredRuntimeContext 必须在 runtimeContextMapMu 读锁下执行（由
// chatRuntimeSessionRead 保证），只读该 map，不得再进入其它加锁辅助函数。
func readChatRestoredRuntimeContext(runtimeSession *runtimechat.Session) chatRestoredRuntimeContext {
	debugMode, debugModeOK := runtimeSessionContextBool(runtimeSession, chatRuntimeContextDebugMode)
	return chatRestoredRuntimeContext{
		approvalReuse:  runtimeSessionContextString(runtimeSession, chatRuntimeContextApprovalReuse),
		permissionMode: runtimeSessionContextString(runtimeSession, chatRuntimeContextPermissionMode),
		debugMode:      debugMode,
		debugModeOK:    debugModeOK,
		selectedAgent:  runtimeSessionContextString(runtimeSession, chatRuntimeContextSelectedAgent),
		activeTeamID:   runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveTeamID),
		activeAgentID:  runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveAgentID),
		activeTaskID:   runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveTaskID),
	}
}

// restoreChatRuntimeContext 把持久化会话里的运行时节段恢复进 ChatSession。
//
// 本函数会被后台 goroutine 调用（goal actor 对账、tool session 上下文刷新等），
// 而共享 context map 同时会被 TUI 侧在 runtimeContextMapMu 下写入，因此 map 读取
// 必须在读锁内完成。这里刻意分两段：先在叶子读锁下取值，再在 runtimeCtxMu 下
// 落字段——两个叶子锁不得互相嵌套（会与反向获取顺序形成死锁）。
func restoreChatRuntimeContext(session *ChatSession) {
	if session == nil {
		return
	}
	restored, ok := chatRuntimeSessionRead(session, readChatRestoredRuntimeContext)
	if !ok {
		return
	}
	session.runtimeCtxMu.Lock()
	defer session.runtimeCtxMu.Unlock()
	if mode, err := parseChatApprovalReuseMode(restored.approvalReuse); err == nil {
		session.ApprovalReuseMode = mode
	}
	// CLI 显式指定的权限模式（--yolo / --permission-mode）优先于
	// 持久化会话中存储的模式：恢复会话时不得用旧会话的权限设置
	// 覆盖用户本次命令行上的明确要求（否则 --yolo 会被降级为
	// default，导致工具调用继续弹审批）。
	if !session.permissionModeCLIChanged {
		if mode, err := parseChatPermissionMode(restored.permissionMode, false); err == nil {
			// plan is a durable lifecycle, not a CLI mode: the stored canonical
			// key may legitimately read "plan" while the lifecycle is active
			// (see syncRuntimeSessionFromChatMode), but restoring it here would
			// poison session.PermissionMode and keep the engine in plan mode
			// even after exit_plan_mode rewrites the row.
			if mode != runtimepolicy.ModePlan {
				session.PermissionMode = mode
			}
		}
	}
	if restored.debugModeOK {
		session.DebugMode = restored.debugMode
	} else {
		session.DebugMode = false
	}
	session.SelectedAgentTarget = restored.selectedAgent
	if restored.activeTeamID == "" {
		session.ActiveTeam = nil
		return
	}
	session.ActiveTeam = &chatTeamBinding{
		TeamID:         restored.activeTeamID,
		AgentID:        firstNonEmptyChatValue(restored.activeAgentID, "lead"),
		TaskID:         restored.activeTaskID,
		PermissionMode: session.PermissionMode,
	}
}

func validateAmbientTeamBinding(session *ChatSession, store team.Store) {
	if session == nil || store == nil {
		return
	}
	binding := chatSessionActiveTeam(session)
	if binding == nil {
		return
	}
	record, err := store.GetTeam(contextBackground(), binding.TeamID)
	if err != nil || record == nil {
		fmt.Fprintf(os.Stderr, "Warning: 清理失效的 active team 绑定: %s\n", binding.TeamID)
		session.runtimeCtxMu.Lock()
		if session.ActiveTeam != nil && session.ActiveTeam.TeamID == binding.TeamID {
			session.ActiveTeam = nil
		}
		session.runtimeCtxMu.Unlock()
		return
	}
	if binding.TaskID == "" {
		return
	}
	task, err := store.GetTask(contextBackground(), binding.TaskID)
	if err != nil || task == nil || strings.TrimSpace(task.TeamID) != strings.TrimSpace(binding.TeamID) {
		fmt.Fprintf(os.Stderr, "Warning: 清理失效的 active task 绑定: %s\n", binding.TaskID)
		session.runtimeCtxMu.Lock()
		if session.ActiveTeam != nil && session.ActiveTeam.TeamID == binding.TeamID && session.ActiveTeam.TaskID == binding.TaskID {
			session.ActiveTeam.TaskID = ""
		}
		session.runtimeCtxMu.Unlock()
	}
}

func inferAmbientTeamBinding(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil || runtimeSession == nil {
		return
	}
	session.runtimeCtxMu.Lock()
	defer session.runtimeCtxMu.Unlock()
	toolCallNames := map[string]string{}
	for _, message := range runtimeSession.History {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				continue
			}
			toolCallNames[strings.TrimSpace(call.ID)] = strings.TrimSpace(call.Name)
		}
	}

	for i := len(runtimeSession.History) - 1; i >= 0; i-- {
		message := runtimeSession.History[i]
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		toolName := toolCallNames[strings.TrimSpace(message.ToolCallID)]
		if toolName == "" {
			continue
		}
		payload := decodeToolResultPayload(message.Content)
		if payload == nil {
			payload = map[string]interface{}{}
		}
		if metadataPayload := messageToolMetadata(message.Metadata); len(metadataPayload) > 0 {
			for key, value := range metadataPayload {
				if _, exists := payload[key]; !exists {
					payload[key] = value
				}
			}
		}
		switch toolName {
		case toolbroker.ToolTeamSpawn:
			teamID := firstNonEmptyChatValue(payloadStringValue(payload["team_id"]))
			taskID := firstNonEmptyChatValue(payloadStringValue(payload["task_id"]))
			if teamID == "" {
				continue
			}
			if session.ActiveTeam == nil {
				session.ActiveTeam = &chatTeamBinding{}
			}
			session.ActiveTeam.TeamID = teamID
			session.ActiveTeam.AgentID = "lead"
			if taskID != "" {
				session.ActiveTeam.TaskID = taskID
			}
			if session.ActiveTeam.PermissionMode == "" {
				session.ActiveTeam.PermissionMode = session.PermissionMode
			}
			return
		case toolbroker.ToolTeamTaskSpec, toolbroker.ToolTeamTaskContext, toolbroker.ToolTeamTaskReport, toolbroker.ToolTeamTaskBlock:
			teamID := firstNonEmptyChatValue(payloadStringValue(payload["team_id"]), payloadNestedStringValue(payload, "spec", "team_id"))
			taskID := firstNonEmptyChatValue(payloadStringValue(payload["task_id"]), payloadNestedStringValue(payload, "spec", "task_id"))
			if teamID == "" && session.ActiveTeam == nil {
				continue
			}
			if session.ActiveTeam == nil {
				session.ActiveTeam = &chatTeamBinding{AgentID: "lead"}
			}
			if teamID != "" {
				session.ActiveTeam.TeamID = teamID
			}
			if taskID != "" {
				session.ActiveTeam.TaskID = taskID
			} else if toolName == toolbroker.ToolTeamTaskReport || toolName == toolbroker.ToolTeamTaskBlock {
				session.ActiveTeam.TaskID = ""
			}
			if session.ActiveTeam.AgentID == "" {
				session.ActiveTeam.AgentID = "lead"
			}
			return
		}
	}
}

func syncChatRuntimeContext(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil || runtimeSession == nil {
		return
	}
	if runtimeSession.Metadata.Context == nil {
		runtimeSession.Metadata.Context = make(map[string]interface{})
	}
	ctx := snapshotChatRuntimeContext(session)
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Client, "aicli")
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Entrypoint, "aicli")
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.PermissionMode, string(ctx.PermissionMode), chatRuntimeContextPermissionMode)
	if profileRef := strings.TrimSpace(session.ProfileReference); profileRef != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ProfileRef, profileRef, sessionmeta.LegacyAPIProfileReference)
	} else {
		sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ProfileRef, sessionmeta.LegacyAPIProfileReference)
	}
	// 工作区写入走会话权威锚点（方案 2）：启动/恢复时由 freezeChatWorkspaceAnchor
	// 固化，profile 热切换不改写。workspace_path 属于会话身份（N9：不随 cwd 漂移），
	// sync 的职责是**保持**而不是重算：
	//   1. 锚点非空 → 以锚点为准；
	//   2. 已持久化值非空 → 保持（2026-10-15 修复：此前 sync 每次重算并覆盖，
	//      是 /profile use 切换后工作目录漂移的直接落盘点）；
	//   3. 两者皆空（极早期 sync / 手工构造的会话）→ 按既有解析填充一次。
	//   解析本身不得被 profile 根污染（resolveLocalWorkspacePath 已不再锚定
	//   ProfileRoot）。
	if workspacePath := strings.TrimSpace(session.WorkspaceAnchor); workspacePath != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.WorkspacePath, workspacePath)
	} else if existing := strings.TrimSpace(runtimeSessionContextString(runtimeSession, sessionmeta.WorkspacePath)); existing != "" {
		// 保持已持久化的会话工作区。
	} else if workspacePath := strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session)); workspacePath != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.WorkspacePath, workspacePath)
	}
	if session.Config != nil {
		if configFile := strings.TrimSpace(session.Config.ConfigFilePath); configFile != "" {
			sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ConfigFile, configFile, sessionmeta.LegacyAICLIConfigFile)
		}
	}
	selectedAgentTarget := strings.TrimSpace(ctx.SelectedAgentTarget)
	if selectedAgentTarget != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.SelectedAgent, selectedAgentTarget, chatRuntimeContextSelectedAgent)
	} else {
		sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.SelectedAgent, chatRuntimeContextSelectedAgent)
	}
	requestedModel := strings.TrimSpace(session.Model)
	if requestedModel != "" {
		runtimeSession.Metadata.Context[toolbroker.AgentSessionContextRequestedModel] = requestedModel
	} else {
		delete(runtimeSession.Metadata.Context, toolbroker.AgentSessionContextRequestedModel)
	}
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamID, chatRuntimeContextActiveTeamID)
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamAgentID, chatRuntimeContextActiveAgentID)
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamTaskID, chatRuntimeContextActiveTaskID)
	if ctx.ActiveTeam == nil || strings.TrimSpace(ctx.ActiveTeam.TeamID) == "" {
		return
	}
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamID, strings.TrimSpace(ctx.ActiveTeam.TeamID), chatRuntimeContextActiveTeamID)
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamAgentID, firstNonEmptyChatValue(ctx.ActiveTeam.AgentID, "lead"), chatRuntimeContextActiveAgentID)
	if strings.TrimSpace(ctx.ActiveTeam.TaskID) != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamTaskID, strings.TrimSpace(ctx.ActiveTeam.TaskID), chatRuntimeContextActiveTaskID)
	}
}

func payloadStringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func stringSliceValueAny(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				values = append(values, item)
			}
		}
		return values
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := payloadStringValue(item); text != "" {
				values = append(values, text)
			}
		}
		return values
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			return []string{text}
		}
	}
	return nil
}

func payloadNestedStringValue(payload map[string]interface{}, key string, nested string) string {
	if payload == nil {
		return ""
	}
	child, _ := payload[key].(map[string]interface{})
	if child == nil {
		return ""
	}
	return payloadStringValue(child[nested])
}

func decodeToolResultPayload(content string) map[string]interface{} {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	content = content[start : end+1]
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	return payload
}

func contextBackground() context.Context { return context.Background() }

func messageToolMetadata(metadata runtimetypes.Metadata) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	merged := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		merged[key] = value
	}
	if value, ok := metadata.Get("tool_metadata"); ok {
		if child, ok := value.(map[string]interface{}); ok {
			for key, nested := range child {
				if _, exists := merged[key]; !exists {
					merged[key] = nested
				}
			}
		}
	}
	return merged
}
