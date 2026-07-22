package toolresult

import (
	"fmt"
	"strings"
)

// MutationSummary returns a non-empty success description when tool metadata
// proves that a filesystem mutation was attempted or replayed.
func MutationSummary(metadata map[string]interface{}) string {
	metadata = mutationMetadata(metadata)
	if len(metadata) == 0 {
		return ""
	}

	rawPaths, hasMutationPaths := metadata["mutated_paths"]
	paths := mutationPaths(rawPaths)
	if len(paths) > 0 {
		return fmt.Sprintf(
			"Tool completed successfully; changed %s: %s.",
			formatMutationCount(len(paths), "file", "files"),
			formatMutationPaths(paths),
		)
	}

	if replay, _ := metadata["idempotent_replay"].(bool); replay {
		if path := mutationString(metadata["file_path"]); path != "" {
			return "Tool completed successfully; no file changes were needed: " + path + "."
		}
		return "Tool completed successfully; no file changes were needed."
	}

	if files, ok := mutationInt(metadata["files"]); ok && files > 0 {
		return fmt.Sprintf("Tool completed successfully; changed %s.", formatMutationCount(files, "file", "files"))
	}

	if hasMutationPaths {
		if path := mutationString(metadata["file_path"]); path != "" {
			return "Tool completed successfully; no file changes were reported: " + path + "."
		}
		return "Tool completed successfully; no file changes were reported."
	}

	return ""
}

func mutationMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok && len(nested) > 0 {
		return nested
	}
	return metadata
}

func mutationPaths(value interface{}) []string {
	var values []interface{}
	switch typed := value.(type) {
	case []string:
		paths := make([]string, 0, len(typed))
		for _, path := range typed {
			if path = strings.TrimSpace(path); path != "" {
				paths = append(paths, path)
			}
		}
		return paths
	case []interface{}:
		values = typed
	case string:
		if path := strings.TrimSpace(typed); path != "" {
			return []string{path}
		}
		return nil
	default:
		return nil
	}

	paths := make([]string, 0, len(values))
	for _, value := range values {
		if path := mutationString(value); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func mutationString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func mutationInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func formatMutationCount(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func formatMutationPaths(paths []string) string {
	const visiblePathLimit = 3
	if len(paths) <= visiblePathLimit {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(paths[:visiblePathLimit], ", "), len(paths)-visiblePathLimit)
}
