package commands

import (
	"fmt"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

type chatProfileState struct {
	Reference     string
	Resolved      *profilesys.ResolvedAgent
	PromptText    string
	ContextValues map[string]interface{}
	ToolPolicy    *runtimepolicy.ToolExecutionPolicy
	// SandboxWarnings are explicit application-sandbox downgrade notices.
	SandboxWarnings []string
	// PermissionMode is optional agent/profile default applied only when the
	// CLI did not explicitly set --permission-mode / --yolo.
	PermissionMode runtimepolicy.Mode
	// AgentSourcePath is the definition/config file that won for this binding.
	AgentSourcePath string
	// AgentSource is builtin|user|project|profile (empty when unknown).
	AgentSource string
	// ConfigOverlay 是 runtime.overrides 的会话级配置覆盖视图（D13，Batch 7）。
	// nil 表示该 profile 未声明覆盖，投影时必须保持基线配置不变。
	ConfigOverlay *chatProfileConfigOverlay
}

func (s *chatProfileState) Active() bool {
	return s != nil && s.Resolved != nil
}

func (s *chatProfileState) RuntimeConfigPath() string {
	if !s.Active() {
		return ""
	}
	return strings.TrimSpace(s.Resolved.RuntimeConfig)
}

func (s *chatProfileState) MCPConfigPath() string {
	if !s.Active() {
		return ""
	}
	return strings.TrimSpace(s.Resolved.MCPConfig)
}

func (s *chatProfileState) SkillDirs() []string {
	if !s.Active() {
		return nil
	}
	return append([]string(nil), s.Resolved.SkillDirs...)
}

// applyProfileStateToChatSession 把解析后的 profile 生效面投影到会话字段。它是
// "profile → 会话生效面"的唯一权威函数：启动路径（chat_setup）与会话内热切换
// 路径（applyRuntimeProfileSwitch，D21）共用同一份投影，避免两处逻辑漂移。
// 调用方负责在投影后执行缓存失效（工具面/锚点/token 计数）。
func applyProfileStateToChatSession(session *ChatSession, state *chatProfileState) bool {
	if session == nil || state == nil || !state.Active() {
		return false
	}
	session.ProfileReference = state.Reference
	session.ProfileName = state.Resolved.ProfileName
	session.ProfileAgent = state.Resolved.AgentID
	session.ProfileRoot = state.Resolved.ProfileRoot
	session.AgentSourcePath = strings.TrimSpace(state.AgentSourcePath)
	session.AgentSource = strings.TrimSpace(state.AgentSource)
	session.SystemPromptText = state.PromptText
	session.RuntimeConfigPath = state.RuntimeConfigPath()
	session.MCPConfigPath = state.MCPConfigPath()
	session.ResolvedSkillDirs = state.SkillDirs()
	// Profile 场景化裁剪声明（Batch 1）：随会话携带，由各权威生效点消费
	// （bootstrap skills 过滤 / MCP 启动服务器选择 / 系统提示组合模式）。
	session.ProfileSkillSelection = runtimeprofileinput.ResolvedSkillSelection{
		Allowlist: append([]string(nil), state.Resolved.Skills.Allowlist...),
		Denylist:  append([]string(nil), state.Resolved.Skills.Denylist...),
	}
	session.ProfileMCPSelection = runtimeprofileinput.ResolvedMCPSelection{
		UseServers:     append([]string(nil), state.Resolved.MCPSelection.UseServers...),
		ExcludeServers: append([]string(nil), state.Resolved.MCPSelection.ExcludeServers...),
	}
	session.ProfilePromptMode = state.Resolved.PromptMode
	session.ProfileContext = cloneSkillContextMap(state.ContextValues)
	session.ToolPolicy = state.ToolPolicy
	if session.ToolPolicy != nil {
		session.BaseToolPolicy = session.ToolPolicy.Clone()
	}
	// function registry 的策略必须与 ToolPolicy 同步刷新：否则下一轮选出的
	// 工具仍是旧 profile 的集合（A2 断言的失败模式）。
	if session.FunctionCatalog != nil && session.ToolPolicy != nil {
		session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
	}
	// Profile 配置覆盖（D13）：runtime.overrides 叠加到会话配置视图。失败不阻断
	// 会话——profile 只能触碰白名单键，覆盖未生效等价于该 profile 少了一层配置
	// 调整，其余生效面（提示词/工具策略/MCP/skills）仍然有效——但必须显式告警，
	// 不能静默丢弃（禁止「假开关」）。
	if err := applyProfileConfigOverlay(session, state.ConfigOverlay); err != nil {
		emitProfileConfigOverlayWarning(err)
	}
	return true
}

func resolveChatProfileState(cfg *config.Config, opts *chatCommandOptions) (*chatProfileState, error) {
	if opts == nil {
		return nil, nil
	}

	profileRef := strings.TrimSpace(opts.ProfileFlag)
	if profileRef == "" && cfg != nil && cfg.Profiles != nil {
		profileRef = strings.TrimSpace(cfg.Profiles.DefaultProfile)
	}
	if profileRef == "" {
		if agentName := strings.TrimSpace(opts.AgentFlag); agentName != "" {
			return resolveChatAgentdefState(agentName, "")
		}
		return nil, nil
	}

	registry := profilesys.NewRegistryFromProfilesConfig(nil)
	if cfg != nil {
		registry = profilesys.NewRegistryFromProfilesConfig(cfg.Profiles)
	}
	resolved, err := profilesys.ResolveRef(registry, profileRef, profilesys.ResolveOptions{
		Agent:             strings.TrimSpace(opts.AgentFlag),
		GlobalRuntimePath: resolveGlobalRuntimeConfigPath(cfg),
		GlobalMCPPath:     resolveConfiguredMCPConfigPath(cfg),
		GlobalSkillDirs:   resolveConfiguredSkillDirs(skillRuntimeConfig(cfg), nil),
	})
	if err != nil {
		return nil, err
	}

	inputs, err := runtimeprofileinput.BuildResolvedAgentInputs(runtimeprofileinput.AdaptFromProfile(resolved))
	if err != nil {
		return nil, err
	}

	state := &chatProfileState{
		Reference:       profileRef,
		Resolved:        resolved,
		PromptText:      inputs.PromptText,
		ContextValues:   inputs.ContextValues,
		ToolPolicy:      inputs.ToolPolicy,
		SandboxWarnings: append([]string(nil), inputs.SandboxWarnings...),
		AgentSource:     string(agentdef.SourceProfile),
		ConfigOverlay:   newChatProfileConfigOverlay(resolved),
	}
	if resolved != nil {
		if path := strings.TrimSpace(resolved.Paths.AgentConfigFile); path != "" {
			state.AgentSourcePath = path
		} else if path := strings.TrimSpace(resolved.Paths.AgentDir); path != "" {
			state.AgentSourcePath = path
		}
	}
	// D17：profile agent 声明的 permission_mode 是会话默认权限模式（仅当 CLI 未显式
	// 指定 --permission-mode/--yolo 时生效）。必须走 agentdef 权威解析：profile 分支
	// 只投影 prompt/工具/skills/mcp 时该默认值会被静默丢弃（假开关），且与 --agent
	// 分支（resolveChatAgentdefState）行为不一致。
	if mode := profileAgentPermissionMode(resolved); mode != "" {
		state.PermissionMode = mode
	}
	return state, nil
}

// profileAgentPermissionMode 返回 profile agent 声明的默认权限模式（空表示未声明）。
// 复用 agentdef 解析 + BuildBinding，保证与 --agent 路径同一权威来源，并继承 D16：
// profile 默认 bypass_permissions 时 BuildBinding 报错，此处按"未声明"处理（更保守），
// 违规本身由 `profile validate` / agentdef 解析路径显式报出。
func profileAgentPermissionMode(resolved *profilesys.ResolvedAgent) runtimepolicy.Mode {
	if resolved == nil {
		return ""
	}
	agentName := strings.TrimSpace(resolved.AgentID)
	if agentName == "" {
		return ""
	}
	def, err := agentdef.Resolve(agentName, agentdefDiscoverOptions(
		"",
		strings.TrimSpace(resolved.ProfileRoot),
		mergeActivePluginAgentDirs(nil),
	))
	if err != nil || def == nil {
		return ""
	}
	binding, err := agentdef.BuildBinding(def)
	if err != nil || binding == nil {
		return ""
	}
	return binding.PermissionMode
}

// resolveChatAgentdefState loads a portable agent definition without a profile
// package so `aicli chat --agent explore` works against builtins/project agents.
func resolveChatAgentdefState(agentName, profileRoot string) (*chatProfileState, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return nil, nil
	}
	def, err := agentdef.Resolve(agentName, agentdefDiscoverOptions(
		"",
		strings.TrimSpace(profileRoot),
		mergeActivePluginAgentDirs(nil),
	))
	if err != nil {
		return nil, err
	}
	binding, err := agentdef.BuildBinding(def)
	if err != nil {
		return nil, err
	}
	resolved := agentdef.ToResolvedAgent(binding, profileRoot)
	if resolved == nil {
		return nil, fmt.Errorf("agentdef: failed to project agent %q", agentName)
	}

	toolPolicy, sandboxWarnings, err := runtimeprofileinput.BuildToolExecutionPolicyWithWorkspace(runtimeprofileinput.ResolvedToolPolicy{
		Allowlist: append([]string(nil), binding.ToolAllowlist...),
		Denylist:  append([]string(nil), binding.ToolDenylist...),
		ReadOnly:  binding.ReadOnly,
		Sandbox:   binding.Sandbox,
		Sources:   []string{binding.SourcePath},
	}, "")
	if err != nil {
		return nil, err
	}

	promptText := agentdef.MergePrompt("", binding)
	if strings.TrimSpace(promptText) == "" {
		promptText = strings.TrimSpace(binding.PromptText)
	}
	if strings.TrimSpace(promptText) == "" && strings.TrimSpace(def.Description) != "" {
		promptText = strings.TrimSpace(def.Description)
	}

	state := &chatProfileState{
		Reference:       "agentdef:" + binding.AgentID,
		Resolved:        resolved,
		PromptText:      promptText,
		ToolPolicy:      toolPolicy,
		SandboxWarnings: sandboxWarnings,
		AgentSourcePath: strings.TrimSpace(binding.SourcePath),
		AgentSource:     string(binding.Source),
	}
	if binding.PermissionMode != "" {
		state.PermissionMode = binding.PermissionMode
	}
	return state, nil
}

