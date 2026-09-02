package commands

import (
	"fmt"
	"sort"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ============================================================================
// /debug display 面板信息区块的 HTTP 结构化投影
//
// /debug/chat/status 的 JSON 快照在渲染/显示状态（encoder/scene/output/
// app_state/executor/projection）之外，补齐 /debug display 面板上其余
// 信息区块的结构化视图，使面板展示的每一项信息都能通过 HTTP 端点以
// JSON（默认）或纯文本（?format=text）两种方式访问：
//
//   - files      会话文件与目录区块（session store/file、日志、产物目录）
//   - runtime    运行时调试区块（配置路径、permission、queued input 等）
//   - routing    Subagent/Team Routing 配置区块
//   - components 运行时组件区块（宿主核心组件 + 观察平面组件）
//   - agents     AgentControl Registry / Agent Graph / Mailbox Pending 区块
//
// 每个区块都是只读快照：只读取 session/provider 上的配置与状态，不做变更。
// ============================================================================

// chatDebugDisplayFilesInfo 对应面板"会话文件与目录:"区块。
type chatDebugDisplayFilesInfo struct {
	SessionLabel     string `json:"session,omitempty"`
	SessionStore     string `json:"session_store,omitempty"`
	SessionFile      string `json:"session_file,omitempty"`
	ChatLogFile      string `json:"chat_log_file,omitempty"`
	DebugLogFile     string `json:"debug_log_file,omitempty"`
	HTTPArtifactDir  string `json:"http_artifact_dir,omitempty"`
	ShellArtifactDir string `json:"shell_artifact_dir,omitempty"`
	ImageArtifactDir string `json:"generated_image_artifact_dir,omitempty"`
	LastHTTPRequest  string `json:"last_http_request,omitempty"`
	LastHTTPResponse string `json:"last_http_response,omitempty"`
	LastShellOut     string `json:"last_shell_out,omitempty"`
	Title            string `json:"title,omitempty"`
	Summary          string `json:"summary,omitempty"`
	HistoryMessages  int    `json:"history_messages,omitempty"`
}

// chatDebugDisplayRuntimeInfo 对应面板"运行时调试:"区块。
type chatDebugDisplayRuntimeInfo struct {
	AICLIConfigPath   string   `json:"aicli_config_path,omitempty"`
	ProfileRoot       string   `json:"profile_root,omitempty"`
	AgentSource       string   `json:"agent_source,omitempty"`
	RuntimeConfigPath string   `json:"runtime_config_path,omitempty"`
	MCPConfigPath     string   `json:"mcp_config_path,omitempty"`
	SkillDirs         []string `json:"skill_dirs,omitempty"`
	OutputFormat      string   `json:"output_format,omitempty"`
	NoInteractive     bool     `json:"no_interactive"`
	JSONOutput        bool     `json:"json_output"`
	JSONEnvelope      bool     `json:"json_envelope"`
	MCPEnabled        bool     `json:"mcp_enabled"`
	DebugMode         bool     `json:"debug_mode"`
	SkillsDebug       bool     `json:"skills_debug"`
	PermissionMode    string   `json:"permission_mode,omitempty"`
	ApprovalReuse     string   `json:"approval_reuse,omitempty"`
	QueuedInput       int      `json:"queued_input,omitempty"`
	QueuedDraining    bool     `json:"queued_draining,omitempty"`
	AgentTarget       string   `json:"agent_target,omitempty"`
	Surface           bool     `json:"surface_enabled"`
	RowPlan           string   `json:"row_plan,omitempty"`
}

// chatDebugDisplayRoutingInfo 对应面板"Subagent Routing:"与"Team Routing:"
// 两个子区块。
type chatDebugDisplayRoutingInfo struct {
	Subagent *chatDebugDisplayRoutingConfigInfo `json:"subagent,omitempty"`
	Team     *chatDebugDisplayRoutingConfigInfo `json:"team,omitempty"`
}

// chatDebugDisplayRoutingConfigInfo 是单个 routing 配置的结构化摘要。
type chatDebugDisplayRoutingConfigInfo struct {
	Source            string   `json:"source,omitempty"`
	Enabled           bool     `json:"enabled"`
	Compatibility     string   `json:"compatibility_mode,omitempty"`
	DefaultDifficulty string   `json:"default_difficulty,omitempty"`
	InheritParent     bool     `json:"inherit_parent"`
	ValidateModels    bool     `json:"validate_models"`
	ReasoningPolicy   string   `json:"reasoning_policy,omitempty"`
	ProviderOverride  bool     `json:"allow_provider_override"`
	ModelOverride     bool     `json:"allow_model_override"`
	ReasoningOverride bool     `json:"allow_reasoning_override"`
	ExpertLimit       int      `json:"expert_limit,omitempty"`
	AllowedProviders  []string `json:"allowed_providers,omitempty"`
	AllowedModels     []string `json:"allowed_models,omitempty"`
	Levels            []string `json:"levels,omitempty"`
	Roles             []string `json:"roles,omitempty"`
}

// chatDebugDisplayComponentsInfo 对应面板"运行时组件:"区块。
type chatDebugDisplayComponentsInfo struct {
	RuntimeCore      string                                `json:"runtime_core,omitempty"`
	ActorRegistry    bool                                  `json:"actor_registry"`
	SessionHub       bool                                  `json:"session_hub"`
	SessionHubActive int                                   `json:"session_hub_active,omitempty"`
	EventBus         bool                                  `json:"event_bus"`
	EventStore       bool                                  `json:"event_store"`
	Supervision      bool                                  `json:"supervision"`
	TeamStore        bool                                  `json:"team_store"`
	AgentControl     bool                                  `json:"agent_control"`
	ToolSurface      bool                                  `json:"skills_mcp_surface"`
	Background       bool                                  `json:"background"`
	Observe          *chatDebugDisplayObserveComponentInfo `json:"observe,omitempty"`
}

// chatDebugDisplayObserveComponentInfo 是观察平面组件配置摘要。
type chatDebugDisplayObserveComponentInfo struct {
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status,omitempty"`
	Retention string `json:"retention,omitempty"`
	Limits    string `json:"limits,omitempty"`
	Redactor  string `json:"redactor,omitempty"`
	Ingress   string `json:"ingress,omitempty"`
}

// chatDebugDisplayAgentsInfo 对应面板"AgentControl Registry:"、
// "Agent Graph:"与"Mailbox Pending:"三个区块。
type chatDebugDisplayAgentsInfo struct {
	Registry    string                      `json:"registry,omitempty"`
	Consistency string                      `json:"consistency,omitempty"`
	Graph       []chatDebugDisplayAgentInfo `json:"graph,omitempty"`
	Mailbox     string                      `json:"mailbox,omitempty"`
}

// chatDebugDisplayAgentInfo 是 Agent Graph 中单个 agent 的结构化投影。
type chatDebugDisplayAgentInfo struct {
	Path            string `json:"path"`
	Status          string `json:"status"`
	SessionID       string `json:"session_id,omitempty"`
	SessionState    string `json:"session_state,omitempty"`
	Parent          string `json:"parent,omitempty"`
	Depth           int    `json:"depth,omitempty"`
	AgentType       string `json:"agent_type,omitempty"`
	TeamID          string `json:"team_id,omitempty"`
	PendingApproval bool   `json:"pending_approval,omitempty"`
	PendingQuestion bool   `json:"pending_question,omitempty"`
	PendingTool     string `json:"pending_tool,omitempty"`
}

// ============================================================================
// 区块构建
// ============================================================================

// buildChatDebugDisplaySessionInfo 构建顶部 Session Info 区块快照
// （与 /debug display 面板第一屏 ui.SessionInfoDocument 一致）。
func buildChatDebugDisplaySessionInfo(session *ChatSession) *chatDebugDisplaySessionInfo {
	if session == nil {
		return nil
	}
	ctx := snapshotChatRuntimeContext(session)
	info := &chatDebugDisplaySessionInfo{
		SessionID:   chatDebugSessionID(session),
		DebugMode:   ctx.DebugMode,
		Surface:     session.Surface != nil && session.Surface.Enabled(),
		Interaction: chatDebugInteractionSummary(session),
	}
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		info.Transport = descriptor.Transport
		info.CoreName = descriptor.Core.Name
	}
	sessInfo := buildChatSessionInfo(session)
	info.Provider = sessInfo.ProviderName
	info.Protocol = sessInfo.Protocol
	info.Model = sessInfo.ModelName
	info.EndpointURL = sessInfo.EndpointURL
	info.Host = sessInfo.Host
	info.KeyCount = sessInfo.KeyCount
	info.Timeout = sessInfo.Timeout
	info.IsStream = sessInfo.IsStream
	info.SupportsFast = sessInfo.SupportsFast
	info.IsFast = sessInfo.IsFast
	info.ReasoningEnabled = sessInfo.ReasoningEnabled
	info.Profile = session.ProfileName
	if session.ProfileAgent != "" {
		info.Profile += fmt.Sprintf(" (agent=%s)", session.ProfileAgent)
	}
	info.AgentSource = formatChatAgentSourceLine(session)
	info.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	return info
}

