package tools

import (
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
)

// normalizeToolkitToolArgs accepts common, unambiguous model aliases for the
// built-in tools. External MCP calls keep their provider-defined arguments.
//
// The alias table itself lives in internal/toolargs so the runtime policy can
// inspect exactly the same argument names this executor reads (see
// toolargs.ToolkitArgAliases); promotion order within a pair is preserved.
func normalizeToolkitToolArgs(toolName string, args map[string]interface{}) map[string]interface{} {
	aliases, ok := toolargs.ToolkitArgAliasesFor(toolName)
	if !ok {
		return args
	}
	normalized := args
	for _, pair := range aliases.Args {
		normalized = promoteToolArgAlias(normalized, pair.Canonical, pair.Aliases...)
	}
	for _, field := range sortedAliasListFields(aliases) {
		candidates := make(map[string][]string, len(aliases.ListFields[field]))
		for _, pair := range aliases.ListFields[field] {
			candidates[pair.Canonical] = pair.Aliases
		}
		normalized = normalizeObjectListAliases(normalized, field, candidates)
	}
	return normalized
}

// sortedAliasListFields keeps promotion deterministic when a tool declares more
// than one array-of-objects argument.
func sortedAliasListFields(aliases toolargs.ToolkitArgAliases) []string {
	if len(aliases.ListFields) == 0 {
		return nil
	}
	fields := make([]string, 0, len(aliases.ListFields))
	for field := range aliases.ListFields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func promoteToolArgAlias(args map[string]interface{}, canonical string, aliases ...string) map[string]interface{} {
	if len(args) == 0 || strings.TrimSpace(canonical) == "" {
		return args
	}
	if _, exists := args[canonical]; exists {
		return args
	}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		value, exists := args[alias]
		if alias == "" || !exists {
			continue
		}
		cloned := cloneToolArgs(args)
		cloned[canonical] = value
		delete(cloned, alias)
		return cloned
	}
	return args
}

func normalizeObjectListAliases(args map[string]interface{}, field string, aliases map[string][]string) map[string]interface{} {
	raw, exists := args[field]
	if !exists || raw == nil {
		return args
	}
	values, restore := objectListValues(raw)
	if restore == nil {
		return args
	}
	changed := false
	normalized := make([]interface{}, len(values))
	for index, value := range values {
		item, itemOK := value.(map[string]interface{})
		if !itemOK {
			normalized[index] = value
			continue
		}
		updated := item
		for canonical, candidates := range aliases {
			updated = promoteToolArgAlias(updated, canonical, candidates...)
		}
		normalized[index] = updated
		changed = changed || !sameToolArgsMap(updated, item)
	}
	if !changed {
		return args
	}
	cloned := cloneToolArgs(args)
	cloned[field] = restore(normalized)
	return cloned
}

func objectListValues(raw interface{}) ([]interface{}, func([]interface{}) interface{}) {
	switch values := raw.(type) {
	case []interface{}:
		return values, func(normalized []interface{}) interface{} { return normalized }
	case []map[string]interface{}:
		generic := make([]interface{}, len(values))
		for index, value := range values {
			generic[index] = value
		}
		return generic, func(normalized []interface{}) interface{} {
			typed := make([]map[string]interface{}, len(normalized))
			for index, value := range normalized {
				typed[index], _ = value.(map[string]interface{})
			}
			return typed
		}
	default:
		return nil, nil
	}
}

func cloneToolArgs(args map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(args)+1)
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

func sameToolArgsMap(left, right map[string]interface{}) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, exists := right[key]; !exists {
			return false
		}
	}
	return true
}
