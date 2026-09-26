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
	// MaxTurns caps the agent loop for runs of this role. 0 = inherit the
	// runtime/session default (CommandCode-compatible maxTurns semantics).
	MaxTurns int `yaml:"maxTurns,omitempty" json:"max_turns,omitempty"`
	// ReasoningEffort is the preferred reasoning level for this role. Unknown
	// load-time values are dropped with a warning (see Normalize) so a typo
	// cannot silently disable reasoning; the runtime re-validates it against
	// the resolved model afterwards.
	ReasoningEffort string `yaml:"reasoningEffort,omitempty" json:"reasoning_effort,omitempty"`
	// Background marks this role's runs as detached by default. The local
	// runtime always dispatches children asynchronously, so this is currently
	// a declarative hint surfaced in spawn defaults/audit, not a mode switch.
	Background bool `yaml:"background,omitempty" json:"background,omitempty"`
	// ShowOutput surfaces the child's final message verbatim in the parent
	// feed instead of the compact summary projection.
	ShowOutput bool `yaml:"showOutput,omitempty" json:"show_output,omitempty"`
	// Body is the markdown/role instruction body after YAML frontmatter.
	Body string `yaml:"-" json:"body,omitempty"`
	// SourcePath is the file path (or builtin:<name>) that produced this definition.
	SourcePath string `yaml:"-" json:"source_path,omitempty"`
	// Source classifies the discovery root that won for this name.
	Source Source `yaml:"-" json:"source,omitempty"`
	// Warnings carries load-time diagnostics for non-fatal issues (unknown
	// reasoning effort, negative maxTurns, ...). It is not part of the file
	// schema; lint and catalog views surface it.
	Warnings []string `yaml:"-" json:"warnings,omitempty"`
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
	MaxTurns              int
	ReasoningEffort       string
	Background            bool
	ShowOutput            bool
	// ToolsWildcard is true when the definition declared tools: "*" (explicit
	// allow-all). A nil ToolAllowlist alone cannot distinguish "omitted" from
	// "wildcard".
	ToolsWildcard bool
	Warnings      []string
	SourcePath    string
	Source        Source
}

// knownReasoningEfforts is the canonical accepted set (mirrors the routing
// layer's reasoningEffortRank). Unknown values are dropped at load time.
var knownReasoningEfforts = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
	"max":    {},
}

// ToolWildcard is the literal allow-all entry for tools.
const ToolWildcard = "*"

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
	d.ReasoningEffort = strings.ToLower(strings.TrimSpace(d.ReasoningEffort))
	if d.ReasoningEffort != "" {
		if _, ok := knownReasoningEfforts[d.ReasoningEffort]; !ok {
			d.Warnings = append(d.Warnings, "reasoning_effort_unknown:"+d.ReasoningEffort)
			d.ReasoningEffort = ""
		}
	}
	if d.MaxTurns < 0 {
		d.Warnings = append(d.Warnings, "max_turns_negative")
		d.MaxTurns = 0
	}
	d.Body = strings.TrimSpace(d.Body)
	d.SourcePath = strings.TrimSpace(d.SourcePath)
}

// HasExplicitTools reports whether the definition declared any tools entry
// (including the "*" wildcard). Omitted tools inherit the full toolkit.
func (d *Definition) HasExplicitTools() bool {
	return d != nil && len(d.Tools) > 0
}

// ToolsWildcard reports whether tools: "*" was declared.
func (d *Definition) ToolsWildcard() bool {
	if d == nil {
		return false
	}
	for _, tool := range d.Tools {
		if strings.TrimSpace(tool) == ToolWildcard {
			return true
		}
	}
	return false
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