// buildChatDebugDisplayFilesInfo 构建"会话文件与目录"区块快照。
func buildChatDebugDisplayFilesInfo(session *ChatSession) *chatDebugDisplayFilesInfo {
	if session == nil {
		return nil
	}
	info := &chatDebugDisplayFilesInfo{
		SessionLabel:     chatDebugValueOrNone(chatDebugSessionLabel(session)),
		SessionStore:     chatDebugValueOrNone(currentRuntimeSessionStoreSummary(session)),
		SessionFile:      chatDebugValueOrNone(currentRuntimeSessionPath(session)),
		ChatLogFile:      chatDebugValueOrNone(currentChatLogFile(session)),
		DebugLogFile:     chatDebugValueOrNone(currentDebugLogFile(session)),
		HTTPArtifactDir:  chatDebugValueOrNone(currentRuntimeHTTPArtifactDir(session)),
		ShellArtifactDir: chatDebugValueOrNone(currentLocalShellArtifactDir(session)),
		ImageArtifactDir: chatDebugValueOrNone(currentGeneratedImageArtifactDir(session)),
		LastHTTPRequest:  chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, true)),
		LastHTTPResponse: chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, false)),
		LastShellOut:     chatDebugValueOrNone(currentLastLocalShellArtifactPath(session)),
	}
	if session.RuntimeSession != nil {
		preview := session.RuntimeSession.BuildPreview()
		info.Title = preview.Title
		info.Summary = preview.Summary
		info.HistoryMessages = preview.MessageCount
	}
	return info
}

