package policy

import "strings"

// Rule describes a static policy rule.
type Rule struct {
	Name         string
	Tools        []string
	Capabilities []Capability
	Decision     DecisionType
	Reason       string
}

// Matches returns true if the rule applies to the evaluation request.
func (r Rule) Matches(req EvalRequest) bool {
	if len(r.Tools) > 0 && !stringSliceContains(r.Tools, req.ToolName) {
		return false
	}
	if len(r.Capabilities) > 0 && !capabilitiesMatch(r.Capabilities, req.Capabilities) {
		return false
	}
	return true
}

func stringSliceContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func capabilitiesMatch(ruleCaps []Capability, caps []Capability) bool {
	if len(ruleCaps) == 0 {
		return true
	}
	seen := make(map[Capability]bool, len(caps))
	for _, cap := range caps {
		seen[cap] = true
	}
	for _, cap := range ruleCaps {
		if !seen[cap] {
			return false
		}
	}
	return true
}
