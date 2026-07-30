package policy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/patchutil"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// ToolExecutionPolicy constrains which runtime tools may execute.
type ToolExecutionPolicy struct {
	ReadOnly               bool
	AllowedTools           map[string]bool
	DeniedTools            map[string]bool
	AllowlistEnabled       bool
	AllowedCapabilities    map[Capability]bool
	CapabilityScopeEnabled bool
	CapabilityResolver     CapabilityResolver
	BlockUntrustedMCP      bool
	BlockRemoteWrites      bool
	Sandbox                *executor.Sandbox
}

// NewToolExecutionPolicy creates a new tool policy.
func NewToolExecutionPolicy(allowedTools []string, readOnly bool) *ToolExecutionPolicy {
	policy := &ToolExecutionPolicy{
		ReadOnly:          readOnly,
		BlockUntrustedMCP: true,
		BlockRemoteWrites: true,
	}
	if allowedTools != nil {
		policy.AllowlistEnabled = true
		policy.AllowedTools = buildAllowedToolsMap(allowedTools)
	}
	return policy
}

// AllowTool checks whether a tool is allowed by name.
func (p *ToolExecutionPolicy) AllowTool(toolName string) error {
	if p == nil {
		return nil
	}
	normalizedToolName := normalizeToolName(toolName)
	if p.DeniedTools[toolName] || runtimeOwnedEssentialExplicitlyDenied(p.DeniedTools, normalizedToolName) {
		return fmt.Errorf("tool denied by execution policy: %s", toolName)
	}
	// Runtime-owned agent essentials (plan mode, goal/todos, collab, search_tool)
	// may be injected after a *narrow non-empty* product/profile allowlist is
	// derived. The allowlist must not brick the agent control plane. An empty
	// allowlist means tools are fully disabled (DisableTools) and essentials
	// do not bypass. Explicit deny, capability scope, and read-only still apply.
	if p.AllowlistEnabled && !p.AllowedTools[toolName] {
		if len(p.AllowedTools) == 0 || !IsRuntimeOwnedEssentialTool(normalizedToolName) {
			return fmt.Errorf("tool not allowed by execution policy: %s", toolName)
		}
	}
	if err := p.AllowCapabilities(p.resolveCapabilities(EvalRequest{ToolName: toolName})); err != nil {
		return fmt.Errorf("tool %s: %w", toolName, err)
	}
	if p.ReadOnly {
		if normalizedToolName == "background_task" {
			return fmt.Errorf("read-only policy blocks background command execution: %s", toolName)
		}
		if IsWriteLikeToolName(toolName) {
			return fmt.Errorf("read-only policy blocks write-like tool: %s", toolName)
		}
		// Shell-like tools are NOT blocked by name under read-only: a shell tool
		// can run read-only commands (git status, rg, ls). Whether a specific
		// invocation is allowed is decided per-command in AllowToolCall, which
		// hard-denies any command outside the read-only allow table.
	}
	return nil
}

// IsRuntimeOwnedEssentialTool reports tools the runtime needs for agent
// operation (session control, plan mode, collab, catalog search). These bypass
// allowlist gates so a narrow --allow-tool / profile allowlist cannot brick the
// agent control plane. Explicit DeniedTools, capability scope, and read-only
// still apply.
func IsRuntimeOwnedEssentialTool(toolName string) bool {
	switch normalizeToolName(toolName) {
	case "search_tool",
		"ask_user_question",
		"enter_plan_mode", "exit_plan_mode",
		"todos", "get_goal", "read_goal", "update_goal",
		"background_task", "task_output",
		"spawn_agent", "spawn_subagents", "spawn_team",
		"list_agents", "wait_agent", "read_agent_events",
		"send_message", "send_input", "followup_task",
		"close_agent", "resume_agent",
		"apply_agent_worktree", "discard_agent_worktree",
		"resolve_agent_approval",
		"wait_team", "send_team_message",
		"read_mailbox_digest", "read_task_spec", "read_task_context",
		"report_task_outcome", "block_current_task":
		return true
	default:
		return false
	}
}

func runtimeOwnedEssentialExplicitlyDenied(deniedTools map[string]bool, normalizedToolName string) bool {
	if !IsRuntimeOwnedEssentialTool(normalizedToolName) {
		return false
	}
	for name, denied := range deniedTools {
		if denied && normalizeToolName(name) == normalizedToolName {
			return true
		}
	}
	return false
}

