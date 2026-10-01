package policy

import (
	"fmt"
	"sort"
	"strings"
)

// NewCapabilityScopedToolExecutionPolicy creates a policy that exposes tools
// only when every capability required by the tool is declared.
func NewCapabilityScopedToolExecutionPolicy(allowedTools []string, capabilities []Capability) *ToolExecutionPolicy {
	policy := NewToolExecutionPolicy(allowedTools, false)
	policy.SetCapabilityScope(capabilities)
	return policy
}

// ReadOnlyChildCapabilities is the minimum capability surface for read-only
// child agents. Write/background stay out of scope and ReadOnly still blocks
// write-like tools and non-readonly shell commands. CapExecShell remains so
// clearly read-only shell commands (git status, rg, ls) can reach the
// argument-level classifier, while control-plane tools remain usable:
// ask_user (plan mode / questions) and agent_management (spawn/collab).
func ReadOnlyChildCapabilities() []Capability {
	return []Capability{
		CapReadOnly,
		CapExecShell,
		CapNetwork,
		CapAskUser,
		CapAgentManagement,
	}
}

// ReadOnlyChildOptionDescription is the single source of truth for the
// read_only spawn option text shown to models. spawn_agent and
// spawn_subagents must serve this same text so the description cannot drift
// from the effective boundary or from each other.
//
// It documents the behavior the runtime actually enforces: write-like tools
// and background tasks are removed from the child's model-visible tool surface
// (and denied at execution), shell stays available but only for individually
// classified read-only commands (compound commands allowed only when every
// segment is on the allow table; redirection / command substitution / dynamic
// expansion are denied — see AssessShellReadOnlyCommand), the delegation
// boundary may remove extra spawn tools, and no approval path can widen the
// boundary.
//
// The dispatch advice tail exists because parents kept picking read_only=true
// for children that then needed general shell syntax mid-run (doc1 §7.10 ②):
// the parent sees the shell contract before dispatch instead of only the
// child-side denial escalation.
const ReadOnlyChildOptionDescription = "Hard child execution boundary. Write-like tools (write, edit, apply_patch, append_write, multiedit, download) and background_task are removed from the child's model-visible tool surface and denied at execution, while shell stays available but only for individually classified read-only commands. The delegation boundary (BlockDelegation / max depth) may remove spawn_agent/spawn_team as well. Independent of permission_mode: approval and bypass_permissions cannot override it. Set read_only=true only when the child must not produce file changes or background jobs; leave it unset for implementer/writer tasks. Read-only children's shell stays allowlisted: redirection, command substitution and dynamic expansion are denied, and compound commands (&&, ||, ;, |) must be read-only in every segment — leave read_only unset for children that need writes or general shell syntax."

func (p *ToolExecutionPolicy) SetCapabilityScope(capabilities []Capability) {
	if p == nil {
		return
	}
	p.CapabilityScopeEnabled = true
	p.AllowedCapabilities = make(map[Capability]bool, len(capabilities))
	for _, capability := range dedupeCapabilities(capabilities) {
		if capability != "" {
			p.AllowedCapabilities[capability] = true
		}
	}
}

func (p *ToolExecutionPolicy) AllowCapabilities(capabilities []Capability) error {
	if p == nil || !p.CapabilityScopeEnabled {
		return nil
	}
	for _, capability := range dedupeCapabilities(capabilities) {
		if !p.AllowedCapabilities[capability] {
			return fmt.Errorf("capability not allowed by execution policy: %s", capability)
		}
	}
	return nil
}

// IntersectAllowedCapabilities returns the capabilities from requested that the
// policy's existing scope already allows. When no capability scope is enabled
// the requested list is returned unchanged (there is nothing to intersect
// with). Hosts use it to seat a derived child boundary (for example the
// read-only child surface) without ever widening an inherited parent scope:
// a read-only child under a parent that never had network/agent-management
// must not gain those capabilities just because the derived boundary lists them.
func (p *ToolExecutionPolicy) IntersectAllowedCapabilities(requested []Capability) []Capability {
	requested = dedupeCapabilities(requested)
	if p == nil || !p.CapabilityScopeEnabled {
		return requested
	}
	filtered := make([]Capability, 0, len(requested))
	for _, capability := range requested {
		if p.AllowedCapabilities[capability] {
			filtered = append(filtered, capability)
		}
	}
	return filtered
}

func (p *ToolExecutionPolicy) AllowedCapabilityNames() []string {
	if p == nil || !p.CapabilityScopeEnabled {
		return nil
	}
	names := make([]string, 0, len(p.AllowedCapabilities))
	for capability, allowed := range p.AllowedCapabilities {
		if allowed {
			names = append(names, string(capability))
		}
	}
	sort.Strings(names)
	return names
}

