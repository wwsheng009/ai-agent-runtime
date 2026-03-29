package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

type chatProfileState struct {
	Reference     string
	Resolved      *profilesys.ResolvedAgent
	PromptText    string
	ContextValues map[string]interface{}
	ToolPolicy    *runtimepolicy.ToolExecutionPolicy
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

func resolveChatProfileState(cfg *config.Config, opts *chatCommandOptions) (*chatProfileState, error) {
	if opts == nil {
		return nil, nil
	}

	profileRef := strings.TrimSpace(opts.ProfileFlag)
	if profileRef == "" && cfg != nil && cfg.Profiles != nil {
		profileRef = strings.TrimSpace(cfg.Profiles.DefaultProfile)
	}
	if profileRef == "" {
		if strings.TrimSpace(opts.AgentFlag) != "" {
			return nil, fmt.Errorf("--agent requires --profile or profiles.default_profile")
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

	return &chatProfileState{
		Reference:     profileRef,
		Resolved:      resolved,
		PromptText:    inputs.PromptText,
		ContextValues: inputs.ContextValues,
		ToolPolicy:    inputs.ToolPolicy,
	}, nil
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
	if strings.TrimSpace(opts.SessionDirFlag) == "" {
		opts.SessionDirFlag = strings.TrimSpace(state.Resolved.Paths.SessionsDir)
	}
	opts.SessionFeaturesRequested = true
}

func resolveGlobalRuntimeConfigPath(cfg *config.Config) string {
	if cfg != nil && cfg.SkillsRuntime != nil && strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) != "" {
		return strings.TrimSpace(cfg.SkillsRuntime.ConfigFile)
	}
	return "configs/runtime.yaml"
}

func resolveConfiguredMCPConfigPath(cfg *config.Config) string {
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && strings.TrimSpace(cfg.AICLI.MCP.ConfigFile) != "" {
		return strings.TrimSpace(cfg.AICLI.MCP.ConfigFile)
	}
	return ""
}

func skillRuntimeConfig(cfg *config.Config) *config.SkillsRuntimeConfig {
	if cfg == nil {
		return nil
	}
	return cfg.SkillsRuntime
}

func resolveChatSkillDirs(cfg *config.Config, session *ChatSession, cliSkillDirs []string) []string {
	if session != nil && len(session.ResolvedSkillDirs) > 0 {
		return appendUniqueExistingDirs(session.ResolvedSkillDirs, cliSkillDirs)
	}
	return resolveConfiguredSkillDirs(skillRuntimeConfig(cfg), cliSkillDirs)
}

func appendUniqueExistingDirs(base []string, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, exists := seen[dir]; exists {
			return
		}
		if _, err := os.Stat(dir); err != nil {
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