// AllowToolInfo validates a tool's governance metadata.
func (p *ToolExecutionPolicy) AllowToolInfo(tool skill.ToolInfo) error {
	if err := p.AllowTool(tool.Name); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if err := p.AllowCapabilities(p.resolveCapabilities(EvalRequest{ToolName: tool.Name, ToolInfo: &tool})); err != nil {
		return fmt.Errorf("tool %s: %w", tool.Name, err)
	}
	if p.BlockUntrustedMCP && tool.MCPTrustLevel == "untrusted_remote" && IsWriteLikeToolName(tool.Name) {
		return fmt.Errorf("untrusted remote MCP cannot execute write-like tool: %s", tool.Name)
	}
	if p.BlockRemoteWrites && tool.ExecutionMode == "remote_mcp" && IsWriteLikeToolName(tool.Name) {
		return fmt.Errorf("remote MCP execution mode blocks write-like tool: %s", tool.Name)
	}
	return nil
}

// AllowToolCall validates a tool call including nested args.
func (p *ToolExecutionPolicy) AllowToolCall(tool skill.ToolInfo, args map[string]interface{}) error {
	if err := p.AllowToolInfo(tool); err != nil {
		return err
	}
	if p == nil {
		return nil
	}

	commands := collectCommandArgs(args)
	if p.ReadOnly {
		// Shell-like tools stay visible under read-only; each concrete command
		// must match the read-only allow table (git status, rg, ls, ...).
		if IsShellLikeToolName(tool.Name) {
			if len(commands) == 0 {
				return fmt.Errorf("read-only policy requires an explicit read-only shell command for %s", tool.Name)
			}
			for _, command := range commands {
				if !IsShellReadOnlyCommand(command) {
					return fmt.Errorf("read-only policy blocks non-readonly shell command: %s", command)
				}
			}
		} else {
			// Non-shell tools may still smuggle nested shell interpreters via
			// command-like args; keep the interpreter block for those paths.
			for _, command := range commands {
				if isShellLikeCommand(command) {
					return fmt.Errorf("read-only policy blocks shell-like command: %s", command)
				}
			}
		}
	}
	if len(args) == 0 {
		return nil
	}

	if p.Sandbox == nil {
		return nil
	}

	for _, command := range commands {
		if err := p.Sandbox.ValidateCommand(command); err != nil {
			return err
		}
	}

	op := executor.OpRead
	if IsWriteLikeToolName(tool.Name) {
		op = executor.OpWrite
	}
	for _, rawURL := range collectURLArgs(args) {
		if err := p.Sandbox.CheckURL(rawURL); err != nil {
			return err
		}
	}
	for _, path := range collectPathArgs(args) {
		if err := p.Sandbox.CheckPermission(op, path); err != nil {
			return err
		}
	}

	return nil
}

// AllowsDefinition is used before tool definitions are exposed.
func (p *ToolExecutionPolicy) AllowsDefinition(toolName string) bool {
	return p == nil || p.AllowTool(toolName) == nil
}

// Clone returns a defensive copy.
func (p *ToolExecutionPolicy) Clone() *ToolExecutionPolicy {
	if p == nil {
		return nil
	}
	cloned := &ToolExecutionPolicy{
		ReadOnly:               p.ReadOnly,
		AllowedTools:           cloneAllowedToolsMap(p.AllowedTools),
		DeniedTools:            cloneAllowedToolsMap(p.DeniedTools),
		AllowlistEnabled:       p.AllowlistEnabled,
		AllowedCapabilities:    cloneCapabilityMap(p.AllowedCapabilities),
		CapabilityScopeEnabled: p.CapabilityScopeEnabled,
		CapabilityResolver:     p.CapabilityResolver,
		BlockUntrustedMCP:      p.BlockUntrustedMCP,
		BlockRemoteWrites:      p.BlockRemoteWrites,
	}
	if p.Sandbox != nil {
		cfg := p.Sandbox.Config()
		cloned.Sandbox = executor.NewSandbox(&cfg)
	}
	return cloned
}

// DeriveChild narrows the parent policy for child execution.
func (p *ToolExecutionPolicy) DeriveChild(allowedTools []string, readOnly bool) *ToolExecutionPolicy {
	return p.DeriveChildForTask(allowedTools, readOnly, "", nil)
}

// DeriveChildForTask creates the minimum capability surface implied by role,
// requested tools, and declared write paths, while never widening the parent.
func (p *ToolExecutionPolicy) DeriveChildForTask(allowedTools []string, readOnly bool, role string, writePaths []string) *ToolExecutionPolicy {
	if p == nil {
		child := NewToolExecutionPolicy(allowedTools, readOnly)
		child.SetCapabilityScope(CapabilitiesForTask(role, readOnly, allowedTools, writePaths))
		return child
	}
	child := p.Clone()
	child.ReadOnly = child.ReadOnly || readOnly
	child.AllowlistEnabled, child.AllowedTools = intersectAllowedTools(p.AllowlistEnabled, p.AllowedTools, allowedTools)
	requested := CapabilitiesForTask(role, child.ReadOnly, allowedTools, writePaths)
	if p.CapabilityScopeEnabled {
		requested = intersectCapabilities(p.AllowedCapabilities, requested)
	}
	child.SetCapabilityScope(requested)
	return child
}

