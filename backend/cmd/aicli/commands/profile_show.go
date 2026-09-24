package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// profileShowResult 是 `profile show` 的结构化输出：一个 profile 解析后的
// 最终生效面（工具/skills/mcp/prompt/paths）。
type profileShowResult struct {
	Reference       string `json:"reference"`
	ProfileName     string `json:"profile_name,omitempty"`
	ProfileRoot     string `json:"profile_root"`
	AgentID         string `json:"agent_id"`
	Provider        string `json:"provider,omitempty"`
	DefaultProvider string `json:"default_provider,omitempty"`
	Model           string `json:"model,omitempty"`
	// PermissionMode 是 profile/agent 声明的**默认**权限模式（D16/D17 可发现性）：
	// 只在 CLI 未显式 --permission-mode/--yolo 时生效，会话内可被用户覆盖，
	// profile 永远不降级显式选择。
	PermissionMode string `json:"permission_mode,omitempty"`
	// PermissionModeSource 标注该默认值的来源（agentdef 来源类别 + 定义文件）。
	PermissionModeSource string                   `json:"permission_mode_source,omitempty"`
	RuntimeConfig        string                   `json:"runtime_config,omitempty"`
	ToolPolicy           profileShowToolPolicy    `json:"tool_policy"`
	Skills               profileShowSkills        `json:"skills"`
	MCP                  profileShowMCP           `json:"mcp"`
	Prompts              profileShowPrompts       `json:"prompts"`
	Paths                profilesys.ResolvedPaths `json:"paths"`
	SandboxWarnings      []string                 `json:"sandbox_warnings,omitempty"`
}

type profileShowToolPolicy struct {
	Allowlist []string `json:"allowlist,omitempty"`
	Denylist  []string `json:"denylist,omitempty"`
	ReadOnly  *bool    `json:"read_only,omitempty"`
	Sources   []string `json:"sources,omitempty"`
	// EffectiveAllowCount 是 allowlist 经过 deny 过滤后的实际允许数量；
	// -1 表示未声明 allowlist（全量允许）。
	EffectiveAllowCount int `json:"effective_allow_count"`
	// ExcludedByDeny 列出"在 allowlist 中但被 denylist 排除"的工具名。
	ExcludedByDeny []string `json:"excluded_by_deny,omitempty"`
}

type profileShowSkills struct {
	Dirs         []string `json:"dirs,omitempty"`
	Allowlist    []string `json:"allowlist,omitempty"`
	Denylist     []string `json:"denylist,omitempty"`
	Declared     bool     `json:"declared"`
	ExposureMode string   `json:"exposure_mode,omitempty"`
	ExposureTopK int      `json:"exposure_top_k,omitempty"`
	Discovered   []string `json:"discovered,omitempty"`
	Effective    []string `json:"effective,omitempty"`
}

type profileShowMCPServerStatus struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type profileShowMCP struct {
	ConfigFile     string                       `json:"config_file,omitempty"`
	UseServers     []string                     `json:"use_servers,omitempty"`
	ExcludeServers []string                     `json:"exclude_servers,omitempty"`
	Declared       bool                         `json:"declared"`
	Servers        []profileShowMCPServerStatus `json:"servers,omitempty"`
	ConfigError    string                       `json:"config_error,omitempty"`
}

type profileShowPromptFile struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	Bytes           int    `json:"bytes,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens,omitempty"`
}

type profileShowPrompts struct {
	Mode           string                  `json:"mode,omitempty"`
	Files          []profileShowPromptFile `json:"files,omitempty"`
	ComposedBytes  int                     `json:"composed_bytes,omitempty"`
	ComposedTokens int                     `json:"composed_estimated_tokens,omitempty"`
}

