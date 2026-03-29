package profileinput

// ResolvedPromptFiles contains existing prompt file paths.
// Mirrors profile.ResolvedPromptFiles from ai-gateway.
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
// Mirrors profile.ResolvedAgent from ai-gateway.
type ResolvedAgent struct {
	ProfileName     string              `json:"profile_name"`
	ProfileRoot     string              `json:"profile_root"`
	AgentID         string              `json:"agent_id"`
	DefaultProvider string              `json:"default_provider,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model,omitempty"`
	RuntimeConfig   string              `json:"runtime_config,omitempty"`
	MCPConfig       string              `json:"mcp_config,omitempty"`
	SkillDirs       []string            `json:"skill_dirs,omitempty"`
	Prompts         ResolvedPromptFiles `json:"prompts,omitempty"`
	ToolPolicy      ResolvedToolPolicy  `json:"tool_policy,omitempty"`
	Paths           ResolvedPaths       `json:"paths"`
}