// buildChatDebugDisplayRuntimeInfo 构建"运行时调试"区块快照。
func buildChatDebugDisplayRuntimeInfo(session *ChatSession) *chatDebugDisplayRuntimeInfo {
	if session == nil {
		return nil
	}
	ctx := snapshotChatRuntimeContext(session)
	info := &chatDebugDisplayRuntimeInfo{
		OutputFormat:   chatDebugValueOrNone(session.OutputFormat),
		NoInteractive:  session.NoInteractive,
		JSONOutput:     session.JSONOutput,
		JSONEnvelope:   session.JSONEnvelope,
		MCPEnabled:     session.MCPEnabled,
		DebugMode:      ctx.DebugMode,
		SkillsDebug:    session.SkillsDebug,
		PermissionMode: chatDebugValueOrNone(string(ctx.PermissionMode)),
		ApprovalReuse:  chatDebugValueOrNone(formatChatApprovalReuseMode(ctx.ApprovalReuseMode)),
		AgentTarget:    chatDebugValueOrNone(strings.TrimSpace(ctx.SelectedAgentTarget)),
		Surface:        session.Surface != nil && session.Surface.Enabled(),
	}
	if session.Config != nil {
		info.AICLIConfigPath = chatDebugValueOrNone(resolveAbsoluteChatPath(session.Config.ConfigFilePath))
	}
	info.ProfileRoot = chatDebugValueOrNone(resolveAbsoluteChatPath(session.ProfileRoot))
	info.AgentSource = chatDebugValueOrNone(formatChatAgentSourceLine(session))
	info.RuntimeConfigPath = chatDebugValueOrNone(resolveAbsoluteChatPath(session.RuntimeConfigPath))
	info.MCPConfigPath = chatDebugValueOrNone(resolveAbsoluteChatPath(session.MCPConfigPath))
	if len(session.ResolvedSkillDirs) > 0 {
		info.SkillDirs = append([]string(nil), session.ResolvedSkillDirs...)
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		info.QueuedInput = queuedCount
		info.QueuedDraining = draining
	}
	if session.Surface != nil {
		info.RowPlan = session.Surface.RowPlanDebugString()
	}
	return info
}

// buildChatDebugDisplayRoutingInfo 构建 Subagent/Team Routing 区块快照。
func buildChatDebugDisplayRoutingInfo(session *ChatSession) *chatDebugDisplayRoutingInfo {
	if session == nil {
		return nil
	}
	routing := &chatDebugDisplayRoutingInfo{
		Subagent: chatDebugDisplayRoutingConfigSnapshot(
			"subagent", localChatSubagentRoutingConfig(session)),
	}
	teamSource := "subagent_inherited"
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Teams != nil && session.Config.AICLI.Teams.Routing != nil {
		teamSource = "team_independent"
	}
	routing.Team = chatDebugDisplayRoutingConfigSnapshot(
		teamSource, localChatTeamRoutingConfig(session))
	return routing
}

