package skills

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func buildRuntimeInstructionMessages(profileState *profileRuntimeState, workspacePath, provider string) []types.Message {
	layers := buildRuntimeInstructionLayers(profileState, workspacePath)
	var compiled []types.Message
	if layers.HasAny() {
		compiled = layers.CompileInstructionMessages(provider)
	} else if profileState != nil && strings.TrimSpace(profileState.PromptText) != "" {
		compiled = []types.Message{*types.NewSystemMessage(strings.TrimSpace(profileState.PromptText))}
	}
	// FR-7 prompt 组合模式：replace（默认）保持既有顺序（profile 组合在前，
	// 内置基础指令追加其后）；append 把内置基础指令提到最前，profile 组合
	// （system→role→tools）与 workspace 指令依次叠加在后。
	if profilePromptModeIsAppend(profileState) {
		return withAppendModeGuidanceOrder(compiled)
	}
	return withDelegationGuidance(compiled)
}

func profilePromptModeIsAppend(profileState *profileRuntimeState) bool {
	if profileState == nil || profileState.Resolved == nil {
		return false
	}
	return runtimeprofileinput.ProfilePromptModeIsAppend(profileState.Resolved.PromptMode)
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

const (
	taskDifficultyGuidanceHeader        = "Task difficulty rating and subagent delegation policy:"
	multiAgentCollaborationGuidanceHead = "Multi-agent collaboration guidance:"
)

// withDelegationGuidance appends the multi-agent instruction blocks that the
// runtime relies on: the delegation policy (difficulty routing) followed by the
// collaboration policy (spawn -> keep working, incremental reads, quota, wait
// discipline). Both blocks are idempotent.
func withDelegationGuidance(messages []types.Message) []types.Message {
	return withMultiAgentCollaborationGuidance(withTaskDifficultyGuidance(messages))
}

// withAppendModeGuidanceOrder 实现 FR-7 append 模式：内置基础指令
// （difficulty + collaboration guidance）置于最前，profile 组合
// （system→role→tools）与 workspace 指令依次叠加在后。
//
// 首条 system 片段与内置基础合并为同一条 system 消息（消息结构不变，仅顺序
// 调整），其余片段（developer/user 层）保持原有相对顺序。
func withAppendModeGuidanceOrder(compiled []types.Message) []types.Message {
	base := withDelegationGuidance(nil)
	if len(base) == 0 {
		return cloneInstructionMessages(compiled)
	}
	if len(compiled) == 0 {
		return base
	}
	first := *compiled[0].Clone()
	if !strings.EqualFold(strings.TrimSpace(first.Role), "system") {
		merged := append([]types.Message{}, base...)
		return append(merged, cloneInstructionMessages(compiled)...)
	}
	merged := base[0]
	if content := strings.TrimSpace(first.Content); content != "" {
		merged.Content = strings.TrimSpace(merged.Content) + "\n\n" + content
	}
	if len(first.Metadata) > 0 {
		merged.Metadata = first.Metadata
	}
	result := make([]types.Message, 0, len(base)+len(compiled))
	result = append(result, merged)
	result = append(result, cloneInstructionMessages(compiled[1:])...)
	return result
}

func withTaskDifficultyGuidance(messages []types.Message) []types.Message {
	return withInstructionGuidance(messages, taskDifficultyGuidanceHeader, runtimeprompt.RenderTaskDifficultyGuidance())
}

func withMultiAgentCollaborationGuidance(messages []types.Message) []types.Message {
	return withInstructionGuidance(messages, multiAgentCollaborationGuidanceHead, runtimeprompt.RenderMultiAgentCollaborationGuidance())
}

// withInstructionGuidance appends one guidance block to the primary system
// instruction message, creating one when the profile has none. A message that
// already carries the block header is left untouched so repeated composition
// (handler + subagent paths) never duplicates the text.
func withInstructionGuidance(messages []types.Message, header, rawGuidance string) []types.Message {
	guidance := strings.TrimSpace(rawGuidance)
	if guidance == "" {
		return cloneInstructionMessages(messages)
	}
	cloned := cloneInstructionMessages(messages)
	for index := range cloned {
		if !strings.EqualFold(strings.TrimSpace(cloned[index].Role), "system") {
			continue
		}
		content := strings.TrimSpace(cloned[index].Content)
		if strings.Contains(content, header) {
			return cloned
		}
		if content == "" {
			cloned[index].Content = guidance
		} else {
			cloned[index].Content = content + "\n\n" + guidance
		}
		return cloned
	}
	guidanceMessage := *types.NewSystemMessage(guidance)
	return append([]types.Message{guidanceMessage}, cloned...)
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
