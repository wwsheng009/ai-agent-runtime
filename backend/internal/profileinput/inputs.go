package profileinput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

// ResolvedAgentInputs contains shared runtime-facing inputs derived from a resolved profile.
type ResolvedAgentInputs struct {
	PromptText    string
	PromptLayers  *runtimeprompt.Layers
	ContextText   string
	ContextValues map[string]interface{}
	ToolPolicy    *runtimepolicy.ToolExecutionPolicy
	// SandboxWarnings are explicit application-sandbox downgrade notices
	// (e.g. strict without workspace root). Callers should surface them.
	SandboxWarnings []string
}

// BuildResolvedAgentInputs converts a resolved profile into shared runtime inputs.
func BuildResolvedAgentInputs(resolved *ResolvedAgent) (*ResolvedAgentInputs, error) {
	if resolved == nil {
		return &ResolvedAgentInputs{}, nil
	}

	promptText, err := LoadPromptText(resolved.Prompts)
	if err != nil {
		return nil, err
	}
	promptLayers, err := LoadPromptLayers(resolved.Prompts)
	if err != nil {
		return nil, err
	}
	contextText, contextValues, err := LoadContextInputs(resolved.Paths)
	if err != nil {
		return nil, err
	}
	toolPolicy, sandboxWarnings, err := BuildToolExecutionPolicyWithWorkspace(resolved.ToolPolicy, "")
	if err != nil {
		return nil, err
	}

	if promptLayers == nil {
		promptLayers = runtimeprompt.NewLayers()
	}
	if strings.TrimSpace(contextText) != "" {
		promptLayers.AddLayer(runtimeprompt.LayerUser, "Profile Runtime Context", contextText, "")
	}

	return &ResolvedAgentInputs{
		PromptText:      ComposeSystemPrompt(promptText, contextText),
		PromptLayers:    promptLayers,
		ContextText:     contextText,
		ContextValues:   contextValues,
		ToolPolicy:      toolPolicy,
		SandboxWarnings: sandboxWarnings,
	}, nil
}

// LoadPromptText loads and composes resolved prompt files.
func LoadPromptText(files ResolvedPromptFiles) (string, error) {
	loaded, err := runtimeprompt.LoadFiles(runtimeprompt.Files{
		System: files.System,
		Role:   files.Role,
		Tools:  files.Tools,
	})
	if err != nil {
		return "", err
	}
	return runtimeprompt.Compose(loaded), nil
}

// LoadPromptLayers loads resolved prompt files into structured prompt fragments.
func LoadPromptLayers(files ResolvedPromptFiles) (*runtimeprompt.Layers, error) {
	loaded, err := runtimeprompt.LoadFiles(runtimeprompt.Files{
		System: files.System,
		Role:   files.Role,
		Tools:  files.Tools,
	})
	if err != nil {
		return nil, err
	}
	layers := runtimeprompt.NewLayers()
	if loaded == nil {
		return layers, nil
	}
	layers.AddLayer(runtimeprompt.LayerBase, "Profile System", loaded.System, loaded.Files.System)
	layers.AddLayer(runtimeprompt.LayerDeveloper, "Profile Role", loaded.Role, loaded.Files.Role)
	layers.AddLayer(runtimeprompt.LayerDeveloper, "Profile Tools", loaded.Tools, loaded.Files.Tools)
	return layers, nil
}

// ComposeSystemPrompt appends read-only profile context to the composed prompt.
func ComposeSystemPrompt(promptText, contextText string) string {
	promptText = strings.TrimSpace(promptText)
	contextText = strings.TrimSpace(contextText)
	switch {
	case promptText == "":
		return contextText
	case contextText == "":
		return promptText
	default:
		return promptText + "\n\n# Profile Runtime Context\n" + contextText
	}
}

// LoadContextInputs loads read-only profile resources that should be visible at runtime.
func LoadContextInputs(paths ResolvedPaths) (string, map[string]interface{}, error) {
	values := make(map[string]interface{})
	resources := make(map[string]interface{})
	sections := make([]string, 0, 2)

	if path := resolveProfileMemoryFile(paths); path != "" {
		content, truncated, err := loadJSONPreview(path, profileMemoryPreviewLimit)
		if err != nil {
			return "", nil, err
		}
		if content != "" {
			values["profile_memory_path"] = path
			resources["memory"] = map[string]interface{}{
				"path":      path,
				"format":    "json",
				"content":   content,
				"truncated": truncated,
			}
			sections = append(sections, renderResourceSection("memory.json", path, "json", content, truncated))
		}
	}

	if path := resolveProfileNotesFile(paths); path != "" {
		content, truncated, err := loadTextPreview(path, profileNotesPreviewLimit)
		if err != nil {
			return "", nil, err
		}
		if content != "" {
			values["profile_notes_path"] = path
			resources["notes"] = map[string]interface{}{
				"path":      path,
				"format":    "markdown",
				"content":   content,
				"truncated": truncated,
			}
			sections = append(sections, renderResourceSection("context/notes.md", path, "markdown", content, truncated))
		}
	}

	if len(resources) > 0 {
		values["profile_resources"] = resources
	}
	if len(values) == 0 {
		return "", nil, nil
	}
	return strings.Join(sections, "\n\n"), values, nil
}

// BuildToolExecutionPolicy converts a resolved profile tool policy into a runtime policy.
func BuildToolExecutionPolicy(resolved ResolvedToolPolicy) (*runtimepolicy.ToolExecutionPolicy, error) {
	policy, _, err := BuildToolExecutionPolicyWithWorkspace(resolved, "")
	return policy, err
}

