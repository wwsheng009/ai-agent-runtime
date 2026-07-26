package agentdef

import (
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// SyntheticTeamTeammateName is the AgentControl fallback when a teammate has no
// portable profile. It must not be treated as an agentdef name.
// AgentControl stores "team_teammate"; normalizeAgentName maps "_" → "-", so
// both forms are treated as synthetic.
const SyntheticTeamTeammateName = "team_teammate"

// IsPortableAgentName reports whether name can resolve to a portable agentdef.
// Empty names and the synthetic team_teammate fallback are not portable.
func IsPortableAgentName(name string) bool {
	raw := strings.ToLower(strings.TrimSpace(name))
	if raw == "" {
		return false
	}
	if raw == SyntheticTeamTeammateName || raw == "team-teammate" {
		return false
	}
	// normalizeAgentName lowercases and rewrites "_" to "-".
	if normalizeAgentName(name) == "team-teammate" {
		return false
	}
	return true
}

// ResolvePortableBinding resolves profile/agent_type to a runtime Binding.
// Empty or synthetic names return (nil, nil). Missing definitions return an error
// from Resolve (callers may treat resolve failure as "no defaults").
func ResolvePortableBinding(name string, opts DiscoverOptions) (*Binding, error) {
	if !IsPortableAgentName(name) {
		return nil, nil
	}
	def, err := Resolve(name, opts)
	if err != nil || def == nil {
		return nil, err
	}
	return BuildBinding(def)
}

// SessionDefaults are ambient session context fields projected from a portable
// agent definition for teammate / child sessions.
type SessionDefaults struct {
	PermissionMode string
	ReadOnly       bool
	HasReadOnly    bool
	Provider       string
	Model          string
}

// PortableSessionDefaults resolves profile to session-facing defaults.
// ok is false when the name is non-portable or resolution fails.
func PortableSessionDefaults(profile string, opts DiscoverOptions) (SessionDefaults, bool) {
	binding, err := ResolvePortableBinding(profile, opts)
	if err != nil || binding == nil {
		return SessionDefaults{}, false
	}
	defaults := SessionDefaults{
		PermissionMode: EffectivePermissionMode(binding),
		Provider:       strings.TrimSpace(binding.Provider),
		Model:          strings.TrimSpace(binding.Model),
	}
	if binding.ReadOnly != nil {
		defaults.HasReadOnly = true
		defaults.ReadOnly = *binding.ReadOnly
	}
	return defaults, true
}

// EffectivePermissionMode returns the runtime permission mode for a binding.
// Read-only sandbox agents default to plan when permissionMode is empty.
func EffectivePermissionMode(binding *Binding) string {
	if binding == nil {
		return ""
	}
	mode := strings.TrimSpace(string(binding.PermissionMode))
	if mode != "" {
		return mode
	}
	if binding.ReadOnly != nil && *binding.ReadOnly {
		return string(runtimepolicy.ModePlan)
	}
	return ""
}

// TeammatePermissionMode resolves a teammate profile to a permission mode.
// Returns empty when the profile is non-portable or does not resolve; callers
// should keep their existing default (typically bypass_permissions for team
// workers without a portable role).
func TeammatePermissionMode(profile string, opts DiscoverOptions) string {
	defaults, ok := PortableSessionDefaults(profile, opts)
	if !ok {
		return ""
	}
	return defaults.PermissionMode
}
