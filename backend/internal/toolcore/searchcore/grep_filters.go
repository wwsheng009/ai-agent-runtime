package searchcore

import "strings"

// grepGlobFilter is a compiled include/exclude filter list. Empty lists are
// represented by a nil filter so "not configured" and "nothing matched" stay
// distinguishable.
type grepGlobFilter struct {
	matchers []*GlobMatcher
}

func newGrepGlobFilter(patterns []string) (*grepGlobFilter, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	filter := &grepGlobFilter{matchers: make([]*GlobMatcher, 0, len(patterns))}
	for _, pattern := range patterns {
		matcher, err := NewGlobMatcher(pattern)
		if err != nil {
			return nil, err
		}
		filter.matchers = append(filter.matchers, matcher)
	}
	return filter, nil
}

func (f *grepGlobFilter) matches(candidate string) bool {
	if f == nil {
		return false
	}
	for _, matcher := range f.matchers {
		if matcher.Match(candidate) {
			return true
		}
	}
	return false
}

// shouldFilterGrepCandidate applies the shared include/exclude rule to one
// candidate path. Include is an allow-list (empty = everything); exclude wins
// over include. Both match through MatchGlobPath, so a pattern without a slash
// matches the basename anywhere in the tree exactly like it does in ripgrep.
// Filters arrive compiled: a walk compiles them once, not once per file.
func shouldFilterGrepCandidate(candidate string, include, exclude *grepGlobFilter) bool {
	if include != nil && !include.matches(candidate) {
		return true
	}
	return exclude.matches(candidate)
}

// SplitGrepGlobList parses the scalar spelling of a file filter list: a bare
// string is split on top-level commas, so shell brace forms such as
// *.{go,ts} survive intact and stay one pattern for the matcher. Entries are
// trimmed, blanks dropped and duplicates collapsed, mirroring the local tool's
// resolveStringListParam/normalizeGlobPatterns chain.
func SplitGrepGlobList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := make([]string, 0, 2)
	start, depth := 0, 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, value[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, value[start:])

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		pattern := strings.TrimSpace(part)
		if pattern == "" {
			continue
		}
		seen := false
		for _, existing := range out {
			if existing == pattern {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, pattern)
		}
	}
	return out
}

// GrepGlobListFromArgs reads an include/exclude argument in either accepted
// spelling: a comma-separated string or an array of strings.
func GrepGlobListFromArgs(args map[string]any, key string) []string {
	if len(args) == 0 {
		return nil
	}
	switch value := args[key].(type) {
	case string:
		return SplitGrepGlobList(value)
	case []string:
		return SplitGrepGlobList(strings.Join(value, ","))
	case []interface{}:
		items := make([]string, 0, len(value))
		for _, raw := range value {
			item, _ := raw.(string)
			items = append(items, item)
		}
		return SplitGrepGlobList(strings.Join(items, ","))
	default:
		return nil
	}
}