func newProfileShowCommand(getConfig func() *config.Config) *cobra.Command {
	var agentFlag string
	cmd := &cobra.Command{
		Use:   "show <profile>",
		Short: "展示一个 profile 解析后的最终生效面",
		Long: `解析并展示 profile 的最终生效面（与真实会话使用同一套解析逻辑）：

  - tools：allowlist/denylist 合并结果、deny 排除后的实际允许数量
  - skills：技能目录、发现清单、allow/deny 过滤后的生效清单
  - mcp：mcp.yaml 服务器清单与 use/exclude 过滤后的启停状态
  - prompts：prompt 文件与 token 估算（估算单点：internal/profile/estimate.go）
  - paths：profile/agent/workspace 各级路径

<profile> 支持：注册名、default root 下的目录名、profile 目录路径。`,
		Example: `  aicli profile show coding
  aicli profile show .\profiles\review --agent reviewer
  aicli profile show coding --output json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile show", "json", err, nil)
			}
			executeStructuredCommand("profile show", outputOptions, func() (profileShowResult, map[string]interface{}, error) {
				result, err := runProfileShowCommand(getConfig(), args[0], agentFlag)
				return result, nil, err
			}, func(result profileShowResult) interface{} {
				return result
			}, renderProfileShowText)
		},
	}
	cmd.Flags().StringVar(&agentFlag, "agent", "", "指定 profile 内的 agent id（默认 default_agent 或 default）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func runProfileShowCommand(cfg *config.Config, ref, agent string) (profileShowResult, error) {
	state, err := resolveProfileStateForCLI(cfg, ref, agent)
	if err != nil {
		return profileShowResult{}, err
	}
	resolved := state.Resolved

	result := profileShowResult{
		Reference:       state.Reference,
		ProfileName:     resolved.ProfileName,
		ProfileRoot:     resolved.ProfileRoot,
		AgentID:         resolved.AgentID,
		Provider:        resolved.Provider,
		DefaultProvider: resolved.DefaultProvider,
		Model:           resolved.Model,
		RuntimeConfig:   resolved.RuntimeConfig,
		Paths:           resolved.Paths,
		SandboxWarnings: append([]string(nil), state.SandboxWarnings...),
	}
	result.PermissionMode = strings.TrimSpace(string(state.PermissionMode))
	if result.PermissionMode != "" {
		result.PermissionModeSource = profilePermissionModeSourceLabel(state)
	}

	result.ToolPolicy = buildProfileShowToolPolicy(state)
	result.Skills = buildProfileShowSkills(cfg, resolved)
	result.MCP = buildProfileShowMCP(resolved)
	result.Prompts = buildProfileShowPrompts(state, resolved)
	return result, nil
}

// profilePermissionModeSourceLabel 标注默认权限模式的来源（D17 可发现性）：
// agentdef 来源类别 + 定义文件路径，便于判断"这个默认值是谁给的"。
func profilePermissionModeSourceLabel(state *chatProfileState) string {
	if state == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if source := strings.TrimSpace(state.AgentSource); source != "" {
		parts = append(parts, "agentdef:"+source)
	}
	if path := strings.TrimSpace(state.AgentSourcePath); path != "" {
		parts = append(parts, path)
	}
	return strings.Join(parts, " ")
}

// buildProfileShowToolPolicy 用真实的 runtime policy（state.ToolPolicy）计算
// deny 过滤后的实际允许数量，避免出现"第二套匹配规则"。
func buildProfileShowToolPolicy(state *chatProfileState) profileShowToolPolicy {
	resolved := state.Resolved
	policy := profileShowToolPolicy{
		Allowlist:           append([]string(nil), resolved.ToolPolicy.Allowlist...),
		Denylist:            append([]string(nil), resolved.ToolPolicy.Denylist...),
		ReadOnly:            resolved.ToolPolicy.ReadOnly,
		Sources:             append([]string(nil), resolved.ToolPolicy.Sources...),
		EffectiveAllowCount: -1,
	}
	if len(policy.Allowlist) == 0 {
		return policy
	}
	policy.EffectiveAllowCount = 0
	for _, name := range policy.Allowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if state.ToolPolicy != nil && !state.ToolPolicy.AllowsDefinition(name) {
			policy.ExcludedByDeny = append(policy.ExcludedByDeny, name)
			continue
		}
		policy.EffectiveAllowCount++
	}
	return policy
}

func buildProfileShowSkills(cfg *config.Config, resolved *profilesys.ResolvedAgent) profileShowSkills {
	discovered := discoverProfileSkillNames(resolved.SkillDirs)
	skills := profileShowSkills{
		Dirs:       append([]string(nil), resolved.SkillDirs...),
		Allowlist:  append([]string(nil), resolved.Skills.Allowlist...),
		Denylist:   append([]string(nil), resolved.Skills.Denylist...),
		Declared:   !resolved.Skills.Empty(),
		Discovered: discovered,
	}
	if cfg != nil && cfg.SkillsRuntime != nil {
		skills.ExposureMode = strings.TrimSpace(cfg.SkillsRuntime.AICLISkillExposureMode)
		skills.ExposureTopK = cfg.SkillsRuntime.AICLISkillExposureTopK
	}
	for _, name := range discovered {
		if resolved.Skills.AllowsSkill(name) {
			skills.Effective = append(skills.Effective, name)
		}
	}
	return skills
}

func buildProfileShowMCP(resolved *profilesys.ResolvedAgent) profileShowMCP {
	mcp := profileShowMCP{
		ConfigFile:     resolved.MCPConfig,
		UseServers:     append([]string(nil), resolved.MCPSelection.UseServers...),
		ExcludeServers: append([]string(nil), resolved.MCPSelection.ExcludeServers...),
		Declared:       !resolved.MCPSelection.Empty(),
	}
	servers, err := loadProfileMCPServers(resolved.MCPConfig)
	if err != nil {
		mcp.ConfigError = err.Error()
		return mcp
	}
	for _, name := range servers {
		mcp.Servers = append(mcp.Servers, profileShowMCPServerStatus{
			Name:    name,
			Enabled: resolved.MCPSelection.AllowsServer(name),
		})
	}
	return mcp
}

func buildProfileShowPrompts(state *chatProfileState, resolved *profilesys.ResolvedAgent) profileShowPrompts {
	prompts := profileShowPrompts{Mode: resolved.PromptMode}
	appendFile := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		file := profileShowPromptFile{Path: path}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			file.Exists = true
			file.Bytes = int(info.Size())
			file.EstimatedTokens = profilesys.EstimateFileTokens(int(info.Size()))
		}
		prompts.Files = append(prompts.Files, file)
	}
	appendFile(resolved.Prompts.System)
	appendFile(resolved.Prompts.Role)
	appendFile(resolved.Prompts.Tools)
	if text := state.PromptText; strings.TrimSpace(text) != "" {
		prompts.ComposedBytes = len(text)
		prompts.ComposedTokens = profilesys.EstimateTokensFromBytes(len(text))
	}
	return prompts
}

// discoverProfileSkillNames 扫描技能目录：目录本身含 SKILL.md 时以目录名作为
// 技能名，否则以含 SKILL.md 的子目录名为技能名。
func discoverProfileSkillNames(dirs []string) []string {
	names := make([]string, 0, 8)
	seen := make(map[string]struct{})
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !info.IsDir() {
			appendName(filepath.Base(dir))
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		sub := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				sub = append(sub, entry.Name())
			}
		}
		sort.Strings(sub)
		for _, name := range sub {
			if info, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil && !info.IsDir() {
				appendName(name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// loadProfileMCPServers 复用 MCP 官方 loader（无第二套解析方言）。
func loadProfileMCPServers(configPath string) ([]string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, nil
	}
	loaded, err := mcpconfig.NewLoader(configPath).Load()
	if err != nil {
		return nil, fmt.Errorf("load mcp config %s: %w", configPath, err)
	}
	if loaded == nil || len(loaded.MCPServers) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(loaded.MCPServers))
	for name := range loaded.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func renderProfileShowText(result profileShowResult) {
	title := result.ProfileName
	if title == "" {
		title = result.Reference
	}
	fmt.Fprintf(os.Stdout, "profile: %s\n", title)
	fmt.Fprintf(os.Stdout, "  root:  %s\n", result.ProfileRoot)
	fmt.Fprintf(os.Stdout, "  agent: %s\n", result.AgentID)
	if result.Provider != "" {
		line := "  provider: " + result.Provider
		if result.DefaultProvider != "" && result.DefaultProvider != result.Provider {
			line += fmt.Sprintf("（default_provider: %s）", result.DefaultProvider)
		}
		fmt.Fprintln(os.Stdout, line)
	}
	if result.Model != "" {
		fmt.Fprintf(os.Stdout, "  model: %s\n", result.Model)
	}
	if result.PermissionMode != "" {
		line := fmt.Sprintf("  permission_mode: %s（默认值，会话内可被显式选择覆盖）", result.PermissionMode)
		if result.PermissionModeSource != "" {
			line += "；来源：" + result.PermissionModeSource
		}
		fmt.Fprintln(os.Stdout, line)
	}
	if result.RuntimeConfig != "" {
		fmt.Fprintf(os.Stdout, "  runtime: %s\n", result.RuntimeConfig)
	}

	fmt.Fprintln(os.Stdout, "\n[tools]")
	tools := result.ToolPolicy
	if len(tools.Allowlist) == 0 && len(tools.Denylist) == 0 && tools.ReadOnly == nil {
		fmt.Fprintln(os.Stdout, "  未声明工具策略（全量工具）")
	} else {
		if len(tools.Allowlist) > 0 {
			count := tools.EffectiveAllowCount
			if count < 0 {
				count = len(tools.Allowlist)
			}
			fmt.Fprintf(os.Stdout, "  allowlist（%d 项，生效 %d）：%s\n", len(tools.Allowlist), count, strings.Join(tools.Allowlist, ", "))
		} else {
			fmt.Fprintln(os.Stdout, "  allowlist：未声明（全量）")
		}
		if len(tools.Denylist) > 0 {
			fmt.Fprintf(os.Stdout, "  denylist：%s\n", strings.Join(tools.Denylist, ", "))
		}
		if len(tools.ExcludedByDeny) > 0 {
			fmt.Fprintf(os.Stdout, "  被 deny 排除：%s\n", strings.Join(tools.ExcludedByDeny, ", "))
		}
		if tools.ReadOnly != nil {
			fmt.Fprintf(os.Stdout, "  read_only：%t\n", *tools.ReadOnly)
		}
		if len(tools.Sources) > 0 {
			fmt.Fprintf(os.Stdout, "  sources：%s\n", strings.Join(tools.Sources, ", "))
		}
	}

	fmt.Fprintln(os.Stdout, "\n[skills]")
	skills := result.Skills
	if len(skills.Dirs) > 0 {
		fmt.Fprintf(os.Stdout, "  dirs：%s\n", strings.Join(skills.Dirs, ", "))
	}
	if skills.Declared {
		fmt.Fprintf(os.Stdout, "  声明：allow=%v deny=%v\n", skills.Allowlist, skills.Denylist)
	} else {
		fmt.Fprintln(os.Stdout, "  声明：无（全量技能）")
	}
	fmt.Fprintf(os.Stdout, "  发现 %d 项，生效 %d 项：%s\n", len(skills.Discovered), len(skills.Effective), strings.Join(skills.Effective, ", "))
	if skills.ExposureMode != "" || skills.ExposureTopK > 0 {
		fmt.Fprintf(os.Stdout, "  exposure：mode=%s top_k=%d\n", skills.ExposureMode, skills.ExposureTopK)
	}

	fmt.Fprintln(os.Stdout, "\n[mcp]")
	mcp := result.MCP
	if mcp.ConfigFile == "" {
		fmt.Fprintln(os.Stdout, "  config：未发现 mcp.yaml")
	} else {
		fmt.Fprintf(os.Stdout, "  config：%s\n", mcp.ConfigFile)
	}
	if mcp.Declared {
		fmt.Fprintf(os.Stdout, "  声明：use=%v exclude=%v\n", mcp.UseServers, mcp.ExcludeServers)
	}
	if mcp.ConfigError != "" {
		fmt.Fprintf(os.Stdout, "  ! %s\n", mcp.ConfigError)
	}
	if len(mcp.Servers) == 0 {
		fmt.Fprintln(os.Stdout, "  servers：无")
	} else {
		states := make([]string, 0, len(mcp.Servers))
		for _, server := range mcp.Servers {
			state := "excluded"
			if server.Enabled {
				state = "enabled"
			}
			states = append(states, fmt.Sprintf("%s(%s)", server.Name, state))
		}
		fmt.Fprintf(os.Stdout, "  servers：%s\n", strings.Join(states, ", "))
	}

	fmt.Fprintln(os.Stdout, "\n[prompts]")
	prompts := result.Prompts
	if prompts.Mode != "" {
		fmt.Fprintf(os.Stdout, "  mode：%s\n", prompts.Mode)
	}
	if len(prompts.Files) == 0 {
		fmt.Fprintln(os.Stdout, "  files：无（使用宿主 system prompt）")
	}
	for _, file := range prompts.Files {
		if file.Exists {
			fmt.Fprintf(os.Stdout, "  %s（%d B ≈ %d tokens）\n", file.Path, file.Bytes, file.EstimatedTokens)
		} else {
			fmt.Fprintf(os.Stdout, "  %s（不存在）\n", file.Path)
		}
	}
	if prompts.ComposedBytes > 0 {
		fmt.Fprintf(os.Stdout, "  composed：%d B ≈ %d tokens\n", prompts.ComposedBytes, prompts.ComposedTokens)
	}

	fmt.Fprintln(os.Stdout, "\n[paths]")
	for _, line := range profileShowPathLines(result.Paths) {
		fmt.Fprintf(os.Stdout, "  %s\n", line)
	}

	if len(result.SandboxWarnings) > 0 {
		fmt.Fprintln(os.Stdout, "\n[sandbox warnings]")
		for _, warning := range result.SandboxWarnings {
			fmt.Fprintf(os.Stdout, "  ! %s\n", warning)
		}
	}
}

func profileShowPathLines(paths profilesys.ResolvedPaths) []string {
	entries := []struct {
		label string
		value string
	}{
		{"profile_file", paths.ProfileFile},
		{"agent_dir", paths.AgentDir},
		{"agent_config_file", paths.AgentConfigFile},
		{"agent_skills_dir", paths.AgentSkillsDir},
		{"profile_skills_dir", paths.ProfileSkillsDir},
		{"profile_mcp_file", paths.ProfileMCPFile},
		{"workspace_dir", paths.WorkspaceDir},
		{"workspace_config_file", paths.WorkspaceConfigFile},
		{"workspace_skills_dir", paths.WorkspaceSkillsDir},
		{"workspace_mcp_file", paths.WorkspaceMCPFile},
		{"prompts_dir", paths.PromptsDir},
		{"tools_dir", paths.ToolsDir},
		{"tool_policy_file", paths.ToolPolicyFile},
		{"sessions_dir", paths.SessionsDir},
		{"memory_dir", paths.MemoryDir},
		{"memory_file", paths.MemoryFile},
		{"context_dir", paths.ContextDir},
		{"context_notes_file", paths.ContextNotesFile},
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.value) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-22s %s", entry.label+":", entry.value))
	}
	return lines
}
