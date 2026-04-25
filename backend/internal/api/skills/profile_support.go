package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/embedding"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// ProfileSupportConfig configures system-level profile resolution for API requests.
type ProfileSupportConfig struct {
	Registry          *profilesys.Registry
	DefaultProfile    string
	GlobalRuntimePath string
	GlobalMCPPath     string
	GlobalSkillDirs   []string
	MCPAutoConnect    bool
}

type profileRuntimeState struct {
	Reference     string
	Resolved      *profilesys.ResolvedAgent
	PromptText    string
	PromptLayers  *runtimeprompt.Layers
	ContextValues map[string]interface{}
	ToolPolicy    *runtimepolicy.ToolExecutionPolicy
	RuntimeConfig *runtimecfg.RuntimeConfig
	RuntimePath   string
	Registry      *skill.Registry
	Loader        *skill.Loader
	Embedding     *skill.SemanticEmbeddingRouter
	MCPAdapter    skill.MCPManager
	MCPManager    mcpmanager.Manager
}

func (h *Handler) SetProfileSupport(cfg ProfileSupportConfig) {
	if h == nil {
		return
	}
	h.profileRegistry = cfg.Registry
	h.profileDefaultRef = strings.TrimSpace(cfg.DefaultProfile)
	h.profileGlobalRuntimePath = strings.TrimSpace(cfg.GlobalRuntimePath)
	h.profileGlobalMCPPath = strings.TrimSpace(cfg.GlobalMCPPath)
	h.profileGlobalSkillDirs = append([]string(nil), cfg.GlobalSkillDirs...)
	h.profileMCPAutoConnect = cfg.MCPAutoConnect
}

func (h *Handler) resolveProfileRuntimeState(ctx context.Context, profileRef, agentID string, scope UsageScope, workspacePath string) (*profileRuntimeState, func(), error) {
	ref, err := h.resolveProfileReference(profileRef, agentID)
	if err != nil || ref == "" {
		return nil, nil, err
	}
	registry := h.profileRegistry
	if registry == nil {
		registry = profilesys.NewRegistry("")
	}
	ref = h.applyProfileFallback(registry, ref, workspacePath)

	resolved, err := profilesys.ResolveRef(registry, ref, profilesys.ResolveOptions{
		Agent:             strings.TrimSpace(agentID),
		GlobalRuntimePath: strings.TrimSpace(h.profileGlobalRuntimePath),
		GlobalMCPPath:     strings.TrimSpace(h.profileGlobalMCPPath),
		GlobalSkillDirs:   append([]string(nil), h.profileGlobalSkillDirs...),
	})
	if err != nil {
		return nil, nil, err
	}

	inputs, err := runtimeprofileinput.BuildResolvedAgentInputs(toProfileInputResolvedAgent(resolved))
	if err != nil {
		return nil, nil, err
	}

	runtimeCfg, runtimePath, err := h.resolveProfileRuntimeConfig(scope, resolved)
	if err != nil {
		return nil, nil, err
	}

	mcpAdapter, mcpManager, err := h.resolveProfileMCPAdapter(ctx, resolved, runtimeCfg)
	if err != nil {
		return nil, nil, err
	}

	registryInstance := skill.NewRegistry(mcpAdapter)
	loader := skill.NewLoader(mcpAdapter)
	if len(resolved.SkillDirs) > 0 {
		loader.SetSkillDirs(resolved.SkillDirs)
		if err := loader.DiscoverAllWithRegistry(resolved.SkillDirs, registryInstance); err != nil {
			if mcpManager != nil {
				_ = mcpManager.Stop()
			}
			return nil, nil, err
		}
	}

	embeddingRouter, err := buildProfileEmbeddingRouter(runtimeCfg, registryInstance)
	if err != nil {
		if mcpManager != nil {
			_ = mcpManager.Stop()
		}
		return nil, nil, err
	}

	state := &profileRuntimeState{
		Reference:     ref,
		Resolved:      resolved,
		PromptText:    inputs.PromptText,
		PromptLayers:  inputs.PromptLayers,
		ContextValues: cloneProfileContextValues(inputs.ContextValues),
		ToolPolicy:    inputs.ToolPolicy,
		RuntimeConfig: runtimeCfg,
		RuntimePath:   runtimePath,
		Registry:      registryInstance,
		Loader:        loader,
		Embedding:     embeddingRouter,
		MCPAdapter:    mcpAdapter,
		MCPManager:    mcpManager,
	}

	cleanup := func() {
		if mcpManager != nil {
			_ = mcpManager.Stop()
		}
	}
	return state, cleanup, nil
}

