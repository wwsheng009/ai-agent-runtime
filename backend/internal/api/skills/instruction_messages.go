package skills

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func buildRuntimeInstructionMessages(profileState *profileRuntimeState, workspacePath, provider string) []types.Message {
	layers := buildRuntimeInstructionLayers(profileState, workspacePath)
	if layers.HasAny() {
		return layers.CompileInstructionMessages(provider)
	}
	if profileState != nil && strings.TrimSpace(profileState.PromptText) != "" {
		return []types.Message{*types.NewSystemMessage(strings.TrimSpace(profileState.PromptText))}
	}
	return nil
}

func buildRuntimeInstructionLayers(profileState *profileRuntimeState, workspacePath string) *runtimeprompt.Layers {
	layers := runtimeprompt.NewLayers()
	if profileState != nil && profileState.PromptLayers != nil {
		layers.Append(profileState.PromptLayers)
	}
	if workspaceLayers, err := runtimeprompt.LoadWorkspaceInstructions(workspacePath, 0); err == nil && workspaceLayers != nil {
		layers.Append(workspaceLayers)
	}
	return layers
}

func ensureSessionInstructionMessages(session *chat.Session, instructions []types.Message) bool {
	if session == nil || len(instructions) == 0 {
		return false
	}
	history := session.GetMessages()
	leading := countLeadingInstructionMessages(history)
	existing := history[:leading]
	if instructionMessagesEqual(existing, instructions) {
		return false
	}

	updated := cloneInstructionMessages(instructions)
	for _, message := range history[leading:] {
		updated = append(updated, *message.Clone())
	}
	session.ReplaceHistory(updated)
	return true
}

func injectInstructionMessages(messages []types.Message, instructions []types.Message) []types.Message {
	if len(instructions) == 0 {
		return messages
	}
	leading := countLeadingInstructionMessages(messages)
	existing := messages[:leading]
	if instructionMessagesEqual(existing, instructions) {
		return messages
	}

	merged := cloneInstructionMessages(instructions)
	for _, message := range messages[leading:] {
		merged = append(merged, *message.Clone())
	}
	return merged
}

func countLeadingInstructionMessages(messages []types.Message) int {
	count := 0
	for _, message := range messages {
		if !isInstructionMessageRole(message.Role) {
			break
		}
		count++
	}
	return count
}

func isInstructionMessageRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	default:
		return false
	}
}

func instructionMessagesEqual(left, right []types.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(strings.TrimSpace(left[index].Role), strings.TrimSpace(right[index].Role)) {
			return false
		}
		if strings.TrimSpace(left[index].Content) != strings.TrimSpace(right[index].Content) {
			return false
		}
		if promptLayerMetadata(left[index].Metadata) != promptLayerMetadata(right[index].Metadata) {
			return false
		}
	}
	return true
}

func cloneInstructionMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		cloned = append(cloned, *message.Clone())
	}
	return cloned
}

func primarySystemInstructionContent(messages []types.Message) string {
	for _, message := range messages {
		if !isInstructionMessageRole(message.Role) {
			break
		}
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			return strings.TrimSpace(message.Content)
		}
	}
	return ""
}

func promptLayerMetadata(metadata types.Metadata) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata["prompt_layer"].(string)
	return strings.TrimSpace(value)
}
