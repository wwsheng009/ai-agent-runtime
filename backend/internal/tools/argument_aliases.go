package tools

import "strings"

// normalizeToolkitToolArgs accepts common, unambiguous model aliases for the
// built-in tools. External MCP calls keep their provider-defined arguments.
func normalizeToolkitToolArgs(toolName string, args map[string]interface{}) map[string]interface{} {
	normalized := args
	promote := func(canonical string, aliases ...string) {
		normalized = promoteToolArgAlias(normalized, canonical, aliases...)
	}

	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "view":
		promote("file_path", "path", "file", "filename", "filePath")
		normalized = normalizeObjectListAliases(normalized, "files", map[string][]string{
			"file_path": {"path", "file", "filename", "filePath"},
		})
	case "edit":
		promote("file_path", "path", "file", "filename", "filePath")
		promote("old_string", "old_text", "old", "oldString")
		promote("new_string", "new_text", "new", "replacement", "newString")
	case "write", "append_write":
		promote("file_path", "path", "file", "filename", "filePath")
		promote("content", "text", "data")
	case "multiedit":
		promote("file_path", "path", "file", "filename", "filePath")
		normalized = normalizeObjectListAliases(normalized, "edits", map[string][]string{
			"old_string": {"old_text", "old", "oldString"},
			"new_string": {"new_text", "new", "replacement", "newString"},
		})
	case "bash", "execute_shell_command":
		promote("command", "cmd", "script", "shell_command")
		promote("workdir", "cwd", "working_directory")
		normalized = normalizeObjectListAliases(normalized, "commands", map[string][]string{
			"command": {"cmd", "script", "shell_command"},
			"workdir": {"cwd", "working_directory"},
		})
	case "grep":
		promote("pattern", "query", "search", "regex")
		promote("patterns", "queries", "searches")
		promote("path", "root", "directory", "search_path")
	case "glob":
		promote("pattern", "glob", "glob_pattern")
		promote("path", "root", "directory", "search_path")
	case "ls":
		promote("path", "root", "directory")
	case "download":
		promote("url", "uri")
		promote("file_path", "path", "target", "target_path")
	case "apply_patch":
		promote("patch", "diff", "input", "patch_text")
	}
	return normalized
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