func applyProfileDefaultsToChatOptions(opts *chatCommandOptions, state *chatProfileState) {
	if opts == nil || state == nil || !state.Active() {
		return
	}
	if !opts.ProviderChanged && strings.TrimSpace(opts.ProviderFlag) == "" {
		opts.ProviderFlag = firstNonEmptyChatValue(state.Resolved.Provider, state.Resolved.DefaultProvider)
	}
	if !opts.ModelChanged && strings.TrimSpace(opts.ModelFlag) == "" {
		opts.ModelFlag = strings.TrimSpace(state.Resolved.Model)
	}
	if !opts.PermissionModeChanged && state.PermissionMode != "" {
		opts.PermissionMode = state.PermissionMode
	}
	if strings.TrimSpace(opts.SessionDirFlag) == "" {
		opts.SessionDirFlag = strings.TrimSpace(state.Resolved.Paths.SessionsDir)
	}
	opts.SessionFeaturesRequested = true
}

// resolveGlobalRuntimeConfigPath resolves the runtime.yaml source for chat
// startup, in the documented layer order:
//
//  1. an explicit, non-convention override (absolute path or custom file name)
//  2. ./.aicli/runtime.yaml — project layer of the current workspace
//  3. ~/.aicli/runtime.yaml — user layer
//
// The repository/development layouts (configs/runtime.yaml,
// backend/configs/runtime.yaml) are never used: backend/configs is a
// development directory, so a dev checkout must not silently drive chat.
// Values copied from older templates are still recognized as convention values,
// so they fall through to the .aicli layers instead of being reported missing.
//
// The returned path is the *effective source* — the highest present layer. The
// layers themselves are merged when loaded (see loadCachedRuntimeConfig): a
// project file overrides only the keys it writes, keeping the user layer's
// remaining settings.
//
// It returns "" when no layer exists, so callers fall back to the built-in
// defaults without warning.
func resolveGlobalRuntimeConfigPath(cfg *config.Config) string {
	configured := ""
	if cfg != nil && cfg.SkillsRuntime != nil && strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) != "" {
		configured = strings.TrimSpace(cfg.SkillsRuntime.ConfigFile)
	}
	resolved := aiclipaths.ResolveRuntimeConfigBootstrapPath(configured)
	if resolved == "" {
		return ""
	}
	if existing := resolveExistingPathValue(resolved, false); existing != "" {
		return existing
	}
	// Nothing on disk. A real override keeps its path so the caller can still
	// surface the misconfiguration; a convention value (or no value at all)
	// means "no runtime config", which is not an error.
	if configured != "" && !aiclipaths.IsRuntimeConfigConventionPath(configured) {
		return resolved
	}
	return ""
}