func (h *Handler) resolveProfileSessionState(profileRef, agentID string, workspacePath string) (*profileRuntimeState, error) {
	ref, err := h.resolveProfileReference(profileRef, agentID)
	if err != nil || ref == "" {
		return nil, err
	}
	registry := h.profileRegistry
	if registry == nil {
		registry = profilesys.NewRegistry("")
	}
	ref = h.applyProfileFallback(registry, ref, workspacePath)

	resolved, err := profilesys.ResolveRef(registry, ref, profilesys.ResolveOptions{
		Agent:             strings.TrimSpace(agentID),
		GlobalRuntimePath: strings.TrimSpace(h.profileGlobalRuntimePath),
		GlobalMCPPath:     strings.TrimSpace(h.profileGlobalMCPPath),
		GlobalSkillDirs:   append([]string(nil), h.profileGlobalSkillDirs...),
	})
	if err != nil {
		return nil, err
	}

	inputs, err := runtimeprofileinput.BuildResolvedAgentInputs(toProfileInputResolvedAgent(resolved))
	if err != nil {
		return nil, err
	}

	runtimeCfg, runtimePath, err := h.resolveProfileRuntimeConfig(UsageScope{}, resolved)
	if err != nil {
		return nil, err
	}

	return &profileRuntimeState{
		Reference:     ref,
		Resolved:      resolved,
		PromptText:    inputs.PromptText,
		PromptLayers:  inputs.PromptLayers,
		ContextValues: cloneProfileContextValues(inputs.ContextValues),
		ToolPolicy:    inputs.ToolPolicy,
		RuntimeConfig: runtimeCfg,
		RuntimePath:   runtimePath,
	}, nil
}

func (h *Handler) resolveProfileMetadata(profileRef, agentID string) (*profilesys.ResolvedAgent, string, error) {
	ref, err := h.resolveProfileReference(profileRef, agentID)
	if err != nil || ref == "" {
		return nil, "", err
	}
	registry := h.profileRegistry
	if registry == nil {
		registry = profilesys.NewRegistry("")
	}
	ref = h.applyProfileFallback(registry, ref, "")
	resolved, err := profilesys.ResolveRef(registry, ref, profilesys.ResolveOptions{
		Agent:             strings.TrimSpace(agentID),
		GlobalRuntimePath: strings.TrimSpace(h.profileGlobalRuntimePath),
		GlobalMCPPath:     strings.TrimSpace(h.profileGlobalMCPPath),
		GlobalSkillDirs:   append([]string(nil), h.profileGlobalSkillDirs...),
	})
	if err != nil {
		return nil, ref, err
	}
	return resolved, ref, nil
}

func (h *Handler) resolveProfileReference(profileRef, agentID string) (string, error) {
	ref := strings.TrimSpace(profileRef)
	if ref == "" && strings.TrimSpace(h.profileDefaultRef) != "" {
		ref = strings.TrimSpace(h.profileDefaultRef)
	}
	if ref == "" {
		if strings.TrimSpace(agentID) != "" {
			return "", fmt.Errorf("agent requires profile or default_profile")
		}
		return "", nil
	}
	return ref, nil
}

