package hooks

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

func matchesHook(cfg HookConfig, payload map[string]interface{}) bool {
	if len(cfg.Match.Tools) > 0 {
		toolName := stringPayload(payload, "tool_name")
		if !stringInSlice(cfg.Match.Tools, toolName) {
			return false
		}
	}
	if len(cfg.Match.PathGlobs) > 0 {
		paths := payloadPaths(payload)
		if len(paths) == 0 {
			if args, ok := payload["args"].(map[string]interface{}); ok {
				paths = append(paths, extractPathsFromArgs(args)...)
			}
		}
		if !matchesAnyGlob(cfg.Match.PathGlobs, paths) {
			return false
		}
	}
	if len(cfg.Match.CommandGlobs) > 0 {
		commands := payloadCommands(payload)
		if len(commands) == 0 {
			if args, ok := payload["args"].(map[string]interface{}); ok {
				commands = extractCommandsFromArgs(args)
			}
		}
		if !matchesAnyGlob(cfg.Match.CommandGlobs, commands) {
			return false
		}
	}
	return true
}

func stringPayload(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func payloadPaths(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	var paths []string
	if value, ok := payload["path"].(string); ok && strings.TrimSpace(value) != "" {
		paths = append(paths, strings.TrimSpace(value))
	}
	if raw, ok := payload["paths"]; ok {
		switch list := raw.(type) {
		case []string:
			for _, item := range list {
				if strings.TrimSpace(item) != "" {
					paths = append(paths, strings.TrimSpace(item))
				}
			}
		case []interface{}:
			for _, item := range list {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					paths = append(paths, strings.TrimSpace(text))
				}
			}
		}
	}
	if len(paths) == 0 {
		if raw, ok := payload["tool_metadata"].(map[string]interface{}); ok {
			paths = append(paths, extractPathsFromArgs(raw)...)
		}
	}
	if len(paths) == 0 {
		if raw, ok := payload["metadata"].(map[string]interface{}); ok {
			paths = append(paths, extractPathsFromArgs(raw)...)
		}
	}
	return paths
}

func payloadCommands(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	commands := make([]string, 0, 1)
	if value, ok := payload["command"].(string); ok && strings.TrimSpace(value) != "" {
		commands = append(commands, strings.TrimSpace(value))
	}
	if value, ok := payload["cmd"].(string); ok && strings.TrimSpace(value) != "" {
		commands = append(commands, strings.TrimSpace(value))
	}
	return commands
}

func extractCommandsFromArgs(args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	keys := []string{"command", "cmd", "executable", "program"}
	commands := make([]string, 0, 1)
	for _, key := range keys {
		if value, ok := args[key]; ok {
			commands = append(commands, extractPathsFromValue(value)...)
		}
	}
	return dedupeStrings(commands)
}

func extractPathsFromArgs(args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	paths := make([]string, 0, 2)
	singleKeys := []string{"path", "file", "file_path", "source", "destination", "target", "workspace_path", "cwd", "workdir", "working_dir"}
	for _, key := range singleKeys {
		if value, ok := args[key]; ok {
			paths = append(paths, extractPathsFromValue(value)...)
		}
	}
	listKeys := []string{"paths", "files", "file_paths", "targets", "sources", "destinations", "mutated_paths", "mutated_files", "changed_paths", "changed_files"}
	for _, key := range listKeys {
		if value, ok := args[key]; ok {
			paths = append(paths, extractPathsFromValue(value)...)
		}
	}
	if value, ok := args["files"]; ok {
		switch items := value.(type) {
		case []interface{}:
			for _, item := range items {
				if entry, ok := item.(map[string]interface{}); ok {
					paths = append(paths, extractPathsFromValue(entry["path"])...)
					paths = append(paths, extractPathsFromValue(entry["file_path"])...)
				}
			}
		case []map[string]interface{}:
			for _, entry := range items {
				paths = append(paths, extractPathsFromValue(entry["path"])...)
				paths = append(paths, extractPathsFromValue(entry["file_path"])...)
			}
		}
	}
	if raw, ok := args["patch"]; ok {
		if patch, ok := raw.(string); ok {
			paths = append(paths, extractPathsFromPatch(patch)...)
		}
	}
	if raw, ok := args["diff"]; ok {
		if patch, ok := raw.(string); ok {
			paths = append(paths, extractPathsFromPatch(patch)...)
		}
	}
	return dedupeStrings(paths)
}

func extractPathsFromValue(value interface{}) []string {
	out := make([]string, 0)
	switch item := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	case []string:
		for _, text := range item {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []interface{}:
		for _, raw := range item {
			out = append(out, extractPathsFromValue(raw)...)
		}
	}
	return out
}

func extractPathsFromPatch(patch string) []string {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil
	}
	lines := strings.Split(patch, "\n")
	paths := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				path := strings.TrimPrefix(parts[3], "b/")
				path = strings.TrimPrefix(path, "a/")
				if path != "" && path != "/dev/null" {
					paths = append(paths, path)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			path = strings.TrimPrefix(path, "a/")
			if path != "" && path != "/dev/null" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func matchesAnyGlob(globs []string, values []string) bool {
	if len(globs) == 0 {
		return true
	}
	if len(values) == 0 {
		return false
	}
	for _, glob := range globs {
		glob = strings.TrimSpace(glob)
		if glob == "" {
			continue
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			if matchGlob(glob, value) {
				return true
			}
		}
	}
	return false
}

func matchGlob(glob, value string) bool {
	if glob == "" || value == "" {
		return false
	}
	if strings.Contains(glob, "**") {
		return matchDoubleStar(glob, value)
	}
	if match, _ := filepath.Match(glob, value); match {
		return true
	}
	globNorm := normalizeGlobPath(glob)
	valueNorm := normalizeGlobPath(value)
	match, _ := path.Match(globNorm, valueNorm)
	return match
}

func matchDoubleStar(glob, value string) bool {
	globNorm := normalizeGlobPath(glob)
	valueNorm := normalizeGlobPath(value)
	re, err := globToRegex(globNorm)
	if err != nil {
		return false
	}
	return re.MatchString(valueNorm)
}

func normalizeGlobPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	return strings.ReplaceAll(value, "\\", "/")
}

func globToRegex(glob string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(glob); i++ {
		ch := glob[i]
		switch ch {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				for i+1 < len(glob) && glob[i+1] == '*' {
					i++
				}
				builder.WriteString(".*")
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				builder.WriteString("\\[")
				continue
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			class = strings.ReplaceAll(class, "\\", "\\\\")
			builder.WriteString("[")
			builder.WriteString(class)
			builder.WriteString("]")
			i += end + 1
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '\\':
			builder.WriteByte('\\')
			builder.WriteByte(ch)
		default:
			builder.WriteByte(ch)
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

func stringInSlice(values []string, target string) bool {
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
