// Package agentdef provides portable AgentDefinition discovery, parse, validation,
// and mapping into runtime profile bindings (Iteration A harness productization).
package agentdef

import (
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// PromptMode controls how the definition body merges into system/role prompts.
type PromptMode string

const (
	// PromptModeExtend appends the body to the default / profile prompt.
	PromptModeExtend PromptMode = "extend"
	// PromptModeFull replaces the role segment with the body.
	PromptModeFull PromptMode = "full"
)

// CompletionRequirement describes end-of-run harness constraints for workers.
type CompletionRequirement string

const (
	// CompletionNone does not require a completion tool call.
	CompletionNone CompletionRequirement = "none"
	// CompletionCompleteTask requires report_task_outcome (complete_task semantics).
	CompletionCompleteTask CompletionRequirement = "complete_task"
)

// Source identifies where a definition was loaded from.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceProject Source = "project"
	SourceProfile Source = "profile"
)

// Definition is the portable agent role definition (Grok-style AgentDefinition).
type Definition struct {
	Name                  string                `yaml:"name" json:"name"`
	Description           string                `yaml:"description,omitempty" json:"description,omitempty"`
	Tools                 []string              `yaml:"tools,omitempty" json:"tools,omitempty"`
	DisallowedTools       []string              `yaml:"disallowedTools,omitempty" json:"disallowedTools,omitempty"`
	PermissionMode        string                `yaml:"permissionMode,omitempty" json:"permissionMode,omitempty"`
	Skills                []string              `yaml:"skills,omitempty" json:"skills,omitempty"`
	Model                 string                `yaml:"model,omitempty" json:"model,omitempty"`
	Provider              string                `yaml:"provider,omitempty" json:"provider,omitempty"`
	PromptMode            PromptMode            `yaml:"promptMode,omitempty" json:"promptMode,omitempty"`
	CompletionRequirement CompletionRequirement `yaml:"completionRequirement,omitempty" json:"completionRequirement,omitempty"`
	Sandbox               string                `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	// Body is the markdown/role instruction body after YAML frontmatter.
	Body string `yaml:"-" json:"body,omitempty"`
	// SourcePath is the file path (or builtin:<name>) that produced this definition.
	SourcePath string `yaml:"-" json:"source_path,omitempty"`
	// Source classifies the discovery root that won for this name.
	Source Source `yaml:"-" json:"source,omitempty"`
}

// Binding is the runtime-facing projection of a Definition.
type Binding struct {
	Definition            Definition
	AgentID               string
	Model                 string
	Provider              string
	PermissionMode        runtimepolicy.Mode
	PromptText            string
	PromptMode            PromptMode
	CompletionRequirement CompletionRequirement
	ToolAllowlist         []string
	ToolDenylist          []string
	SkillAllowlist        []string
	Sandbox               map[string]interface{}
	ReadOnly              *bool
	SourcePath            string
	Source                Source
}

// Normalize fills defaults and trims fields in place.
func (d *Definition) Normalize() {
	if d == nil {
		return
	}
	d.Name = normalizeAgentName(d.Name)
	d.Description = collapseWhitespace(d.Description)
	d.Tools = normalizeStringSlice(d.Tools)
	d.DisallowedTools = normalizeStringSlice(d.DisallowedTools)
	d.Skills = normalizeStringSlice(d.Skills)
	d.Model = strings.TrimSpace(d.Model)
	d.Provider = strings.TrimSpace(d.Provider)
	d.PermissionMode = strings.ToLower(strings.TrimSpace(d.PermissionMode))
	d.PromptMode = PromptMode(strings.ToLower(strings.TrimSpace(string(d.PromptMode))))
	if d.PromptMode == "" {
		d.PromptMode = PromptModeExtend
	}
	d.CompletionRequirement = CompletionRequirement(strings.ToLower(strings.TrimSpace(string(d.CompletionRequirement))))
	if d.CompletionRequirement == "" {
		d.CompletionRequirement = CompletionNone
	}
	d.Sandbox = strings.ToLower(strings.TrimSpace(d.Sandbox))
	d.Body = strings.TrimSpace(d.Body)
	d.SourcePath = strings.TrimSpace(d.SourcePath)
}

func normalizeAgentName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