func (h *Handler) applyProfileFallback(registry *profilesys.Registry, ref string, workspacePath string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || looksLikePath(ref) {
		return ref
	}
	if fallback := fallbackProfileRoot(ref, workspacePath); fallback != "" {
		return fallback
	}
	if registry != nil {
		if root, err := registry.Resolve(ref); err == nil {
			if profileRootExists(root) {
				return ref
			}
		}
	}
	return ref
}

func fallbackProfileRoot(name string, workspacePath string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath != "" {
		candidate := filepath.Join(workspacePath, ".gagent", "profiles", name)
		if profileRootExists(candidate) {
			return candidate
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".gagent", "profiles", name)
		if profileRootExists(candidate) {
			return candidate
		}
	}
	return ""
}

func toProfileInputResolvedAgent(resolved *profilesys.ResolvedAgent) *runtimeprofileinput.ResolvedAgent {
	if resolved == nil {
		return nil
	}

	return &runtimeprofileinput.ResolvedAgent{
		ProfileName:     resolved.ProfileName,
		ProfileRoot:     resolved.ProfileRoot,
		AgentID:         resolved.AgentID,
		DefaultProvider: resolved.DefaultProvider,
		Provider:        resolved.Provider,
		Model:           resolved.Model,
		RuntimeConfig:   resolved.RuntimeConfig,
		MCPConfig:       resolved.MCPConfig,
		SkillDirs:       append([]string(nil), resolved.SkillDirs...),
		Prompts: runtimeprofileinput.ResolvedPromptFiles{
			System: resolved.Prompts.System,
			Role:   resolved.Prompts.Role,
			Tools:  resolved.Prompts.Tools,
		},
		ToolPolicy: runtimeprofileinput.ResolvedToolPolicy{
			Allowlist: append([]string(nil), resolved.ToolPolicy.Allowlist...),
			Denylist:  append([]string(nil), resolved.ToolPolicy.Denylist...),
			ReadOnly:  resolved.ToolPolicy.ReadOnly,
			Sandbox:   cloneProfileContextValues(resolved.ToolPolicy.Sandbox),
			Sources:   append([]string(nil), resolved.ToolPolicy.Sources...),
		},
		Paths: runtimeprofileinput.ResolvedPaths{
			ProfileRoot:         resolved.Paths.ProfileRoot,
			ProfileFile:         resolved.Paths.ProfileFile,
			RuntimeConfigFile:   resolved.Paths.RuntimeConfigFile,
			ProfileMCPFile:      resolved.Paths.ProfileMCPFile,
			ProfileSkillsDir:    resolved.Paths.ProfileSkillsDir,
			AgentDir:            resolved.Paths.AgentDir,
			AgentConfigFile:     resolved.Paths.AgentConfigFile,
			AgentSkillsDir:      resolved.Paths.AgentSkillsDir,
			WorkspaceDir:        resolved.Paths.WorkspaceDir,
			WorkspaceConfigFile: resolved.Paths.WorkspaceConfigFile,
			WorkspaceSkillsDir:  resolved.Paths.WorkspaceSkillsDir,
			WorkspaceMCPFile:    resolved.Paths.WorkspaceMCPFile,
			PromptsDir:          resolved.Paths.PromptsDir,
			PromptSystemFile:    resolved.Paths.PromptSystemFile,
			PromptRoleFile:      resolved.Paths.PromptRoleFile,
			PromptToolsFile:     resolved.Paths.PromptToolsFile,
			ToolsDir:            resolved.Paths.ToolsDir,
			ToolPolicyFile:      resolved.Paths.ToolPolicyFile,
			SessionsDir:         resolved.Paths.SessionsDir,
			MemoryDir:           resolved.Paths.MemoryDir,
			MemoryFile:          resolved.Paths.MemoryFile,
			ContextDir:          resolved.Paths.ContextDir,
			ContextNotesFile:    resolved.Paths.ContextNotesFile,
		},
	}
}

func profileRootExists(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "profile.yaml"))
	return err == nil && !info.IsDir()
}

