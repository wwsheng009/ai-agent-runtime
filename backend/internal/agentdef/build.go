package agentdef

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// profileAgentYAML mirrors the subset of profile agents/*/agent.yaml we care about.
type profileAgentYAML struct {
	Name                  string   `yaml:"name"`
	Description           string   `yaml:"description"`
	Model                 string   `yaml:"model"`
	Provider              string   `yaml:"provider"`
	Tools                 []string `yaml:"tools"`
	DisallowedTools       []string `yaml:"disallowedTools"`
	DisallowedToolsAlt    []string `yaml:"disallowed_tools"`
	PermissionMode        string   `yaml:"permissionMode"`
	PermissionModeAlt     string   `yaml:"permission_mode"`
	Skills                []string `yaml:"skills"`
	PromptMode            string   `yaml:"promptMode"`
	PromptModeAlt         string   `yaml:"prompt_mode"`
	CompletionRequirement string   `yaml:"completionRequirement"`
	CompletionReqAlt      string   `yaml:"completion_requirement"`
	Sandbox               string   `yaml:"sandbox"`
	// SystemPrompt inline fallback when prompts/role.md is absent.
	SystemPrompt string `yaml:"system_prompt"`
	SystemPrompt2 string `yaml:"systemPrompt"`
}

// AdaptProfileAgent converts a profile agents/<id>/agent.yaml (+ optional prompts) into Definition.
func AdaptProfileAgent(profileRoot, agentID, configFile string) (*Definition, error) {
	agentID = normalizeAgentName(agentID)
	configFile = filepath.Clean(strings.TrimSpace(configFile))
	if configFile == "" {
		paths := profilesys.ResolveAgentPaths(profileRoot, agentID)
		configFile = paths.ConfigFile
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var raw profileAgentYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("agentdef: parse profile agent %s: %w", configFile, err)
	}

	name := normalizeAgentName(raw.Name)
	if name == "" {
		name = agentID
	}
	disallowed := raw.DisallowedTools
	if len(disallowed) == 0 {
		disallowed = raw.DisallowedToolsAlt
	}
	perm := firstNonEmpty(raw.PermissionMode, raw.PermissionModeAlt)
	promptMode := firstNonEmpty(raw.PromptMode, raw.PromptModeAlt)
	completion := firstNonEmpty(raw.CompletionRequirement, raw.CompletionReqAlt)

	body := ""
	paths := profilesys.ResolveAgentPaths(profileRoot, agentID)
	for _, candidate := range []string{paths.PromptRoleFile, paths.PromptSystemFile} {
		if text, err := os.ReadFile(candidate); err == nil {
			body = strings.TrimSpace(string(text))
			if body != "" {
				break
			}
		}
	}
	if body == "" {
		body = firstNonEmpty(raw.SystemPrompt, raw.SystemPrompt2)
	}

	def := &Definition{
		Name:                  name,
		Description:           raw.Description,
		Tools:                 raw.Tools,
		DisallowedTools:       disallowed,
		PermissionMode:        perm,
		Skills:                raw.Skills,
		Model:                 raw.Model,
		Provider:              raw.Provider,
		PromptMode:            PromptMode(promptMode),
		CompletionRequirement: CompletionRequirement(completion),
		Sandbox:               raw.Sandbox,
		Body:                  body,
		SourcePath:            configFile,
		Source:                SourceProfile,
	}
	def.Normalize()
	if err := Validate(def); err != nil {
		return nil, err
	}
	return def, nil
}

// BuildBinding projects a Definition into a runtime Binding.
func BuildBinding(def *Definition) (*Binding, error) {
	if def == nil {
		return nil, fmt.Errorf("agentdef: definition is nil")
	}
	clone := *def
	clone.Normalize()
	if err := Validate(&clone); err != nil {
		return nil, err
	}

	mode := runtimepolicy.ModeDefault
	if clone.PermissionMode != "" {
		switch runtimepolicy.Mode(clone.PermissionMode) {
		case runtimepolicy.ModeAcceptEdits, runtimepolicy.ModePlan, runtimepolicy.ModeBypassPermissions, runtimepolicy.ModeDefault:
			mode = runtimepolicy.Mode(clone.PermissionMode)
		case "dont_ask":
			// Treat dont_ask as default for mode enum; ask resolution still headless-denies.
			mode = runtimepolicy.ModeDefault
		}
	}

	var readOnly *bool
	sandbox := map[string]interface{}{}
	switch clone.Sandbox {
	case "read-only", "readonly":
		v := true
		readOnly = &v
		sandbox["mode"] = "read-only"
	case "off", "workspace", "strict":
		sandbox["mode"] = clone.Sandbox
	}

	promptText := clone.Body
	if clone.Description != "" && clone.PromptMode == PromptModeExtend {
		// Keep description available as a short header when body is present.
		if promptText == "" {
			promptText = clone.Description
		}
	}

	return &Binding{
		Definition:            clone,
		AgentID:               clone.Name,
		Model:                 clone.Model,
		Provider:              clone.Provider,
		PermissionMode:        mode,
		PromptText:            promptText,
		PromptMode:            clone.PromptMode,
		CompletionRequirement: clone.CompletionRequirement,
		ToolAllowlist:         append([]string(nil), clone.Tools...),
		ToolDenylist:          append([]string(nil), clone.DisallowedTools...),
		SkillAllowlist:        append([]string(nil), clone.Skills...),
		Sandbox:               sandbox,
		ReadOnly:              readOnly,
		SourcePath:            clone.SourcePath,
		Source:                clone.Source,
	}, nil
}

// ToResolvedAgent maps a Binding into profile.ResolvedAgent for chat/profileinput reuse.
func ToResolvedAgent(binding *Binding, profileRoot string) *profilesys.ResolvedAgent {
	if binding == nil {
		return nil
	}
	agentID := binding.AgentID
	if agentID == "" {
		agentID = binding.Definition.Name
	}
	root := strings.TrimSpace(profileRoot)
	resolved := &profilesys.ResolvedAgent{
		ProfileName: "agentdef",
		ProfileRoot: root,
		AgentID:     agentID,
		Provider:    binding.Provider,
		Model:       binding.Model,
		ToolPolicy: profilesys.ResolvedToolPolicy{
			Allowlist: append([]string(nil), binding.ToolAllowlist...),
			Denylist:  append([]string(nil), binding.ToolDenylist...),
			ReadOnly:  binding.ReadOnly,
			Sandbox:   binding.Sandbox,
			Sources:   []string{binding.SourcePath},
		},
		Paths: profilesys.ResolvedPaths{
			ProfileRoot:     root,
			AgentDir:        binding.SourcePath,
			AgentConfigFile: binding.SourcePath,
		},
	}
	return resolved
}

// MergePrompt applies binding prompt text onto an existing system prompt according to PromptMode.
func MergePrompt(existing string, binding *Binding) string {
	if binding == nil {
		return existing
	}
	body := strings.TrimSpace(binding.PromptText)
	if body == "" {
		body = strings.TrimSpace(binding.Definition.Body)
	}
	if body == "" {
		return existing
	}
	existing = strings.TrimSpace(existing)
	switch binding.PromptMode {
	case PromptModeFull:
		return body
	default:
		if existing == "" {
			return body
		}
		return existing + "\n\n# Agent Role (" + binding.AgentID + ")\n" + body
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
