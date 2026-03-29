package profileinput

import profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"

// AdaptFromProfile converts a *profile.ResolvedAgent to *profileinput.ResolvedAgent.
func AdaptFromProfile(p *profilesys.ResolvedAgent) *ResolvedAgent {
	if p == nil {
		return nil
	}
	return &ResolvedAgent{
		ProfileName:     p.ProfileName,
		ProfileRoot:     p.ProfileRoot,
		AgentID:         p.AgentID,
		DefaultProvider: p.DefaultProvider,
		Provider:        p.Provider,
		Model:           p.Model,
		RuntimeConfig:   p.RuntimeConfig,
		MCPConfig:       p.MCPConfig,
		SkillDirs:       append([]string(nil), p.SkillDirs...),
		Prompts: ResolvedPromptFiles{
			System: p.Prompts.System,
			Role:   p.Prompts.Role,
			Tools:  p.Prompts.Tools,
		},
		ToolPolicy: ResolvedToolPolicy{
			Allowlist: append([]string(nil), p.ToolPolicy.Allowlist...),
			Denylist:  append([]string(nil), p.ToolPolicy.Denylist...),
			ReadOnly:  p.ToolPolicy.ReadOnly,
			Sandbox:   p.ToolPolicy.Sandbox,
			Sources:   append([]string(nil), p.ToolPolicy.Sources...),
		},
		Paths: ResolvedPaths{
			ProfileRoot:         p.Paths.ProfileRoot,
			ProfileFile:         p.Paths.ProfileFile,
			RuntimeConfigFile:   p.Paths.RuntimeConfigFile,
			ProfileMCPFile:      p.Paths.ProfileMCPFile,
			ProfileSkillsDir:    p.Paths.ProfileSkillsDir,
			AgentDir:            p.Paths.AgentDir,
			AgentConfigFile:     p.Paths.AgentConfigFile,
			AgentSkillsDir:      p.Paths.AgentSkillsDir,
			WorkspaceDir:        p.Paths.WorkspaceDir,
			WorkspaceConfigFile: p.Paths.WorkspaceConfigFile,
			WorkspaceSkillsDir:  p.Paths.WorkspaceSkillsDir,
			WorkspaceMCPFile:    p.Paths.WorkspaceMCPFile,
			PromptsDir:          p.Paths.PromptsDir,
			PromptSystemFile:    p.Paths.PromptSystemFile,
			PromptRoleFile:      p.Paths.PromptRoleFile,
			PromptToolsFile:     p.Paths.PromptToolsFile,
			ToolsDir:            p.Paths.ToolsDir,
			ToolPolicyFile:      p.Paths.ToolPolicyFile,
			SessionsDir:         p.Paths.SessionsDir,
			MemoryDir:           p.Paths.MemoryDir,
			MemoryFile:          p.Paths.MemoryFile,
			ContextDir:          p.Paths.ContextDir,
			ContextNotesFile:    p.Paths.ContextNotesFile,
		},
	}
}
