package skills

import (
	"fmt"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func buildMissingPathMessage(label, path string) string {
	label = strings.TrimSpace(label)
	path = strings.TrimSpace(path)
	if label == "" || path == "" {
		return ""
	}

	if suffix := buildMissingPathSuggestionSuffix(path); suffix != "" {
		return fmt.Sprintf("%s: %s%s", label, path, suffix)
	}
	return fmt.Sprintf("%s: %s", label, path)
}

func buildMissingPathSuggestionSuffix(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	detail := runtimeexecutor.ResolveUpwardPathDetail(path)
	if strings.TrimSpace(detail.Resolved) != "" || len(detail.Candidates) == 0 {
		return ""
	}
	return fmt.Sprintf("，可能的候选路径: %s", strings.Join(detail.Candidates, ", "))
}
