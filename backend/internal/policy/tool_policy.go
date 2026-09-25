package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/patchutil"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// ToolExecutionPolicy constrains which runtime tools may execute.
type ToolExecutionPolicy struct {
	ReadOnly         bool
	AllowedTools     map[string]bool
	DeniedTools      map[string]bool
	AllowlistEnabled bool
	// BlockDelegation is an inherited control-plane boundary.  It is kept
	// separate from ReadOnly because a read-only agent may still be allowed to
	// coordinate work, while an ordinary child must not create more agents
	// unless its parent explicitly opted into nested delegation.
	BlockDelegation        bool
	AllowedCapabilities    map[Capability]bool
	CapabilityScopeEnabled bool
	CapabilityResolver     CapabilityResolver
	BlockUntrustedMCP      bool
	BlockRemoteWrites      bool
	Sandbox                *executor.Sandbox
	// PlanModeActive marks that the session is in plan mode, and
	// PlanWriteAllowPaths / PlanWriteAllowPathsResolved carry the plan write
	// allowlist (raw relative / workspace-resolved absolute, see
	// SetPlanWriteExemption). While active, file-mutating tools may bypass the
	// tool allowlist gate when every call target is a plan file, so a narrow
	// --allow-tool list cannot brick plan mode's own write path. Explicit deny,
	// read-only, capability scope, and the sandbox boundary keep their
	// precedence.
	PlanModeActive              bool
	PlanWriteAllowPaths         []string
	PlanWriteAllowPathsResolved []string
	// PathAnchorRoot is the filesystem root this policy resolves relative path
	// arguments against when the caller's context carries no
	// toolctx.WorkspaceRoot. It must mirror the base path the toolkit tools were
	// registered with (tools.Manager SetBasePath, i.e.
	// runtimecfg.RuntimeConfig.Workspace.Root) so the static policy validates the
	// same file the executor touches. Without it a relative argument would be
	// resolved against the server process working directory while the executor
	// anchors it to the session workspace, which both false-denies legitimate
	// calls and lets a path pass the sandbox check against a different file.
	PathAnchorRoot string
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

// SetPathAnchorRoot records the fallback root used to resolve relative path
// arguments when the execution context has no workspace root.
//
// Pass exactly the base path the toolkit tools were registered with
// (tools.Manager SetBasePath, i.e. runtimecfg.RuntimeConfig.Workspace.Root).
// The session-bound workspace is deliberately not an alternative here: it is
// carried per run in toolctx.WorkspaceRoot, and the executor only consults the
// registered base path when that context value is absent. Anchoring the policy
// to a session workspace the executor would not use re-creates the very
// mismatch this field exists to prevent - the sandbox would clear a path that
// resolves to a different file than the one the tool touches. See
// PathAnchorRoot.
func (p *ToolExecutionPolicy) SetPathAnchorRoot(root string) *ToolExecutionPolicy {
	if p == nil {
		return nil
	}
	p.PathAnchorRoot = strings.TrimSpace(root)
	return p
}

// SetPlanWriteExemption records the plan-mode write exemption for this policy:
// while plan mode is active, file-mutating tools (write/apply_patch and their
// aliases) bypass the tool allowlist gate for calls whose every target is
// covered by the plan write allowlist. Pass the raw allowlist (relative,
// display/fallback semantics) and, when known, its workspace-resolved absolute
// form; the resolved list wins during matching.
//
// The exemption does not weaken any other check: explicit denies, read-only
// delegation, capability scope, and the sandbox path boundary are still
// enforced, and the concrete target paths are verified per call.
func (p *ToolExecutionPolicy) SetPlanWriteExemption(active bool, raw []string, resolved []string) *ToolExecutionPolicy {
	if p == nil {
		return nil
	}
	p.PlanModeActive = active
	p.PlanWriteAllowPaths = cleanPlanAllowPaths(raw)
	p.PlanWriteAllowPathsResolved = cleanPlanAllowPaths(resolved)
	return p
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
	if p.BlockDelegation && IsDelegationToolName(normalizedToolName) {
		return fmt.Errorf("nested delegation is disabled by execution policy: %s", toolName)
	}
	// Runtime-owned agent essentials (plan mode, goal/todos, collab, search_tool)
	// may be injected after a *narrow non-empty* product/profile allowlist is
	// derived. The allowlist must not brick the agent control plane. An empty
	// allowlist means tools are fully disabled (DisableTools) and essentials
	// do not bypass. Explicit deny, capability scope, and read-only still apply.
	if p.AllowlistEnabled && !p.AllowedTools[toolName] {
		if len(p.AllowedTools) == 0 ||
			(!IsRuntimeOwnedEssentialTool(normalizedToolName) && !p.planWriteAllowlistExempt(normalizedToolName)) {
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
		"enter_plan_mode", "exit_plan_mode", "plan_review",
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
		"report_task_outcome", "block_current_task",
		"supervision_snapshot", "supervision_descendants", "read_agent_result", "subagent_status", "subagent_inspect_task", "subagent_ack_lifecycle", "subagent_control", "ack_lifecycle", "control_descendant":
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

// planWriteAllowlistExempt reports whether the plan-mode write exemption lets a
// tool name past the allowlist gate.
//
// The exemption is claimable only while plan mode is active, only for
// file-mutating tools, and only with a configured plan allowlist. It relaxes
// the allowlist gate alone: the concrete targets are still verified per call in
// allowToolCall, and explicit deny, read-only, capability scope, and the
// sandbox boundary are checked either before or after this gate.
func (p *ToolExecutionPolicy) planWriteAllowlistExempt(normalizedToolName string) bool {
	if p == nil || !p.PlanModeActive {
		return false
	}
	if !isPlanWriteToolName(normalizedToolName) {
		return false
	}
	return hasPlanAllowEntries(p.PlanWriteAllowPaths) || hasPlanAllowEntries(p.PlanWriteAllowPathsResolved)
}

// planWriteExemptionClaimed reports whether this exact tool name took the
// plan-mode write exemption (i.e. the allowlist gate was relaxed for it). The
// condition mirrors AllowTool's gate so the per-call target verification in
// allowToolCall runs exactly for the calls the exemption let through.
func (p *ToolExecutionPolicy) planWriteExemptionClaimed(toolName string) bool {
	if p == nil || !p.AllowlistEnabled || p.AllowedTools[toolName] {
		return false
	}
	return p.planWriteAllowlistExempt(toolName)
}

// planTargetWorkspaceRoot resolves plan exemption targets the same way the
// sandbox check resolves path arguments: the session-bound workspace root from
// ctx first, then the tool-registered PathAnchorRoot.
func (p *ToolExecutionPolicy) planTargetWorkspaceRoot(ctx context.Context) string {
	if root := planWorkspaceRoot(ctx); root != "" {
		return root
	}
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.PathAnchorRoot)
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
//
// Without a context there is no session workspace root, so relative path
// arguments are resolved against the policy's PathAnchorRoot and, only when
// that is unset too, against the process working directory. Callers that have a
// session context should prefer AllowToolCallWithContext so the checks cover the
// same file the tool touches.
func (p *ToolExecutionPolicy) AllowToolCall(tool skill.ToolInfo, args map[string]interface{}) error {
	return p.AllowToolCallWithContext(context.Background(), tool, args)
}

// AllowToolCallWithContext validates a tool call like AllowToolCall, but resolves
// relative path arguments the way the executor will: against the session-bound
// workspace root carried in ctx (toolctx.WorkspaceRoot), falling back to the
// policy's PathAnchorRoot (the toolkit base path) and only then to the process
// working directory.
//
// The policy must inspect the same target the tool will mutate. Toolkit tools and
// the execution preflight both anchor relative paths to the session workspace
// root, so a policy that used the process working directory would validate a
// different file whenever the two differ (the normal case for directory-bound
// sessions).
func (p *ToolExecutionPolicy) AllowToolCallWithContext(ctx context.Context, tool skill.ToolInfo, args map[string]interface{}) error {
	if err := p.AllowToolInfo(tool); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	return p.allowToolCall(ctx, tool, args)
}

func (p *ToolExecutionPolicy) allowToolCall(ctx context.Context, tool skill.ToolInfo, args map[string]interface{}) error {
	// Inspect the same argument shape the executor will receive: provider
	// fallbacks such as {"_raw": "{...}"} must not hide a path or a command
	// from the policy below.
	args = toolargs.Normalize(args)
	argKeys := policyArgKeysForTool(tool.Name)

	// Plan-mode write exemption: AllowTool waved this file-mutating tool past
	// the allowlist gate, so every concrete target of this call must be verified
	// against the plan write allowlist here. Explicit deny, read-only, and
	// capability scope were already enforced (AllowTool), and the sandbox
	// boundary below still runs, so the exemption never widens an execution
	// beyond the plan files it was granted for. Calls the allowlist already
	// covers keep their previous treatment (the engine's plan gate still applies
	// to them), so the exemption never narrows an explicitly granted tool.
	if p.planWriteExemptionClaimed(tool.Name) {
		targets := planWriteTargets(tool.Name, args)
		if len(targets) == 0 {
			return fmt.Errorf("plan mode cannot verify %s targets against the plan write allowlist", tool.Name)
		}
		if !planWriteTargetsAllowed(
			tool.Name,
			args,
			p.PlanWriteAllowPathsResolved,
			p.PlanWriteAllowPaths,
			p.planTargetWorkspaceRoot(ctx),
		) {
			return fmt.Errorf("plan mode allows %s only for plan files, got: %s", tool.Name, strings.Join(targets, ", "))
		}
	}

	// A non-string path/url/command cannot be inspected, and silently skipping
	// it would turn a sandbox or read-only rule into a no-op. Refuse the call
	// instead of running it with unverifiable arguments.
	if p.ReadOnly || p.Sandbox != nil {
		if issues := policyArgKindIssues(args, argKeys); len(issues) > 0 {
			return fmt.Errorf("policy cannot verify tool arguments before execution: %s", strings.Join(issues, "; "))
		}
	}

	commands := collectStringArgs(args, argKeys.command)
	if p.ReadOnly {
		// Shell-like tools stay visible under read-only; each concrete command
		// must match the read-only allow table (git status, rg, ls, ...).
		if IsShellLikeToolName(tool.Name) {
			if len(commands) == 0 {
				return fmt.Errorf("read-only policy requires an explicit read-only shell command for %s", tool.Name)
			}
			for _, command := range commands {
				assessment := AssessShellReadOnlyCommand(command)
				if assessment.Allowed {
					continue
				}
				if assessment.Reason == ShellReadOnlyReasonCompound {
					return fmt.Errorf("read-only policy blocks compound shell command; submit one command per shell.commands entry: %s", command)
				}
				if assessment.Reason == ShellReadOnlyReasonDynamicSyntax {
					return fmt.Errorf("read-only policy blocks shell redirection or dynamic command syntax: %s", command)
				}
				if !assessment.Allowed {
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
	for _, rawURL := range collectStringArgs(args, argKeys.url) {
		if err := p.Sandbox.CheckURL(rawURL); err != nil {
			return err
		}
	}
	for _, path := range collectPathArgs(args, argKeys) {
		// Resolve exactly like the executor: a relative path means "inside the
		// session workspace", not "inside the server process directory".
		if err := p.Sandbox.CheckPermission(op, p.resolvePolicyPath(ctx, path)); err != nil {
			return err
		}
	}

	return nil
}

// resolvePolicyPath anchors a relative path argument to the session-bound
// workspace root carried in ctx, falling back to the policy's PathAnchorRoot
// (the toolkit base path), mirroring toolkit effectiveBasePath /
// resolvePathWithContext and the execution preflight. Absolute paths are
// returned unchanged, and when neither root is known the value is left untouched
// so the sandbox keeps resolving it against the process working directory (the
// historical behavior).
func (p *ToolExecutionPolicy) resolvePolicyPath(ctx context.Context, targetPath string) string {
	trimmed := strings.TrimSpace(targetPath)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return trimmed
	}
	root := ""
	if ctx != nil {
		root = strings.TrimSpace(toolctx.WorkspaceRoot(ctx))
	}
	if root == "" && p != nil {
		root = strings.TrimSpace(p.PathAnchorRoot)
	}
	if root == "" {
		return trimmed
	}
	if !filepath.IsAbs(root) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return trimmed
		}
		root = absRoot
	}
	return filepath.Clean(filepath.Join(root, trimmed))
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
		BlockDelegation:        p.BlockDelegation,
		AllowedCapabilities:    cloneCapabilityMap(p.AllowedCapabilities),
		CapabilityScopeEnabled: p.CapabilityScopeEnabled,
		CapabilityResolver:     p.CapabilityResolver,
		BlockUntrustedMCP:      p.BlockUntrustedMCP,
		BlockRemoteWrites:      p.BlockRemoteWrites,
		PathAnchorRoot:         p.PathAnchorRoot,
	}
	cloned.PlanModeActive = p.PlanModeActive
	cloned.PlanWriteAllowPaths = append([]string(nil), p.PlanWriteAllowPaths...)
	cloned.PlanWriteAllowPathsResolved = append([]string(nil), p.PlanWriteAllowPathsResolved...)
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

// IsDelegationToolName reports tools that can create another execution node.
// Keep this list independent of the tool broker package so policy can enforce
// the boundary on both built-in and broker-provided control-plane tools
// without introducing an import cycle.
func IsDelegationToolName(toolName string) bool {
	switch normalizeToolName(toolName) {
	case "spawn_agent", "spawn_subagents", "spawn_team":
		return true
	default:
		return false
	}
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

// policyMutationHintArgKeys lists argument names that unambiguously announce an
// already-applied mutation, including the names third-party/MCP tools use to
// report the paths they wrote.
var policyMutationHintArgKeys = []string{
	"mutated_paths", "mutated_files", "changed_paths", "changed_files",
	"write_paths", "writes", "files_written", "written_files",
	"created_paths", "created_files", "deleted_paths", "deleted_files",
	"removed_paths", "removed_files", "moved_paths", "renamed_paths",
	"modified_paths", "modified_files", "updated_paths", "updated_files",
}

// policyMutationVerbSegments holds the leading segment of a mutation-ish argument
// name (write_paths, deleted_files, patched_uri, ...).
var policyMutationVerbSegments = map[string]bool{
	"write": true, "writes": true, "written": true, "writing": true,
	"create": true, "creates": true, "created": true, "creating": true,
	"delete": true, "deletes": true, "deleted": true, "deleting": true,
	"remove": true, "removes": true, "removed": true, "removing": true,
	"move": true, "moves": true, "moved": true, "moving": true,
	"rename": true, "renames": true, "renamed": true, "renaming": true,
	"modify": true, "modifies": true, "modified": true, "modifying": true,
	"update": true, "updates": true, "updated": true, "updating": true,
	"mutate": true, "mutates": true, "mutated": true, "mutating": true,
	"change": true, "changes": true, "changed": true, "changing": true,
	"edit": true, "edits": true, "edited": true, "editing": true,
	"patch": true, "patches": true, "patched": true, "patching": true,
	"apply": true, "applies": true, "applied": true, "applying": true,
	"commit": true, "commits": true, "committed": true, "committing": true,
}

// policyMutationNameSuffixes are the object kinds a mutation verb can announce.
var policyMutationNameSuffixes = []string{"path", "paths", "file", "files", "url", "urls", "uri", "uris"}

// HasMutationHints reports whether args carry explicit mutation hints.
func HasMutationHints(args map[string]interface{}) bool {
	if len(args) == 0 {
		return false
	}
	for _, key := range policyMutationHintArgKeys {
		if hasMutationValue(args[key]) {
			return true
		}
	}
	for _, key := range policyPatchArgKeyList {
		if hasMutationValue(args[key]) {
			return true
		}
	}
	// Third-party tools are free to name their mutation arguments, so also treat
	// a mutation verb paired with a path/file/URL object as a hint. Names that
	// merely share a verb stem (created_after, updated_at) are deliberately not
	// matched: over-detecting here would serialize otherwise parallel reads.
	for key, value := range args {
		if hasMutationValue(value) && keyLooksLikeMutationName(normalizePolicyArgKey(key)) {
			return true
		}
	}
	return false
}

func keyLooksLikeMutationName(key string) bool {
	verb, rest, found := strings.Cut(key, "_")
	if !found || !policyMutationVerbSegments[verb] {
		return false
	}
	for _, suffix := range policyMutationNameSuffixes {
		if rest == suffix || strings.HasSuffix(rest, "_"+suffix) {
			return true
		}
	}
	return false
}

// hasMutationValue is deliberately conservative: any non-empty value counts as
// a mutation hint. Under-detecting a mutation would skip checkpointing or
// parallel-scheduling safeguards, while over-detecting only costs one extra
// checkpoint, so unrecognized shapes resolve to true.
func hasMutationValue(value interface{}) bool {
	switch items := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(items) != ""
	case []string:
		return len(items) > 0
	case []interface{}:
		return len(items) > 0
	case []map[string]interface{}:
		return len(items) > 0
	case map[string]interface{}:
		return len(items) > 0
	default:
		return true
	}
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

// policyCommandArgKeys, policyPathArgKeys, policyURLArgKeys and
// policyPatchArgKeys name every argument the runtime policy inspects before a
// tool call executes. They are shared with policyArgKindIssues so the
// fail-closed kind check can never drift away from what is actually enforced.
var (
	policyCommandArgKeys = map[string]bool{
		"cmd":        true,
		"command":    true,
		"executable": true,
		"program":    true,
		"commands":   true,
	}

	policyPathArgKeys = map[string]bool{
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
	}

	policyURLArgKeys = map[string]bool{
		"url":       true,
		"urls":      true,
		"uri":       true,
		"uris":      true,
		"endpoint":  true,
		"endpoints": true,
		"base_url":  true,
		"host":      true,
	}

	// Patch text is the only shape the policy can mine for mutated paths; the
	// list is ordered so extracted-path diagnostics stay deterministic.
	policyPatchArgKeyList = []string{"patch", "diff", "patch_text", "patchtext"}
	policyPatchArgKeys    = buildPolicyArgKeySet(policyPatchArgKeyList)

	// policyScalarArgKeys is the union of every argument the policy reads as a
	// plain string. It is the default inspection set; built-in toolkit tools
	// extend it through policyArgKeysForTool.
	policyScalarArgKeys = buildPolicyScalarArgKeys()
)

// policyArgClass tells which policy check must inspect an argument name.
type policyArgClass int

const (
	// policyArgClassPayload marks canonical arguments that carry a payload
	// rather than something the sandbox can verify (file content, a search
	// pattern, an old/new string). Their aliases must never be checked as a
	// path or command.
	policyArgClassPayload policyArgClass = iota
	policyArgClassCommand
	policyArgClassPath
	policyArgClassURL
	policyArgClassPatch
)

// policyToolkitCanonicalArgClasses maps the canonical argument names read by the
// built-in toolkit tools (see toolargs.ToolkitArgAliases) to the check that must
// inspect them. A canonical name missing here is treated as payload, so adding a
// new built-in argument can never silently widen the sandbox surface; the drift
// guard test lists every canonical name that is expected to stay payload.
var policyToolkitCanonicalArgClasses = map[string]policyArgClass{
	"command":   policyArgClassCommand,
	"file_path": policyArgClassPath,
	"workdir":   policyArgClassPath,
	"path":      policyArgClassPath,
	"url":       policyArgClassURL,
	"patch":     policyArgClassPatch,
}

// policyToolArgKeys is the argument-name set inspected for one concrete tool
// call, split by the check that consumes the value.
type policyToolArgKeys struct {
	command   map[string]bool
	path      map[string]bool
	url       map[string]bool
	patch     map[string]bool
	patchList []string
	scalar    map[string]bool
}

var policyDefaultToolArgKeys = policyToolArgKeys{
	command:   policyCommandArgKeys,
	path:      policyPathArgKeys,
	url:       policyURLArgKeys,
	patch:     policyPatchArgKeys,
	patchList: policyPatchArgKeyList,
	scalar:    policyScalarArgKeys,
}

// policyArgKeysForTool returns the inspection set for a tool call.
//
// Built-in toolkit tools add their canonical argument names plus every alias the
// executor promotes before execution, so a call that uses the canonical name
// (file_path) is inspected exactly like a call that uses a legacy alias (path).
// Unknown tools keep the default set: provider-defined MCP arguments are never
// re-interpreted as runtime paths or commands.
func policyArgKeysForTool(toolName string) policyToolArgKeys {
	aliases, ok := toolargs.ToolkitArgAliasesFor(toolName)
	if !ok {
		return policyDefaultToolArgKeys
	}
	keys := policyToolArgKeys{
		command:   clonePolicyArgKeySet(policyCommandArgKeys),
		path:      clonePolicyArgKeySet(policyPathArgKeys),
		url:       clonePolicyArgKeySet(policyURLArgKeys),
		patch:     clonePolicyArgKeySet(policyPatchArgKeys),
		patchList: policyPatchArgKeyList,
	}
	for _, pair := range policyToolkitArgPairs(aliases) {
		class := policyToolkitCanonicalArgClasses[normalizePolicyArgKey(pair.Canonical)]
		if class == policyArgClassPayload {
			continue
		}
		names := make([]string, 0, len(pair.Aliases)+1)
		names = append(names, pair.Canonical)
		names = append(names, pair.Aliases...)
		for _, name := range names {
			key := normalizePolicyArgKey(name)
			if key == "" {
				continue
			}
			switch class {
			case policyArgClassCommand:
				keys.command[key] = true
			case policyArgClassPath:
				keys.path[key] = true
			case policyArgClassURL:
				keys.url[key] = true
			case policyArgClassPatch:
				if keys.patch[key] {
					continue
				}
				keys.patch[key] = true
				keys.patchList = append(keys.patchList, key)
			}
		}
	}
	keys.scalar = buildPolicyScalarArgKeysFrom(keys.command, keys.path, keys.url, keys.patch)
	return keys
}

// policyToolkitArgPairs flattens the top-level and array-item argument pairs of a
// built-in tool in a deterministic order.
func policyToolkitArgPairs(aliases toolargs.ToolkitArgAliases) []toolargs.ToolkitArgAliasPair {
	pairs := make([]toolargs.ToolkitArgAliasPair, 0, len(aliases.Args)+len(aliases.ListFields))
	pairs = append(pairs, aliases.Args...)
	if len(aliases.ListFields) == 0 {
		return pairs
	}
	fields := make([]string, 0, len(aliases.ListFields))
	for field := range aliases.ListFields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		pairs = append(pairs, aliases.ListFields[field]...)
	}
	return pairs
}

func buildPolicyArgKeySet(keys []string) map[string]bool {
	if len(keys) == 0 {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[normalizePolicyArgKey(key)] = true
	}
	return set
}

func buildPolicyScalarArgKeys() map[string]bool {
	return buildPolicyScalarArgKeysFrom(policyCommandArgKeys, policyPathArgKeys, policyURLArgKeys, policyPatchArgKeys)
}

func buildPolicyScalarArgKeysFrom(sources ...map[string]bool) map[string]bool {
	total := 0
	for _, source := range sources {
		total += len(source)
	}
	keys := make(map[string]bool, total)
	for _, source := range sources {
		for key := range source {
			keys[key] = true
		}
	}
	return keys
}

func clonePolicyArgKeySet(source map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(source))
	for key := range source {
		cloned[key] = true
	}
	return cloned
}

func normalizePolicyArgKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// policyArgKindIssues lists policy-relevant arguments whose runtime kind the
// policy cannot inspect. Reporting them keeps sandbox and read-only rules
// fail-closed instead of silently skipping a value the executor still uses.
func policyArgKindIssues(args map[string]interface{}, keys policyToolArgKeys) []string {
	if len(args) == 0 {
		return nil
	}
	const maxReportedIssues = 4
	issues := make([]string, 0, maxReportedIssues)
	report := func(key, detail string) {
		if len(issues) >= maxReportedIssues {
			return
		}
		issues = append(issues, fmt.Sprintf("%q %s", key, detail))
	}

	var walk func(value interface{}, key string)
	walk = func(value interface{}, key string) {
		switch typed := value.(type) {
		case nil, string:
			return
		case map[string]interface{}:
			if keys.patch[key] {
				report(key, fmt.Sprintf("must be a patch/diff string, got %s", policyArgKindName(value)))
				return
			}
			for childKey, child := range typed {
				walk(child, normalizePolicyArgKey(childKey))
			}
		case []interface{}:
			if keys.patch[key] {
				report(key, fmt.Sprintf("must be a patch/diff string, got %s", policyArgKindName(value)))
				return
			}
			for _, item := range typed {
				walk(item, key)
			}
		case []map[string]interface{}:
			if keys.patch[key] {
				report(key, fmt.Sprintf("must be a patch/diff string, got %s", policyArgKindName(value)))
				return
			}
			for _, item := range typed {
				walk(item, key)
			}
		case []string:
			if keys.patch[key] {
				report(key, fmt.Sprintf("must be a patch/diff string, got %s", policyArgKindName(value)))
			}
		default:
			if keys.scalar[key] {
				report(key, fmt.Sprintf("must be a string, got %s", policyArgKindName(typed)))
			}
		}
	}

	// Deterministic diagnostics: the model sees the first issue listed.
	argNames := make([]string, 0, len(args))
	for key := range args {
		argNames = append(argNames, key)
	}
	sort.Strings(argNames)
	for _, key := range argNames {
		walk(args[key], normalizePolicyArgKey(key))
	}
	return issues
}

func policyArgKindName(value interface{}) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case []interface{}, []string, []map[string]interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// collectPathArgs gathers every path the policy must check: explicit path
// arguments plus the paths mined out of patch/diff text. The patch key order is
// the tool's inspection order, so diagnostics stay deterministic.
func collectPathArgs(args map[string]interface{}, keys policyToolArgKeys) []string {
	paths := collectStringArgs(args, keys.path)
	for _, key := range keys.patchList {
		if raw, ok := args[key].(string); ok {
			paths = append(paths, patchutil.ExtractPaths(raw)...)
		}
	}
	return dedupeValues(paths)
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
