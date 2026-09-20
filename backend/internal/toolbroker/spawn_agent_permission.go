package toolbroker

import (
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// Route warnings emitted by the spawn_agent permission inheritance policy.
// They are persisted on the child route context and echoed in the spawn result
// metadata so the parent can explain why a child did not run with the mode it
// asked for instead of silently degrading (or widening) the session policy.
const (
	// SpawnAgentRouteWarningPermissionInherited marks a child that omitted
	// permission_mode and inherited the parent session mode.
	SpawnAgentRouteWarningPermissionInherited = "permission_mode_inherited_from_parent"
	// SpawnAgentRouteWarningPermissionPinned marks a child whose requested
	// mode was pinned back to the parent session mode because the parent mode
	// is session-pinned: bypass_permissions must not silently regain approval
	// prompts, and plan must not delegate writes to children.
	SpawnAgentRouteWarningPermissionPinned = "permission_mode_pinned_to_parent"
	// SpawnAgentRouteWarningPermissionEscalated marks a child that asked for a
	// wider mode than its parent. The request is blocked by default and the
	// child is pinned back to the parent mode (the session tree's ceiling);
	// the warning keeps the attempt auditable. Trusted bounded subtasks can
	// restore the legacy honor-with-warning behavior through
	// SpawnAgentEnvAllowPermissionEscalation.
	SpawnAgentRouteWarningPermissionEscalated = "permission_mode_escalated_from_parent"
)

// SpawnAgentEnvAllowPermissionEscalation is the explicit opt-in that restores
// honoring a child permission_mode wider than the parent session mode (H13).
// Default off: the parent session mode is the ceiling for the whole session
// tree, so a child cannot widen its own authority.
const SpawnAgentEnvAllowPermissionEscalation = "AICLI_AGENTS_ALLOW_PERMISSION_ESCALATION"

// ParentSessionPermissionMode reads the permission mode a spawned child
// inherits from its parent session. The session-level effective mode
// ("permission_mode", maintained by the actor executor and the runtime
// permission-mode API) wins; the mode requested when the session was created
// is the fallback. Unknown values are ignored so a stale context value cannot
// weaken the child policy.
func ParentSessionPermissionMode(session agentcontrol.ContextGetter) string {
	if session == nil {
		return ""
	}
	for _, key := range []string{AgentSessionContextPermissionMode, AgentSessionContextRequestedPermissionMode} {
		if mode := normalizeKnownSpawnAgentPermissionMode(agentcontrol.ContextString(session, key)); mode != "" {
			return mode
		}
	}
	return ""
}

// ResolveSpawnAgentPermissionPolicy reconciles the permission mode a child
// session was asked to run with against the parent session's own mode.
//
// Rules, in order:
//  1. An omitted child mode inherits the parent mode, so a --yolo session
//     keeps a prompt-free sub-tree instead of falling back to the default
//     approval mode.
//  2. plan children are left alone: plan never prompts and never writes, so it
//     is a strict narrowing of every other parent mode.
//  3. A bypass_permissions parent (--yolo) pins children that would ask the
//     user before writes or shell execution (default / accept_edits) back to
//     the parent mode.
//  4. A plan parent pins every other mode back to plan: plan mode is
//     read-only, so it must not delegate writes to a child.
//  5. Any remaining escalation above the parent mode is blocked by default:
//     the parent mode is the ceiling for the session tree, so the child is
//     pinned back to it and the attempt is recorded as a route warning. The
//     legacy honor-with-warning behavior is preserved for trusted bounded
//     subtasks behind the SpawnAgentEnvAllowPermissionEscalation opt-in.
//
// Callers set EffectivePermissionMode after this function returns.
func ResolveSpawnAgentPermissionPolicy(args SpawnAgentArgs, parentPermissionMode string) SpawnAgentArgs {
	parent := normalizeKnownSpawnAgentPermissionMode(parentPermissionMode)
	requested := normalizeKnownSpawnAgentPermissionMode(args.PermissionMode)
	if requested == "" {
		if parent != "" {
			args.PermissionMode = parent
			args.RouteWarnings = appendSpawnAgentRouteWarning(args.RouteWarnings, SpawnAgentRouteWarningPermissionInherited)
		}
		return args
	}
	if parent == "" || requested == parent {
		return args
	}
	if runtimepolicy.Mode(requested) == runtimepolicy.ModePlan {
		// plan is prompt-free and read-only, so it is a strict narrowing of
		// every other parent mode and never violates a session-level pin.
		return args
	}
	if spawnAgentPermissionPinnedToParent(parent, requested) {
		args.PermissionMode = parent
		if strings.TrimSpace(args.RequestedPermissionMode) == "" {
			args.RequestedPermissionMode = requested
		}
		args.RouteWarnings = appendSpawnAgentRouteWarning(args.RouteWarnings, SpawnAgentRouteWarningPermissionPinned)
		return args
	}
	if spawnAgentPermissionRank(requested) > spawnAgentPermissionRank(parent) {
		if strings.TrimSpace(args.RequestedPermissionMode) == "" {
			args.RequestedPermissionMode = requested
		}
		args.RouteWarnings = appendSpawnAgentRouteWarning(args.RouteWarnings, SpawnAgentRouteWarningPermissionEscalated)
		if !spawnAgentPermissionEscalationAllowed() {
			args.PermissionMode = parent
			args.RouteWarnings = appendSpawnAgentRouteWarning(args.RouteWarnings, SpawnAgentRouteWarningPermissionPinned)
		}
	}
	return args
}

// spawnAgentPermissionPinnedToParent reports whether the child request must be
// reduced to the parent session mode instead of being honored.
func spawnAgentPermissionPinnedToParent(parent, requested string) bool {
	switch runtimepolicy.Mode(parent) {
	case runtimepolicy.ModeBypassPermissions:
		return spawnAgentPermissionRequiresApproval(requested)
	case runtimepolicy.ModePlan:
		return true
	}
	return false
}

// spawnAgentPermissionRequiresApproval reports whether a mode stops for a
// user confirmation before writes or shell execution (internal/policy
// modeDecision returns DecisionAsk for these modes).
func spawnAgentPermissionRequiresApproval(mode string) bool {
	switch runtimepolicy.Mode(mode) {
	case runtimepolicy.ModeDefault, runtimepolicy.ModeAcceptEdits:
		return true
	}
	return false
}

// spawnAgentPermissionRank orders modes by how much they delegate to the model
// without user involvement. plan is the strictest (read-only).
func spawnAgentPermissionRank(mode string) int {
	switch runtimepolicy.Mode(mode) {
	case runtimepolicy.ModePlan:
		return 0
	case runtimepolicy.ModeDefault:
		return 1
	case runtimepolicy.ModeAcceptEdits:
		return 2
	case runtimepolicy.ModeBypassPermissions:
		return 3
	}
	return 0
}

// normalizeKnownSpawnAgentPermissionMode drops unsupported values instead of
// failing: it is used on modes recovered from session/run context, where a
// rejection would break every later spawn.
func normalizeKnownSpawnAgentPermissionMode(raw string) string {
	mode, err := normalizeSpawnAgentPermissionMode(raw)
	if err != nil {
		return ""
	}
	return mode
}

func appendSpawnAgentRouteWarning(warnings []string, warning string) []string {
	if strings.TrimSpace(warning) == "" {
		return warnings
	}
	for _, existing := range warnings {
		if strings.TrimSpace(existing) == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

// SpawnAgentPermissionEscalationNextAction returns the actionable guidance for
// a spawn whose child permission escalation was blocked and pinned back to the
// parent session mode. It returns "" when no escalation was blocked (honored
// request, opt-in enabled, or no escalation at all), so callers attach it as
// next_action only for the blocked case.
func SpawnAgentPermissionEscalationNextAction(routeWarnings []string, effectivePermissionMode string) string {
	if !spawnAgentRouteWarningListed(routeWarnings, SpawnAgentRouteWarningPermissionEscalated) ||
		!spawnAgentRouteWarningListed(routeWarnings, SpawnAgentRouteWarningPermissionPinned) {
		return ""
	}
	mode := normalizeKnownSpawnAgentPermissionMode(effectivePermissionMode)
	if mode == "" {
		mode = "the parent session mode"
	}
	return "permission escalation was blocked: the child runs with " + mode +
		" because a child cannot widen the parent session mode. Re-spawn with permission_mode=" + mode +
		" (or omit permission_mode to inherit it), or set " + SpawnAgentEnvAllowPermissionEscalation +
		"=1 to explicitly allow escalation."
}

// spawnAgentPermissionEscalationAllowed reports whether the explicit opt-in is
// enabled. Only an explicit truthy value enables the legacy behavior, so an
// empty or malformed value keeps the fail-closed default.
func spawnAgentPermissionEscalationAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(SpawnAgentEnvAllowPermissionEscalation))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	}
	return false
}

func spawnAgentRouteWarningListed(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == want {
			return true
		}
	}
	return false
}
