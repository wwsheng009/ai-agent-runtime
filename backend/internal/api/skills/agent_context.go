package skills

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextpack"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

const agentContextSummaryMaxBytes = 4096

func buildAgentContextMessages(contextValues map[string]interface{}, workspaceCtx *workspace.WorkspaceContext) []types.Message {
	messages := make([]types.Message, 0, 2)
	if workspaceCtx != nil && strings.TrimSpace(workspaceCtx.Summary) != "" {
		messages = append(messages, *types.NewSystemMessage("Workspace context: " + strings.TrimSpace(workspaceCtx.Summary)))
	}
	if summary := buildAgentContextSummary(contextValues); summary != "" {
		messages = append(messages, *types.NewSystemMessage("Runtime context summary:\n" + summary))
	}
	return messages
}

func prependContextMessages(history []types.Message, contextMessages []types.Message) []types.Message {
	if len(contextMessages) == 0 {
		cloned := make([]types.Message, len(history))
		for index := range history {
			cloned[index] = *history[index].Clone()
		}
		return cloned
	}

	merged := make([]types.Message, 0, len(contextMessages)+len(history))
	for _, message := range contextMessages {
		merged = append(merged, *message.Clone())
	}
	for _, message := range history {
		merged = append(merged, *message.Clone())
	}
	return merged
}

func buildAgentContextSummary(contextValues map[string]interface{}) string {
	if len(contextValues) == 0 {
		return ""
	}

	summary := map[string]interface{}{}
	if workspacePath, ok := contextValues["workspace_path"].(string); ok && strings.TrimSpace(workspacePath) != "" {
		summary["workspace_path"] = strings.TrimSpace(workspacePath)
	}
	profileLayer := false
	if pack, ok := contextValues["context_pack"].(map[string]interface{}); ok {
		if reduced := reduceAgentContextPack(pack); len(reduced) > 0 {
			summary["context_pack"] = reduced
		}
		_, profileLayer = pack["profile"].(map[string]interface{})
	}
	if permissions := agentContextStringSlice(contextValues["permissions"]); len(permissions) > 0 {
		summary["permissions"] = permissions
	}

	for key, value := range contextValues {
		if key == "context_pack" || key == "workspace_path" || key == "permissions" {
			continue
		}
		if profileLayer && strings.HasPrefix(key, "profile_") {
			continue
		}
		if agentContextScalar(value) {
			summary[key] = value
		}
	}

	if len(summary) == 0 {
		return ""
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	if len(raw) > agentContextSummaryMaxBytes {
		raw = append(raw[:agentContextSummaryMaxBytes], []byte("...")...)
	}
	return string(raw)
}

func reduceAgentContextPack(pack map[string]interface{}) map[string]interface{} {
	return contextpack.Reduce(pack)
}

func summarizeAgentContextString(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func agentContextStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func limitAgentContextStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func copyAgentContextString(target map[string]interface{}, key string, value interface{}) {
	if target == nil {
		return
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		target[key] = strings.TrimSpace(text)
	}
}

func agentContextScalar(value interface{}) bool {
	switch value.(type) {
	case string, bool, int, int32, int64, float32, float64, uint, uint32, uint64:
		return true
	default:
		return false
	}
}

func agentContextInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

