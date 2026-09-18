package toolbroker

import "strings"

// planPathArgs normalizes the two plan-mode path inputs into a primary plan
// artifact path plus the additional writable paths:
//
//   - plan_path accepts a string or an array of strings. When an array is given,
//     the first entry is the primary plan artifact and the remaining entries
//     join the write allowlist.
//   - plan_write_paths is an explicit list of additional writable plan paths.
//
// Entries are trimmed; empty entries and duplicates (including duplicates of
// the primary path) are dropped. The primary path stays the artifact surfaced
// in tool results and plan reviews; the additional paths only widen the
// plan-mode write allowlist enforced by the permission engine.
func planPathArgs(args map[string]interface{}) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	items := planPathItems(args["plan_path"])
	var primary string
	if len(items) > 0 {
		primary = items[0]
		items = items[1:]
	}
	items = append(items, planPathItems(args["plan_write_paths"])...)
	return primary, dedupePlanExtras(primary, items)
}

// planPathItems normalizes a path-or-list argument (string | []string |
// []interface{} of strings) into trimmed non-empty path strings.
func planPathItems(value interface{}) []string {
	var out []string
	appendValue := func(raw string) {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	switch typed := value.(type) {
	case string:
		appendValue(typed)
	case []string:
		for _, item := range typed {
			appendValue(item)
		}
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				appendValue(text)
			}
		}
	}
	return out
}

// dedupePlanExtras drops empty/duplicate extra paths (case-insensitively, and
// never repeating the primary path).
func dedupePlanExtras(primary string, extras []string) []string {
	if len(extras) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	if trimmed := strings.TrimSpace(primary); trimmed != "" {
		seen[strings.ToLower(trimmed)] = struct{}{}
	}
	out := make([]string, 0, len(extras))
	for _, extra := range extras {
		trimmed := strings.TrimSpace(extra)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