// chatDebugDisplayRoutingConfigSnapshot 构建单个 routing 配置的结构化摘要。
func chatDebugDisplayRoutingConfigSnapshot(source string, routing *config.AICLISubagentRoutingConfig) *chatDebugDisplayRoutingConfigInfo {
	info := &chatDebugDisplayRoutingConfigInfo{
		Source:            source,
		Enabled:           modelrouting.RoutingEnabled(routing),
		Compatibility:     modelrouting.CompatibilityMode(routing),
		DefaultDifficulty: modelrouting.DefaultDifficulty(routing),
		InheritParent:     modelrouting.InheritParentWhenMissing(routing),
		ValidateModels:    modelrouting.ValidateModelCapabilities(routing),
		ReasoningPolicy:   modelrouting.UnsupportedReasoningPolicy(routing),
	}
	if routing == nil {
		return info
	}
	info.ProviderOverride = routing.AllowExplicitProviderOverride
	info.ModelOverride = routing.AllowExplicitModelOverride
	info.ReasoningOverride = routing.AllowExplicitReasoningOverride
	info.ExpertLimit = routing.MaxExpertConcurrency
	if len(routing.AllowedProviderOverrides) > 0 {
		info.AllowedProviders = append([]string(nil), routing.AllowedProviderOverrides...)
	}
	if len(routing.AllowedModelOverrides) > 0 {
		info.AllowedModels = append([]string(nil), routing.AllowedModelOverrides...)
	}
	info.Levels = sortedChatRouteProfileNames(routing.Levels)
	roleNames := make([]string, 0, len(routing.Roles))
	for role := range routing.Roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	info.Roles = roleNames
	return info
}

// buildChatDebugDisplayComponentsInfo 构建"运行时组件"区块快照。
func buildChatDebugDisplayComponentsInfo(session *ChatSession) *chatDebugDisplayComponentsInfo {
	if session == nil {
		return nil
	}
	host := localChatRuntimeHostOf(session)
	info := &chatDebugDisplayComponentsInfo{
		ActorRegistry: host != nil && host.ActorRegistry != nil,
		SessionHub:    host != nil && host.SessionHub != nil,
		EventBus:      host != nil && host.EventBus != nil,
		EventStore:    host != nil && host.EventStore != nil,
		Supervision:   host != nil && host.Supervision != nil,
		TeamStore:     host != nil && host.TeamStore != nil,
		AgentControl:  host != nil && host.AgentControl != nil,
		ToolSurface:   host != nil && host.ToolSurface != nil,
		Background:    host != nil && host.Background != nil,
	}
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		transport := strings.TrimSpace(descriptor.Transport)
		if transport == "" {
			transport = "in-process"
		}
		info.RuntimeCore = fmt.Sprintf("%s v%d transport=%s",
			descriptor.Core.Name, descriptor.Core.ContractVersion, transport)
	} else {
		info.RuntimeCore = "<none>"
	}
	if host != nil && host.SessionHub != nil {
		info.SessionHubActive = len(host.SessionHub.ActiveSessionIDs(4096))
	}
	if observe, ok := chatSessionObserveConfig(session); ok {
		obs := &chatDebugDisplayObserveComponentInfo{
			Enabled: observe.Enabled,
			Status:  "未启用",
		}
		if observe.Enabled {
			obs.Status = "ready"
			obs.Retention = fmt.Sprintf("retention=%d events / %d bytes / %s",
				observe.RetentionEvents, observe.RetentionBytes, observe.RetentionTTL)
			obs.Limits = fmt.Sprintf("event_max=%d bytes snapshot_max=%d bytes query=%d..%d",
				observe.MaxEventBytes, observe.MaxSnapshotBytes, observe.DefaultQueryLimit, observe.MaxQueryLimit)
			obs.Redactor = fmt.Sprintf("profile=%s key_ref=%s",
				observe.RedactionProfile, observe.HMACKeyRef)
			obs.Ingress = fmt.Sprintf("%d events / %d bytes",
				observe.IngressQueueEvents, observe.IngressQueueBytes)
		}
		info.Observe = obs
	}
	return info
}

// buildChatDebugDisplayAgentsInfo 构建 AgentControl Registry / Agent Graph /
// Mailbox Pending 区块快照。
func buildChatDebugDisplayAgentsInfo(session *ChatSession) *chatDebugDisplayAgentsInfo {
	if session == nil {
		return nil
	}
	info := &chatDebugDisplayAgentsInfo{
		Registry:    strings.TrimSpace(chatAgentPanelRegistryLine(session)),
		Consistency: strings.Join(chatAgentControlConsistencyLines(session), " | "),
		Mailbox:     strings.Join(chatDebugMailboxLines(session), " | "),
	}
	agents, err := chatAgentGraphItems(session)
	if err == nil {
		info.Graph = make([]chatDebugDisplayAgentInfo, 0, len(agents))
		for _, agent := range agents {
			info.Graph = append(info.Graph, chatDebugDisplayAgentInfo{
				Path:            firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID),
				Status:          firstNonEmptyChatValue(agent.Status, "unknown"),
				SessionID:       firstNonEmptyChatValue(agent.SessionID, agent.ID),
				SessionState:    agent.SessionState,
				Parent:          agent.ParentSessionID,
				Depth:           agent.Depth,
				AgentType:       agent.AgentType,
				TeamID:          agent.TeamID,
				PendingApproval: agent.PendingApproval,
				PendingQuestion: agent.PendingQuestion,
				PendingTool:     agent.PendingToolName,
			})
		}
	}
	return info
}
