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
// classified read-only commands, the delegation boundary may remove extra
// spawn tools, and no approval path can widen the boundary.
const ReadOnlyChildOptionDescription = "Hard child execution boundary. Write-like tools (write, edit, apply_patch, append_write, multiedit, download) and background_task are removed from the child's model-visible tool surface and denied at execution, while shell stays available but only for individually classified read-only commands. The delegation boundary (BlockDelegation / max depth) may remove spawn_agent/spawn_team as well. Independent of permission_mode: approval and bypass_permissions cannot override it. Set read_only=true only when the child must not produce file changes or background jobs; leave it unset for implementer/writer tasks."

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
	if readOnly {
		filtered := capabilities[:0]
		for _, capability := range capabilities {
			// CapExecShell is kept under read-only so declared shell tools can
			// still run read-only commands; CapWriteFS / external side effects
			// remain out of scope regardless of declared tool names.
			if capability != CapWriteFS && capability != CapExternalSideEffect {
				filtered = append(filtered, capability)
			}
		}
		capabilities = filtered
	}
	return dedupeCapabilities(capabilities)
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
