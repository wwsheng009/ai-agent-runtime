package profile

import "strings"

// WildcardAll matches every name in allow/deny (or use/exclude) declarations.
const WildcardAll = "*"

// Prompt composition modes (PromptsSpec.Mode).
const (
	// PromptModeReplace replaces the host system prompt with the profile prompt.
	// This is the default and preserves pre-profile behavior byte-for-byte.
	PromptModeReplace = "replace"
	// PromptModeAppend appends the profile prompt after the host system prompt.
	PromptModeAppend = "append"
)

// NormalizePromptMode validates and normalizes a prompt mode declaration.
// An empty value resolves to PromptModeReplace; unknown values are rejected.
func NormalizePromptMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return PromptModeReplace, true
	case PromptModeReplace:
		return PromptModeReplace, true
	case PromptModeAppend:
		return PromptModeAppend, true
	default:
		return "", false
	}
}

// MergeSkillSelections merges skill selection layers (low to high priority).
// Same-name collections take the union; deny always wins at evaluation time.
func MergeSkillSelections(specs ...SkillsSpec) ResolvedSkillSelection {
	result := ResolvedSkillSelection{}
	for _, spec := range specs {
		result.Allowlist = appendUniqueStringsFold(result.Allowlist, spec.Allowlist...)
		result.Denylist = appendUniqueStringsFold(result.Denylist, spec.Denylist...)
	}
	return result
}

// MergeMCPSelections merges MCP server selection layers (low to high priority).
// Same-name collections take the union; exclude always wins at evaluation time.
func MergeMCPSelections(specs ...MCPSpec) ResolvedMCPSelection {
	result := ResolvedMCPSelection{}
	for _, spec := range specs {
		result.UseServers = appendUniqueStringsFold(result.UseServers, spec.UseServers...)
		result.ExcludeServers = appendUniqueStringsFold(result.ExcludeServers, spec.ExcludeServers...)
	}
	return result
}

// AllowsSkill reports whether a skill name passes the merged selection:
// deny wins over allow; an empty allowlist means "all names allowed".
func (s ResolvedSkillSelection) AllowsSkill(name string) bool {
	return selectionAllows(s.Allowlist, s.Denylist, name)
}

// AllowsServer reports whether an MCP server name passes the merged selection:
// exclude wins over use; an empty use list means "all servers allowed".
func (s ResolvedMCPSelection) AllowsServer(name string) bool {
	return selectionAllows(s.UseServers, s.ExcludeServers, name)
}

// Empty reports whether no skill selection was declared (profile 无声明路径
// must stay byte-for-byte identical to the pre-profile behavior).
func (s ResolvedSkillSelection) Empty() bool {
	return !hasEffectiveEntry(s.Allowlist) && !hasEffectiveEntry(s.Denylist)
}

// Empty reports whether no MCP server selection was declared.
func (s ResolvedMCPSelection) Empty() bool {
	return !hasEffectiveEntry(s.UseServers) && !hasEffectiveEntry(s.ExcludeServers)
}

func selectionAllows(allowlist []string, denylist []string, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, denied := range denylist {
		if nameMatchesSelection(denied, name) {
			return false
		}
	}
	// Blank declarations are ignored: they must never turn into an implicit
	// "deny everything" allowlist (ValidateProfileSpec reports them as errors).
	hasEffectiveAllow := false
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == "" {
			continue
		}
		hasEffectiveAllow = true
		if nameMatchesSelection(allowed, name) {
			return true
		}
	}
	return !hasEffectiveAllow
}

func hasEffectiveEntry(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func nameMatchesSelection(declared string, name string) bool {
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return false
	}
	if declared == WildcardAll {
		return true
	}
	return strings.EqualFold(declared, name)
}

// appendUniqueStringsFold is the case-insensitive variant used by the Batch 1
// selection declarations. It intentionally does not replace appendUniqueStrings
// (tool policy merging relies on the original case-sensitive semantics).
func appendUniqueStringsFold(existing []string, values ...string) []string {
	if len(values) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		seen[strings.ToLower(trimmed)] = struct{}{}
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, trimmed)
	}
	return existing
}

// MergeToolPolicies merges policy layers from low to high priority.
func MergeToolPolicies(policies ...ToolPolicySpec) ResolvedToolPolicy {
	result := ResolvedToolPolicy{}
	for _, policy := range policies {
		result.Allowlist = appendUniqueStrings(result.Allowlist, policy.Allowlist...)
		result.Denylist = appendUniqueStrings(result.Denylist, policy.Denylist...)
		if policy.ReadOnly != nil {
			value := *policy.ReadOnly
			result.ReadOnly = &value
		}
		if len(policy.Sandbox) > 0 {
			result.Sandbox = mergeSandboxPolicy(result.Sandbox, policy.Sandbox)
		}
	}
	return result
}

func mergeSandboxPolicy(base map[string]interface{}, override map[string]interface{}) map[string]interface{} {
	if len(override) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]interface{}, len(override))
	}
	for key, value := range override {
		if existing, ok := base[key]; ok {
			if merged, ok := mergeSandboxLists(existing, value); ok {
				base[key] = merged
				continue
			}
		}
		base[key] = value
	}
	return base
}

func mergeSandboxLists(existing interface{}, incoming interface{}) ([]string, bool) {
	base, ok := coerceStringSlice(existing)
	if !ok {
		return nil, false
	}
	override, ok := coerceStringSlice(incoming)
	if !ok {
		return nil, false
	}
	return appendUniqueStrings(base, override...), true
}

func coerceStringSlice(value interface{}) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []interface{}:
		if len(typed) == 0 {
			return []string{}, true
		}
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}

func coalesceString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func appendUniqueStrings(existing []string, values ...string) []string {
	if len(values) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		seen[trimmed] = struct{}{}
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		existing = append(existing, trimmed)
	}
	return existing
}