// BuildToolExecutionPolicyWithWorkspace converts a resolved profile tool policy
// into a runtime policy, materializing named sandbox profiles against workspaceRoot.
func BuildToolExecutionPolicyWithWorkspace(resolved ResolvedToolPolicy, workspaceRoot string) (*runtimepolicy.ToolExecutionPolicy, []string, error) {
	hasPolicy := len(resolved.Allowlist) > 0 || len(resolved.Denylist) > 0 || resolved.ReadOnly != nil || len(resolved.Sandbox) > 0
	if !hasPolicy {
		return nil, nil, nil
	}

	readOnly := resolved.ReadOnly != nil && *resolved.ReadOnly
	allowlist := []string(nil)
	if len(resolved.Allowlist) > 0 {
		allowlist = append([]string(nil), resolved.Allowlist...)
	}
	policy := runtimepolicy.NewToolExecutionPolicy(allowlist, readOnly)
	if len(resolved.Denylist) > 0 {
		policy.DeniedTools = make(map[string]bool, len(resolved.Denylist))
		for _, name := range resolved.Denylist {
			name = strings.TrimSpace(name)
			if name != "" {
				policy.DeniedTools[name] = true
			}
		}
	}
	var warnings []string
	if len(resolved.Sandbox) > 0 {
		result, err := runtimeexecutor.ResolveSandboxMap(resolved.Sandbox, runtimeexecutor.SandboxProfileOptions{
			WorkspaceRoot: workspaceRoot,
		})
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, result.Warnings...)
		if result.ReadOnly {
			readOnly = true
			policy.ReadOnly = true
		}
		if result.Config.Enabled || runtimeexecutor.SandboxConfigActive(result.Config) {
			cfg := result.Config
			if !cfg.Enabled {
				cfg.Enabled = runtimeexecutor.SandboxConfigActive(cfg)
			}
			policy.Sandbox = runtimeexecutor.NewSandbox(&cfg)
			warnings = append(warnings, policy.Sandbox.CollectOSSandboxWarnings(context.Background())...)
		}
	}
	return policy, warnings, nil
}

// MaterializeSandboxForWorkspace re-applies named sandbox profiles once a
// concrete workspace root is known (chat/API session startup).
func MaterializeSandboxForWorkspace(policy *runtimepolicy.ToolExecutionPolicy, sandboxMap map[string]interface{}, workspaceRoot string) ([]string, error) {
	if policy == nil || len(sandboxMap) == 0 {
		return nil, nil
	}
	result, err := runtimeexecutor.ResolveSandboxMap(sandboxMap, runtimeexecutor.SandboxProfileOptions{
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		return nil, err
	}
	if result.ReadOnly {
		policy.ReadOnly = true
	}
	warnings := append([]string(nil), result.Warnings...)
	if result.Config.Enabled || runtimeexecutor.SandboxConfigActive(result.Config) {
		cfg := result.Config
		if !cfg.Enabled {
			cfg.Enabled = true
		}
		policy.Sandbox = runtimeexecutor.NewSandbox(&cfg)
		warnings = append(warnings, policy.Sandbox.CollectOSSandboxWarnings(context.Background())...)
	}
	return warnings, nil
}

const (
	profileMemoryPreviewLimit = 2048
	profileNotesPreviewLimit  = 4096
)

func resolveProfileMemoryFile(paths ResolvedPaths) string {
	candidates := []string{
		strings.TrimSpace(paths.MemoryFile),
	}
	if dir := strings.TrimSpace(paths.MemoryDir); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "memory.json"))
	}
	return firstExistingFile(candidates...)
}

func resolveProfileNotesFile(paths ResolvedPaths) string {
	candidates := []string{
		strings.TrimSpace(paths.ContextNotesFile),
	}
	if dir := strings.TrimSpace(paths.ContextDir); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "notes.md"))
	}
	return firstExistingFile(candidates...)
}

func firstExistingFile(paths ...string) string {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func loadJSONPreview(path string, limit int) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read profile memory file %s: %w", path, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", false, nil
	}

	preview := ""
	var decoded interface{}
	if err := json.Unmarshal(trimmed, &decoded); err == nil {
		formatted, marshalErr := json.MarshalIndent(decoded, "", "  ")
		if marshalErr == nil {
			preview = string(formatted)
		}
	}
	if preview == "" {
		preview = string(trimmed)
	}

	return truncateText(preview, limit), len([]rune(strings.TrimSpace(preview))) > limit, nil
}

func loadTextPreview(path string, limit int) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read profile notes file %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", false, nil
	}
	return truncateText(trimmed, limit), len([]rune(trimmed)) > limit, nil
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func renderResourceSection(name, path, language, content string, truncated bool) string {
	lines := []string{
		"## " + name,
		"Path: " + path,
		"```" + language,
		content,
		"```",
	}
	if truncated {
		lines = append(lines, "Note: content truncated for runtime prompt safety.")
	}
	return strings.Join(lines, "\n")
}

func decodeSandboxConfig(raw map[string]interface{}) (*runtimeexecutor.SandboxConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	result, err := runtimeexecutor.ResolveSandboxMap(raw, runtimeexecutor.SandboxProfileOptions{})
	if err != nil {
		return nil, err
	}
	cfg := result.Config
	if !cfg.Enabled {
		cfg.Enabled = runtimeexecutor.SandboxConfigActive(cfg)
	}
	if !cfg.Enabled && !runtimeexecutor.SandboxConfigActive(cfg) {
		// Named mode off / empty after resolution.
		if result.Effective == runtimeexecutor.SandboxProfileOff || result.Effective == "" {
			return &cfg, nil
		}
	}
	return &cfg, nil
}
