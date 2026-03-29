package profile

import "strings"

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