// AllowedToolNames returns sorted allowed tool names.
func (p *ToolExecutionPolicy) AllowedToolNames() []string {
	if p == nil || !p.AllowlistEnabled {
		return nil
	}
	names := make([]string, 0, len(p.AllowedTools))
	for name, allowed := range p.AllowedTools {
		if !allowed || strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsWriteLikeToolName reports whether a tool name implies mutation.
func IsWriteLikeToolName(toolName string) bool {
	lower := strings.ToLower(toolName)
	for _, marker := range []string{"write", "edit", "patch", "apply", "delete", "remove", "rename", "move", "download"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsShellLikeToolName reports whether a tool name likely executes shell commands.
func IsShellLikeToolName(toolName string) bool {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"shell", "bash", "exec"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// HasMutationHints reports whether args carry explicit mutation hints.
func HasMutationHints(args map[string]interface{}) bool {
	if len(args) == 0 {
		return false
	}
	for _, key := range []string{"mutated_paths", "mutated_files", "changed_paths", "changed_files"} {
		if hasStringSliceValue(args[key]) {
			return true
		}
	}
	if raw, ok := args["patch"].(string); ok && strings.TrimSpace(raw) != "" {
		return true
	}
	if raw, ok := args["diff"].(string); ok && strings.TrimSpace(raw) != "" {
		return true
	}
	return false
}

func hasStringSliceValue(value interface{}) bool {
	switch items := value.(type) {
	case []string:
		for _, item := range items {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	case []interface{}:
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

func isShellLikeCommand(command string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	if name == "" {
		return false
	}
	for _, shell := range []string{"sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh", "python", "python3", "node"} {
		if name == shell {
			return true
		}
	}
	return false
}

func collectCommandArgs(args map[string]interface{}) []string {
	return collectStringArgs(args, map[string]bool{
		"cmd":        true,
		"command":    true,
		"executable": true,
		"program":    true,
	})
}

func collectPathArgs(args map[string]interface{}) []string {
	paths := collectStringArgs(args, map[string]bool{
		"path":           true,
		"file":           true,
		"source":         true,
		"destination":    true,
		"target":         true,
		"workspace_path": true,
		"cwd":            true,
		"workdir":        true,
		"working_dir":    true,
		"paths":          true,
		"files":          true,
	})
	if raw, ok := args["patch"].(string); ok {
		paths = append(paths, patchutil.ExtractPaths(raw)...)
	}
	if raw, ok := args["diff"].(string); ok {
		paths = append(paths, patchutil.ExtractPaths(raw)...)
	}
	return dedupeValues(paths)
}

func collectURLArgs(args map[string]interface{}) []string {
	return collectStringArgs(args, map[string]bool{
		"url":       true,
		"urls":      true,
		"uri":       true,
		"uris":      true,
		"endpoint":  true,
		"endpoints": true,
		"base_url":  true,
		"host":      true,
	})
}

func collectStringArgs(args map[string]interface{}, keys map[string]bool) []string {
	if len(args) == 0 || len(keys) == 0 {
		return nil
	}
	collected := make([]string, 0, len(keys))
	seen := make(map[string]bool)
	var walk func(value interface{}, parentKey string)
	walk = func(value interface{}, parentKey string) {
		switch typed := value.(type) {
		case string:
			if !keys[parentKey] {
				return
			}
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" || seen[trimmed] {
				return
			}
			seen[trimmed] = true
			collected = append(collected, trimmed)
		case []string:
			for _, item := range typed {
				walk(item, parentKey)
			}
		case []interface{}:
			for _, item := range typed {
				walk(item, parentKey)
			}
		case map[string]interface{}:
			for key, item := range typed {
				walk(item, strings.ToLower(strings.TrimSpace(key)))
			}
		}
	}
	for key, value := range args {
		walk(value, strings.ToLower(strings.TrimSpace(key)))
	}
	return collected
}

func dedupeValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildAllowedToolsMap(allowedTools []string) map[string]bool {
	if len(allowedTools) == 0 {
		return map[string]bool{}
	}
	result := make(map[string]bool, len(allowedTools))
	for _, tool := range allowedTools {
		if name := strings.TrimSpace(tool); name != "" {
			result[name] = true
		}
	}
	return result
}

func cloneAllowedToolsMap(source map[string]bool) map[string]bool {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func intersectAllowedTools(parentEnabled bool, parent map[string]bool, requested []string) (bool, map[string]bool) {
	switch {
	case !parentEnabled && requested == nil:
		return false, nil
	case parentEnabled && requested == nil:
		return true, cloneAllowedToolsMap(parent)
	case !parentEnabled && requested != nil:
		return true, buildAllowedToolsMap(requested)
	default:
		requestedSet := buildAllowedToolsMap(requested)
		result := make(map[string]bool)
		for name := range requestedSet {
			if parent[name] {
				result[name] = true
			}
		}
		return true, result
	}
}
