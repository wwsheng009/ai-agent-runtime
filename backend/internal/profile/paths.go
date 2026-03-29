package profile

import "path/filepath"

// Paths describes conventional profile-level paths.
type Paths struct {
	Root              string `json:"root"`
	ProfileFile       string `json:"profile_file"`
	RuntimeConfigFile string `json:"runtime_config_file"`
	ProfileMCPFile    string `json:"profile_mcp_file"`
	ProfileSkillsDir  string `json:"profile_skills_dir"`
	AgentsDir         string `json:"agents_dir"`
	SharedDir         string `json:"shared_dir"`
}

// AgentPaths describes conventional agent-level paths.
type AgentPaths struct {
	AgentID             string `json:"agent_id"`
	Root                string `json:"root"`
	Dir                 string `json:"dir"`
	ConfigFile          string `json:"config_file"`
	SkillsDir           string `json:"skills_dir"`
	WorkspaceDir        string `json:"workspace_dir"`
	WorkspaceConfigFile string `json:"workspace_config_file"`
	WorkspaceSkillsDir  string `json:"workspace_skills_dir"`
	WorkspaceMCPFile    string `json:"workspace_mcp_file"`
	PromptsDir          string `json:"prompts_dir"`
	PromptSystemFile    string `json:"prompt_system_file"`
	PromptRoleFile      string `json:"prompt_role_file"`
	PromptToolsFile     string `json:"prompt_tools_file"`
	ToolsDir            string `json:"tools_dir"`
	ToolPolicyFile      string `json:"tool_policy_file"`
	SessionsDir         string `json:"sessions_dir"`
	MemoryDir           string `json:"memory_dir"`
	MemoryFile          string `json:"memory_file"`
	ContextDir          string `json:"context_dir"`
	ContextNotesFile    string `json:"context_notes_file"`
}

// ResolveProfilePaths returns conventional paths for a profile root.
func ResolveProfilePaths(root string) Paths {
	return Paths{
		Root:              root,
		ProfileFile:       filepath.Join(root, "profile.yaml"),
		RuntimeConfigFile: filepath.Join(root, "runtime.yaml"),
		ProfileMCPFile:    filepath.Join(root, "mcp.yaml"),
		ProfileSkillsDir:  filepath.Join(root, "skills"),
		AgentsDir:         filepath.Join(root, "agents"),
		SharedDir:         filepath.Join(root, "shared"),
	}
}

// ResolveAgentPaths returns conventional paths for an agent under a profile root.
func ResolveAgentPaths(root, agent string) AgentPaths {
	agentDir := filepath.Join(root, "agents", agent)
	workspaceDir := filepath.Join(agentDir, "workspace")
	promptsDir := filepath.Join(agentDir, "prompts")
	toolsDir := filepath.Join(agentDir, "tools")
	return AgentPaths{
		AgentID:             agent,
		Root:                root,
		Dir:                 agentDir,
		ConfigFile:          filepath.Join(agentDir, "agent.yaml"),
		SkillsDir:           filepath.Join(agentDir, "skills"),
		WorkspaceDir:        workspaceDir,
		WorkspaceConfigFile: filepath.Join(workspaceDir, "workspace.yaml"),
		WorkspaceSkillsDir:  filepath.Join(workspaceDir, "skills"),
		WorkspaceMCPFile:    filepath.Join(workspaceDir, "mcp.yaml"),
		PromptsDir:          promptsDir,
		PromptSystemFile:    filepath.Join(promptsDir, "system.md"),
		PromptRoleFile:      filepath.Join(promptsDir, "role.md"),
		PromptToolsFile:     filepath.Join(promptsDir, "tools.md"),
		ToolsDir:            toolsDir,
		ToolPolicyFile:      filepath.Join(toolsDir, "policy.yaml"),
		SessionsDir:         filepath.Join(agentDir, "sessions"),
		MemoryDir:           filepath.Join(agentDir, "memory"),
		MemoryFile:          filepath.Join(agentDir, "memory", "memory.json"),
		ContextDir:          filepath.Join(agentDir, "context"),
		ContextNotesFile:    filepath.Join(agentDir, "context", "notes.md"),
	}
}
