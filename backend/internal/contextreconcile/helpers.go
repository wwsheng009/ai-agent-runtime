package contextreconcile

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func cloneMessages(messages []types.Message) []types.Message {
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func replacementIndex(messages []types.Message) (string, map[string]bool) {
	parts := make([]string, 0, len(messages))
	goalIDs := make(map[string]bool)
	for _, message := range messages {
		parts = append(parts, strings.ToLower(strings.TrimSpace(message.Content)))
		if value, ok := message.Metadata["goal_id"].(string); ok {
			goalIDs[strings.TrimSpace(value)] = true
		}
	}
	return strings.Join(parts, "\n"), goalIDs
}

func replacementHasStage(messages []types.Message, stage string) bool {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Metadata.GetString("context_stage", "")), stage) {
			return true
		}
	}
	return false
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func dedupeCorrections(values []Correction) []Correction {
	seen := make(map[string]bool, len(values))
	out := make([]Correction, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value.Code) + "\x00" + strings.TrimSpace(value.Detail)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