func looksLikePath(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.ContainsRune(ref, filepath.Separator) {
		return true
	}
	if filepath.VolumeName(ref) != "" {
		return true
	}
	return false
}

func (h *Handler) resolveProfileRuntimeConfig(scope UsageScope, resolved *profilesys.ResolvedAgent) (*runtimecfg.RuntimeConfig, string, error) {
	if resolved == nil {
		return h.resolveRuntimeConfig(scope), h.runtimeConfigFile, nil
	}
	runtimePath := strings.TrimSpace(resolved.RuntimeConfig)
	if runtimePath == "" {
		return h.resolveRuntimeConfig(scope), h.runtimeConfigFile, nil
	}
	if samePath(runtimePath, h.runtimeConfigFile) {
		return h.resolveRuntimeConfig(scope), h.runtimeConfigFile, nil
	}

	manager := runtimecfg.NewRuntimeManager(runtimePath)
	if err := manager.Load(); err != nil {
		return nil, "", err
	}
	return manager.Get(), manager.GetFilePath(), nil
}

func samePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return strings.EqualFold(left, right)
}

func (h *Handler) resolveProfileMCPAdapter(ctx context.Context, resolved *profilesys.ResolvedAgent, runtimeCfg *runtimecfg.RuntimeConfig) (skill.MCPManager, mcpmanager.Manager, error) {
	if resolved == nil {
		if h.mcpManager == nil {
			return runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimeCfg)), nil, nil
		}
		return h.mcpManager, nil, nil
	}
	configPath := strings.TrimSpace(resolved.MCPConfig)
	if configPath == "" {
		if h.mcpManager == nil {
			return runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimeCfg)), nil, nil
		}
		return h.mcpManager, nil, nil
	}
	if samePath(configPath, h.profileGlobalMCPPath) && h.mcpManager != nil {
		return h.mcpManager, nil, nil
	}
	if !h.profileMCPAutoConnect {
		return runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimeCfg)), nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager := mcpmanager.NewManager()
	if err := manager.LoadConfig(configPath); err != nil {
		return nil, nil, err
	}
	if err := manager.Start(ctx); err != nil {
		return nil, nil, err
	}
	return runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(manager, runtimeCfg)), manager, nil
}

func buildProfileEmbeddingRouter(config *runtimecfg.RuntimeConfig, registry *skill.Registry) (*skill.SemanticEmbeddingRouter, error) {
	if config == nil || registry == nil {
		return nil, nil
	}
	if !config.Router.EnableEmbedding || !config.Embedding.Enabled {
		return nil, nil
	}
	vectorIndex, err := embedding.NewVectorIndex(
		embedding.NewLocalEmbeddingGenerator(config.Embedding.VectorDim),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding vector index: %w", err)
	}
	router, err := skill.NewSemanticEmbeddingRouter(vectorIndex, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding router: %w", err)
	}
	if threshold := resolveEmbeddingThreshold(config); threshold > 0 {
		router.SetThreshold(threshold)
	}
	if topK := resolveEmbeddingTopK(config); topK > 0 {
		router.SetTopK(topK)
	}
	if err := router.IndexSkills(); err != nil {
		return nil, fmt.Errorf("failed to index skills for embedding router: %w", err)
	}
	return router, nil
}

func resolveEmbeddingThreshold(config *runtimecfg.RuntimeConfig) float32 {
	if config == nil {
		return 0
	}
	if config.Embedding.Threshold > 0 {
		return config.Embedding.Threshold
	}
	return config.Router.Threshold
}

func resolveEmbeddingTopK(config *runtimecfg.RuntimeConfig) int {
	if config == nil {
		return 0
	}
	if config.Embedding.TopK > 0 {
		return config.Embedding.TopK
	}
	return config.Router.TopK
}

func cloneProfileContextValues(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneProfileContextValue(value)
	}
	return cloned
}

func cloneProfileContextValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneProfileContextValues(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneProfileContextValue(item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}
