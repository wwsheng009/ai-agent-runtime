package profile

// ResolveOptions configures profile resolution.
type ResolveOptions struct {
	Root              string
	Agent             string
	GlobalRuntimePath string
	GlobalMCPPath     string
	GlobalSkillDirs   []string
}

// ResolvedPromptFiles contains existing prompt file paths.
type ResolvedPromptFiles struct {
	System string `json:"system,omitempty"`
	Role   string `json:"role,omitempty"`
	Tools  string `json:"tools,omitempty"`
}

// ResolvedToolPolicy contains merged tool policy data.
type ResolvedToolPolicy struct {
	Allowlist []string               `json:"allowlist,omitempty"`
	Denylist  []string               `json:"denylist,omitempty"`
	ReadOnly  *bool                  `json:"read_only,omitempty"`
	Sandbox   map[string]interface{} `json:"sandbox,omitempty"`
	Sources   []string               `json:"sources,omitempty"`
}

// ResolvedSkillSelection contains merged skill allow/deny declarations.
// Empty means "no profile declaration" — callers must skip filtering so the
// pre-profile behavior stays byte-for-byte identical (NFR-1).
type ResolvedSkillSelection struct {
	Allowlist []string `json:"allowlist,omitempty"`
	Denylist  []string `json:"denylist,omitempty"`
}

// ResolvedMCPSelection contains merged MCP server use/exclude declarations.
// Empty means "no profile declaration" (all servers keep their mcp.yaml state).
type ResolvedMCPSelection struct {
	UseServers     []string `json:"use_servers,omitempty"`
	ExcludeServers []string `json:"exclude_servers,omitempty"`
}

// ResolvedPaths contains all selected paths for a resolved agent.
type ResolvedPaths struct {
	ProfileRoot         string `json:"profile_root"`
	ProfileFile         string `json:"profile_file"`
	RuntimeConfigFile   string `json:"runtime_config_file,omitempty"`
	ProfileMCPFile      string `json:"profile_mcp_file,omitempty"`
	ProfileSkillsDir    string `json:"profile_skills_dir,omitempty"`
	AgentDir            string `json:"agent_dir"`
	AgentConfigFile     string `json:"agent_config_file,omitempty"`
	AgentSkillsDir      string `json:"agent_skills_dir,omitempty"`
	WorkspaceDir        string `json:"workspace_dir,omitempty"`
	WorkspaceConfigFile string `json:"workspace_config_file,omitempty"`
	WorkspaceSkillsDir  string `json:"workspace_skills_dir,omitempty"`
	WorkspaceMCPFile    string `json:"workspace_mcp_file,omitempty"`
	PromptsDir          string `json:"prompts_dir,omitempty"`
	PromptSystemFile    string `json:"prompt_system_file,omitempty"`
	PromptRoleFile      string `json:"prompt_role_file,omitempty"`
	PromptToolsFile     string `json:"prompt_tools_file,omitempty"`
	ToolsDir            string `json:"tools_dir,omitempty"`
	ToolPolicyFile      string `json:"tool_policy_file,omitempty"`
	SessionsDir         string `json:"sessions_dir"`
	MemoryDir           string `json:"memory_dir,omitempty"`
	MemoryFile          string `json:"memory_file,omitempty"`
	ContextDir          string `json:"context_dir,omitempty"`
	ContextNotesFile    string `json:"context_notes_file,omitempty"`
}

// ResolvedAgent is the system-level output of profile resolution.
type ResolvedAgent struct {
	ProfileName     string               `json:"profile_name"`
	ProfileRoot     string               `json:"profile_root"`
	AgentID         string               `json:"agent_id"`
	DefaultProvider string               `json:"default_provider,omitempty"`
	Provider        string               `json:"provider,omitempty"`
	Model           string               `json:"model,omitempty"`
	RuntimeConfig   string               `json:"runtime_config,omitempty"`
	MCPConfig       string               `json:"mcp_config,omitempty"`
	MCPSelection    ResolvedMCPSelection `json:"mcp_selection,omitempty"`
	// Overrides is the validated sparse config overlay declared by
	// `runtime.overrides` (Batch 7 / D12 mode B). Nil means "no overlay":
	// callers must skip the merge entirely so behavior stays byte-for-byte
	// identical to the pre-profile baseline (NFR-1).
	Overrides  map[string]interface{} `json:"overrides,omitempty"`
	SkillDirs  []string               `json:"skill_dirs,omitempty"`
	Skills     ResolvedSkillSelection `json:"skills,omitempty"`
	Prompts    ResolvedPromptFiles    `json:"prompts,omitempty"`
	PromptMode string                 `json:"prompt_mode,omitempty"`
	// PromptSuppressed（D29 / Batch 14）：项目级 profile 在**未信任工作区**被
	// 扣留 prompts 时置位。调用方（TUI / Switch Report / 前端）据此显示"部分内容
	// 未应用"警告，而不是静默少一层生效面（禁止假开关）。
	PromptSuppressed bool `json:"prompt_suppressed,omitempty"`
	// PromptSuppressionReason 是给用户看的一句话原因（未扣留时为空）。
	PromptSuppressionReason string             `json:"prompt_suppression_reason,omitempty"`
	ToolPolicy              ResolvedToolPolicy `json:"tool_policy,omitempty"`
	Paths                   ResolvedPaths      `json:"paths"`
}