func (p *ToolExecutionPolicy) resolveCapabilities(req EvalRequest) []Capability {
	if p == nil || !p.CapabilityScopeEnabled {
		return nil
	}
	resolver := p.CapabilityResolver
	if resolver == nil {
		resolver = DefaultCapabilityResolver{}
	}
	return resolver.Resolve(req)
}

// CapabilitiesForTask derives a conservative minimum from task role, tool
// names, mutability, and declared write paths.
func CapabilitiesForTask(role string, readOnly bool, toolNames, writePaths []string) []Capability {
	capabilities := []Capability{CapReadOnly}
	role = strings.ToLower(strings.TrimSpace(role))
	if !readOnly || len(writePaths) > 0 || role == "writer" || role == "implementer" {
		capabilities = append(capabilities, CapWriteFS)
	}
	if role == "lead" || role == "planner" || role == "coordinator" {
		capabilities = append(capabilities, CapAgentManagement)
	}
	resolver := DefaultCapabilityResolver{}
	for _, toolName := range toolNames {
		capabilities = append(capabilities, resolver.Resolve(EvalRequest{ToolName: toolName})...)
	}
	// A task without an explicit tool list inherits the parent's surface, so the
	// capability floor has to follow the role family instead: the vocabulary
	// models actually use (spawn_agent agent_type explore/general/plan,
	// spawn_subagents task_type config/explore/generate/implement/...) is wider
	// than the legacy role names, and an unrecognized role used to fall through
	// to "no defaults" - which silently dropped CapExecShell/CapNetwork from the
	// derived child scope while the child still inherited the parent's tools.
	if len(toolNames) == 0 {
		switch RoleFamilyForTask(role) {
		case RoleFamilyResearch:
			capabilities = append(capabilities, CapExecShell, CapNetwork)
		case RoleFamilyTest, RoleFamilyWrite:
			capabilities = append(capabilities, CapExecShell)
		}
	}
	if readOnly {
		capabilities = filterReadOnlyCapabilities(capabilities)
	}
	return dedupeCapabilities(capabilities)
}

// filterReadOnlyCapabilities drops the capabilities a read-only child must
// never hold: write access and external side effects. CapExecShell is kept so
// declared shell tools can still run individually classified read-only
// commands; the read-only policy flag and the shell classifier enforce the
// command-level boundary. Used both by the role/tool-derived floor
// (CapabilitiesForTask) and by the inherited-parent-scope path
// (DeriveChildForTask) so the two cannot drift apart.
func filterReadOnlyCapabilities(capabilities []Capability) []Capability {
	filtered := make([]Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability != CapWriteFS && capability != CapExternalSideEffect {
			filtered = append(filtered, capability)
		}
	}
	return filtered
}

// RoleFamily classifies a subagent role (or task_type) onto the role families
// the runtime ships defaults for. It is the single alias table shared by
// agent.DefaultToolsForRole and CapabilitiesForTask so the tool list and the
// capability floor cannot drift apart.
type RoleFamily string

const (
	RoleFamilyResearch RoleFamily = "research"
	RoleFamilyTest     RoleFamily = "test"
	RoleFamilyWrite    RoleFamily = "write"
)

// RoleFamilyForTask maps a role/task_type string onto its family. Unknown or
// empty roles return "" (no defaults, no capability widening).
func RoleFamilyForTask(role string) RoleFamily {
	normalized := strings.ToLower(strings.TrimSpace(role))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "researcher", "web-researcher", "explorer", "scout",
		"explore", "understand", "research", "investigate",
		"plan", "planner":
		return RoleFamilyResearch
	case "tester", "test", "verifier", "verification", "verify", "validate":
		return RoleFamilyTest
	case "writer", "implementer", "coder", "developer",
		"implement", "generate", "modify", "refactor", "migrate",
		"integration", "config", "security",
		"general", "worker", "default":
		return RoleFamilyWrite
	default:
		return ""
	}
}

func cloneCapabilityMap(source map[Capability]bool) map[Capability]bool {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[Capability]bool, len(source))
	for capability, allowed := range source {
		clone[capability] = allowed
	}
	return clone
}

func intersectCapabilities(parent map[Capability]bool, requested []Capability) []Capability {
	result := make([]Capability, 0, len(requested))
	for _, capability := range requested {
		if parent[capability] {
			result = append(result, capability)
		}
	}
	return dedupeCapabilities(result)
}