func resolveConfiguredMCPConfigPath(cfg *config.Config) string {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MCP == nil {
		return ""
	}
	// Same priority as the runtime config and the runtime-server: ./.aicli/mcp.yaml >
	// ~/.aicli/mcp.yaml > explicit override > upward search > configs/mcp.yaml.
	return aiclipaths.ResolveMCPConfigPath(cfg.AICLI.MCP.ConfigFile)
}

func skillRuntimeConfig(cfg *config.Config) *config.SkillsRuntimeConfig {
	if cfg == nil {
		return nil
	}
	return cfg.SkillsRuntime
}

func resolveChatSkillDirs(cfg *config.Config, session *ChatSession, cliSkillDirs []string) []string {
	if session != nil && len(session.ResolvedSkillDirs) > 0 {
		// Profile/session skill dirs already include configured dirs; still append
		// active plugin skill roots so trust→hot-load works mid-session after restart.
		return mergeActivePluginSkillDirs(appendUniqueExistingDirs(session.ResolvedSkillDirs, cliSkillDirs))
	}
	return resolveConfiguredSkillDirs(skillRuntimeConfig(cfg), cliSkillDirs)
}

func appendUniqueExistingDirs(base []string, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	addDir := func(dir string) {
		dir = resolveExistingPathValue(dir, true)
		if dir == "" {
			return
		}
		if _, exists := seen[dir]; exists {
			return
		}
		seen[dir] = struct{}{}
		result = append(result, dir)
	}
	for _, dir := range base {
		addDir(dir)
	}
	for _, dir := range extra {
		addDir(dir)
	}
	return result
}

func firstNonEmptyChatValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
