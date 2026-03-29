package profile

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Resolve loads and resolves a profile/agent topology into a reusable runtime model.
func Resolve(options ResolveOptions) (*ResolvedAgent, error) {
	root, err := normalizeRoot(options.Root)
	if err != nil {
		return nil, err
	}

	rootPaths := ResolveProfilePaths(root)
	spec, err := LoadProfile(root)
	if err != nil {
		return nil, err
	}

	agentID, err := resolveAgentID(options.Agent, spec, rootPaths)
	if err != nil {
		return nil, err
	}
	agentPaths := ResolveAgentPaths(root, agentID)
	if !agentDeclaredOrPresent(agentID, spec, agentPaths) {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	inlineAgent, _ := spec.Agent(agentID)
	agentFileSpec, err := LoadAgent(root, agentID)
	if err != nil {
		return nil, err
	}
	workspaceSpec, err := LoadWorkspace(root, agentID)
	if err != nil {
		return nil, err
	}
	filePolicySpec, err := LoadToolPolicy(root, agentID)
	if err != nil {
		return nil, err
	}

	provider := coalesceString(
		valueOrEmpty(workspaceSpec, func(v *WorkspaceSpec) string { return v.Provider }),
		valueOrEmpty(agentFileSpec, func(v *AgentSpec) string { return v.Provider }),
		inlineAgent.Provider,
		spec.Providers.DefaultProvider,
	)
	model := coalesceString(
		valueOrEmpty(workspaceSpec, func(v *WorkspaceSpec) string { return v.Model }),
		valueOrEmpty(agentFileSpec, func(v *AgentSpec) string { return v.Model }),
		inlineAgent.Model,
	)

	runtimeConfig := firstExistingPath(rootPaths.RuntimeConfigFile, options.GlobalRuntimePath)
	mcpConfig := firstExistingPath(agentPaths.WorkspaceMCPFile, rootPaths.ProfileMCPFile, options.GlobalMCPPath)
	skillDirs := collectExistingDirs(
		agentPaths.WorkspaceSkillsDir,
		agentPaths.SkillsDir,
		rootPaths.ProfileSkillsDir,
	)
	skillDirs = appendUniqueStrings(skillDirs, collectExistingDirs(options.GlobalSkillDirs...)...)

	policyLayers := []ToolPolicySpec{spec.Tools, inlineAgent.Tools}
	if agentFileSpec != nil {
		policyLayers = append(policyLayers, agentFileSpec.Tools)
	}
	if workspaceSpec != nil {
		policyLayers = append(policyLayers, workspaceSpec.Tools)
	}
	if filePolicySpec != nil {
		policyLayers = append(policyLayers, *filePolicySpec)
	}
	toolPolicy := MergeToolPolicies(policyLayers...)
	toolPolicy.Sources = appendToolPolicySources(
		toolPolicy.Sources,
		layerHasPolicy("profile.inline", spec.Tools),
		layerHasPolicy("agent.inline", inlineAgent.Tools),
		layerHasPolicyFromAgentSpec("agent.file", agentFileSpec),
		layerHasPolicyFromWorkspaceSpec("workspace.file", workspaceSpec),
	)
	toolPolicy.Sources = appendUniqueStrings(toolPolicy.Sources, collectExistingFiles(agentPaths.ToolPolicyFile)...)

	prompts := ResolvedPromptFiles{
		System: firstExistingPath(agentPaths.PromptSystemFile),
		Role:   firstExistingPath(agentPaths.PromptRoleFile),
		Tools:  firstExistingPath(agentPaths.PromptToolsFile),
	}

	resolved := &ResolvedAgent{
		ProfileName:     coalesceString(spec.Profile.Name, filepath.Base(root)),
		ProfileRoot:     root,
		AgentID:         agentID,
		DefaultProvider: strings.TrimSpace(spec.Providers.DefaultProvider),
		Provider:        provider,
		Model:           model,
		RuntimeConfig:   runtimeConfig,
		MCPConfig:       mcpConfig,
		SkillDirs:       skillDirs,
		Prompts:         prompts,
		ToolPolicy:      toolPolicy,
		Paths: ResolvedPaths{
			ProfileRoot:         root,
			ProfileFile:         rootPaths.ProfileFile,
			RuntimeConfigFile:   runtimeConfig,
			ProfileMCPFile:      firstExistingPath(rootPaths.ProfileMCPFile),
			ProfileSkillsDir:    firstExistingPath(rootPaths.ProfileSkillsDir),
			AgentDir:            agentPaths.Dir,
			AgentConfigFile:     firstExistingPath(agentPaths.ConfigFile),
			AgentSkillsDir:      firstExistingPath(agentPaths.SkillsDir),
			WorkspaceDir:        firstExistingPath(agentPaths.WorkspaceDir),
			WorkspaceConfigFile: firstExistingPath(agentPaths.WorkspaceConfigFile),
			WorkspaceSkillsDir:  firstExistingPath(agentPaths.WorkspaceSkillsDir),
			WorkspaceMCPFile:    firstExistingPath(agentPaths.WorkspaceMCPFile),
			PromptsDir:          firstExistingPath(agentPaths.PromptsDir),
			PromptSystemFile:    prompts.System,
			PromptRoleFile:      prompts.Role,
			PromptToolsFile:     prompts.Tools,
			ToolsDir:            firstExistingPath(agentPaths.ToolsDir),
			ToolPolicyFile:      firstExistingPath(agentPaths.ToolPolicyFile),
			SessionsDir:         agentPaths.SessionsDir,
			MemoryDir:           firstExistingPath(agentPaths.MemoryDir),
			MemoryFile:          firstExistingPath(agentPaths.MemoryFile),
			ContextDir:          firstExistingPath(agentPaths.ContextDir),
			ContextNotesFile:    firstExistingPath(agentPaths.ContextNotesFile),
		},
	}
	return resolved, nil
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if fileExists(trimmed) || dirExists(trimmed) {
			return trimmed
		}
	}
	return ""
}

func collectExistingDirs(paths ...string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" || !dirExists(trimmed) {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func collectExistingFiles(paths ...string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" || !fileExists(trimmed) {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func valueOrEmpty[T any](value *T, getter func(*T) string) string {
	if value == nil {
		return ""
	}
	return getter(value)
}

func appendToolPolicySources(existing []string, values ...string) []string {
	return appendUniqueStrings(existing, values...)
}

func layerHasPolicy(source string, policy ToolPolicySpec) string {
	if len(policy.Allowlist) > 0 || len(policy.Denylist) > 0 || policy.ReadOnly != nil || len(policy.Sandbox) > 0 {
		return source
	}
	return ""
}

func layerHasPolicyFromAgentSpec(source string, spec *AgentSpec) string {
	if spec == nil {
		return ""
	}
	return layerHasPolicy(source, spec.Tools)
}

func layerHasPolicyFromWorkspaceSpec(source string, spec *WorkspaceSpec) string {
	if spec == nil {
		return ""
	}
	return layerHasPolicy(source, spec.Tools)
}
