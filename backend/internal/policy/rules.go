package policy

import "strings"

// Rule describes a static policy rule.
type Rule struct {
	Name  string
	Tools []string
	// Specifiers holds the parsed `Tool(specifier)` / tool-glob entries of the
	// rule; Tools keeps the plain names with their legacy exact-match
	// semantics. A rule matches when any entry matches (OR).
	Specifiers   []ToolSpecifier
	Capabilities []Capability
	Decision     DecisionType
	Reason       string
}

// Matches returns true if the rule applies to the evaluation request. It is the
// context-free form used by callers without a workspace root; the engine uses
// MatchesRequest so path anchors resolve against the session workspace.
func (r Rule) Matches(req EvalRequest) bool {
	matched, _ := r.matchesTools("", req)
	return matched
}

// MatchesRequest reports whether the rule applies and returns a short detail
// for the matching specifier (e.g. the matched segment/path) for diagnostics.
func (r Rule) MatchesRequest(root string, req EvalRequest) (bool, string) {
	matched, detail := r.matchesTools(root, req)
	if !matched {
		return false, ""
	}
	if len(r.Capabilities) > 0 && !capabilitiesMatch(r.Capabilities, req.Capabilities) {
		return false, ""
	}
	return true, detail
}

func (r Rule) matchesTools(root string, req EvalRequest) (bool, string) {
	if len(r.Tools) == 0 && len(r.Specifiers) == 0 {
		return true, ""
	}
	if len(r.Tools) > 0 && stringSliceContains(r.Tools, req.ToolName) {
		return true, "tool=" + strings.TrimSpace(req.ToolName)
	}
	for _, spec := range r.Specifiers {
		if matched, detail := spec.MatchesTool(root, req, r.Decision); matched {
			return true, detail
		}
	}
	return false, ""
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
