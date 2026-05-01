package chatcore

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/historyguard"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ProviderTurnRequest describes the normalized provider request for one assistant turn.
type ProviderTurnRequest struct {
	Messages  []types.Message        `json:"messages,omitempty"`
	Tools     []types.ToolDefinition `json:"tools,omitempty"`
	Stream    bool                   `json:"stream,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	EventSink func(ChatEvent)        `json:"-"`
}

// ProviderTurnResponse describes the normalized provider response for one assistant turn.
type ProviderTurnResponse struct {
	Message *types.Message    `json:"message,omitempty"`
	Usage   *types.TokenUsage `json:"usage,omitempty"`
}

// ProviderTurnExecutor is the minimal provider surface required by the shared replay loop.
type ProviderTurnExecutor interface {
	Complete(ctx context.Context, req ProviderTurnRequest) (*ProviderTurnResponse, error)
}

// ToolExecutor is the minimal tool surface required by the shared replay loop.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, call types.ToolCall) ToolResult
}

// HistoryCompactor optionally replaces oversized prompt history with a more
// compact continuation-friendly history before the next provider request.
type HistoryCompactor func(ctx context.Context, history []types.Message) ([]types.Message, bool, error)

// ToolResult captures the normalized output of one tool invocation.
type ToolResult struct {
	Content  string                 `json:"content,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ToolLoopRequest describes the prepared inputs required to replay provider tool calls.
type ToolLoopRequest struct {
	Prompt                               string
	ExplicitImagePaths                   []string
	ImageArtifactDir                     string // session-local directory for persisting image copies
	History                              []types.Message
	ActiveTurnMaxBytes                   int
	ActiveTurnMaxTokens                  int
	CountTokens                          func([]types.Message) int
	PromptBudgetSource                   string
	PromptBudgetDetail                   string
	ResolvedProvider                     string
	ResolvedModel                        string
	ModelCapabilityMaxContextTokens      int
	ModelCapabilityAutoCompactRatio      float64
	ModelCapabilityAutoCompactTokenLimit int
	HistoryCompactor                     HistoryCompactor
	Metadata                             map[string]interface{}
	Provider                             ProviderTurnExecutor
	Tools                                []types.ToolDefinition
	ToolExecutor                         ToolExecutor
	EventSink                            func(ChatEvent)
	Stream                               bool
}

// ToolLoopResult returns the final assistant response plus the replayed history.
type ToolLoopResult struct {
	Response *ChatResult
	History  []types.Message
}

// ExecuteToolLoop replays provider tool calls until the provider returns a final assistant message.
func ExecuteToolLoop(ctx context.Context, req ToolLoopRequest) (*ToolLoopResult, error) {
	if req.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}

	history := cloneMessages(req.History)
	if req.Prompt != "" {
		msg, err := llm.NewUserPromptMessageWithImages(req.Prompt, req.ExplicitImagePaths)
		if err != nil {
			return nil, fmt.Errorf("resolving image attachments: %w", err)
		}
		if msg != nil {
			if req.ImageArtifactDir != "" {
				if persistErr := llm.PersistLocalInputImages(msg, req.ImageArtifactDir); persistErr != nil {
					emitChatEvent(req.EventSink, ChatEvent{
						Type:    EventWarning,
						Content: fmt.Sprintf("image persistence: %v", persistErr),
					})
				}
			}
			history = append(history, *msg)
		}
		for _, warning := range llm.ValidateLocalInputImagePaths(req.ExplicitImagePaths) {
			emitChatEvent(req.EventSink, ChatEvent{
				Type:    EventWarning,
				Content: warning,
			})
		}
	}

	response := NewChatResult()

	for {
		compactedHistory, preflightErr := enforceToolLoopPromptPreflight(ctx, history, req)
		if preflightErr != nil {
			return nil, preflightErr
		}
		history = compactedHistory

		turn, err := req.Provider.Complete(ctx, ProviderTurnRequest{
			Messages:  cloneMessages(history),
			Tools:     cloneToolDefinitions(req.Tools),
			Stream:    req.Stream,
			Metadata:  cloneInterfaceMap(req.Metadata),
			EventSink: req.EventSink,
		})
		if err != nil {
			return nil, err
		}
		if turn == nil || turn.Message == nil {
			return nil, fmt.Errorf("provider returned no message")
		}

		assistantMessage := cloneMessage(turn.Message)
		history = append(history, *assistantMessage)

		if len(assistantMessage.ToolCalls) == 0 {
			response.Output = assistantMessage.Content
			response.Usage = cloneUsage(turn.Usage)
			return &ToolLoopResult{
				Response: response,
				History:  cloneMessages(history),
			}, nil
		}
		if req.ToolExecutor == nil {
			return nil, fmt.Errorf("tool executor is required when provider requests tools")
		}

		emitChatEvent(req.EventSink, ChatEvent{
			Type:  EventTool,
			Stage: "batch_start",
			Metadata: map[string]interface{}{
				"call_count": len(assistantMessage.ToolCalls),
			},
		})

		successCount := 0
		errorCount := 0
		for _, call := range assistantMessage.ToolCalls {
			emitChatEvent(req.EventSink, ChatEvent{
				Type:       EventTool,
				Stage:      "tool_requested",
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneInterfaceMap(call.Args),
				Metadata:   cloneInterfaceMap(toolDefinitionMetadata(req.Tools, call.Name)),
			})

			toolResult := req.ToolExecutor.ExecuteTool(ctx, call)
			execution := ToolExecutionSummary{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Output:     toolResult.Content,
				Error:      toolResult.Error,
				Metadata:   cloneInterfaceMap(toolResult.Metadata),
				Success:    toolResult.Error == "",
			}
			response.ToolExecutions = append(response.ToolExecutions, execution)
			if execution.Success {
				successCount++
			} else {
				errorCount++
			}

			toolMessage := types.NewToolMessage(call.ID, renderToolMessage(toolResult))
			if len(toolResult.Metadata) > 0 {
				toolMessage.Metadata = types.NewMetadata()
				for key, value := range toolResult.Metadata {
					toolMessage.Metadata[key] = cloneInterfaceValue(value)
				}
			}
			history = append(history, *toolMessage)
			if compacted, changed := compactToolLoopActiveTurnReplay(history, req); changed {
				history = compacted
			}

			emitChatEvent(req.EventSink, ChatEvent{
				Type:       EventTool,
				Stage:      "tool_result",
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneInterfaceMap(call.Args),
				Output:     execution.Output,
				Error:      execution.Error,
				Success:    execution.Success,
				Metadata:   cloneInterfaceMap(toolResult.Metadata),
			})
		}

		emitChatEvent(req.EventSink, ChatEvent{
			Type:  EventTool,
			Stage: "batch_end",
			Metadata: map[string]interface{}{
				"success_count": successCount,
				"error_count":   errorCount,
			},
		})
	}
}

func enforceToolLoopPromptPreflight(ctx context.Context, history []types.Message, req ToolLoopRequest) ([]types.Message, error) {
	if req.CountTokens == nil || req.ActiveTurnMaxTokens <= 0 {
		return history, nil
	}

	promptTokensBefore := req.CountTokens(history)
	if promptTokensBefore <= req.ActiveTurnMaxTokens {
		return history, nil
	}

	alreadyCompacted := historyguard.HasActiveTurnCompactionSummary(history)
	currentHistory := history
	currentTokens := promptTokensBefore
	activeTurnCompacted := false
	if compacted, changed := compactToolLoopActiveTurnReplay(history, req); changed {
		currentHistory = compacted
		currentTokens = req.CountTokens(compacted)
		activeTurnCompacted = true
		if currentTokens <= req.ActiveTurnMaxTokens {
			return currentHistory, nil
		}
	} else if alreadyCompacted {
		activeTurnCompacted = true
	}

	if req.HistoryCompactor != nil {
		if compacted, changed, err := req.HistoryCompactor(ctx, currentHistory); err == nil && changed && len(compacted) > 0 {
			currentHistory = compacted
			currentTokens = req.CountTokens(compacted)
			if currentTokens <= req.ActiveTurnMaxTokens {
				return currentHistory, nil
			}
			return currentHistory, newToolLoopPromptPreflightError(
				req,
				"prompt_still_exceeds_budget_after_compaction",
				currentTokens,
				activeTurnCompacted,
				currentHistory,
				currentHistory,
			)
		}
	}

	if activeTurnCompacted {
		return currentHistory, newToolLoopPromptPreflightError(
			req,
			"prompt_still_exceeds_budget_after_compaction",
			currentTokens,
			true,
			currentHistory,
			currentHistory,
		)
	}

	return history, newToolLoopPromptPreflightError(req, "active_turn_not_compactable", promptTokensBefore, false, history, nil)
}

func compactToolLoopActiveTurnReplay(history []types.Message, req ToolLoopRequest) ([]types.Message, bool) {
	maxBytes := req.ActiveTurnMaxBytes
	if maxBytes <= 0 {
		maxBytes = historyguard.DefaultActiveTurnReplayMaxBytes
	}
	if req.CountTokens != nil && req.ActiveTurnMaxTokens > 0 {
		return historyguard.CompactActiveTurnReplayWithCounter(
			history,
			maxBytes,
			req.ActiveTurnMaxTokens,
			req.CountTokens,
		)
	}
	return historyguard.CompactActiveTurnReplay(history, maxBytes)
}

func newToolLoopPromptPreflightError(req ToolLoopRequest, code string, promptTokens int, activeTurnCompacted bool, messages []types.Message, replacementHistory []types.Message) error {
	reason := "prompt exceeds budget before send"
	detail := fmt.Sprintf("prompt tokens %d exceed budget %d before the provider request is sent", promptTokens, req.ActiveTurnMaxTokens)
	suggestedAction := "请减少 prompt 尺寸或降低上下文保留。"

	switch code {
	case "active_turn_not_compactable":
		reason = "active-turn replay cannot be compacted further"
		detail = fmt.Sprintf("prompt tokens %d exceed budget %d, and no earlier replay block remains available for compaction", promptTokens, req.ActiveTurnMaxTokens)
		suggestedAction = "请减少更早历史、提高 prompt 预算，或开启新的用户轮次。"
	case "prompt_still_exceeds_budget_after_compaction":
		if activeTurnCompacted {
			reason = "prompt budget still exceeded after active-turn compaction"
			detail = fmt.Sprintf("prompt tokens %d still exceed budget %d after compacting older replay in the current turn", promptTokens, req.ActiveTurnMaxTokens)
		} else {
			reason = "prompt budget still exceeded after history compaction"
			detail = fmt.Sprintf("prompt tokens %d still exceed budget %d after compacting the conversation history before the provider request is sent", promptTokens, req.ActiveTurnMaxTokens)
		}
		suggestedAction = "请继续收缩上下文层、提高预算，或从新的轮次继续。"
	}

	return &agent.PromptPreflightError{
		PromptTokens:                         promptTokens,
		PromptBudget:                         req.ActiveTurnMaxTokens,
		BudgetSource:                         req.PromptBudgetSource,
		BudgetSourceDetail:                   req.PromptBudgetDetail,
		ResolvedProvider:                     req.ResolvedProvider,
		ResolvedModel:                        req.ResolvedModel,
		ModelCapabilityMaxContextTokens:      req.ModelCapabilityMaxContextTokens,
		ModelCapabilityAutoCompactRatio:      req.ModelCapabilityAutoCompactRatio,
		ModelCapabilityAutoCompactTokenLimit: req.ModelCapabilityAutoCompactTokenLimit,
		Code:                                 code,
		Reason:                               reason,
		Detail:                               detail,
		SuggestedAction:                      suggestedAction,
		CanRetryAfterCompaction:              false,
		ActiveTurnCompacted:                  activeTurnCompacted,
		ActiveTurnMessageCount:               activeTurnMessageCount(messages),
		LatestReplayBlockMessageCount:        latestReplayBlockMessageCount(messages),
		ReplacementHistory:                   cloneMessages(replacementHistory),
	}
}

func activeTurnMessageCount(messages []types.Message) int {
	userIndex := toolLoopActiveUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages) {
		return 0
	}
	return len(messages) - userIndex
}

func latestReplayBlockMessageCount(messages []types.Message) int {
	userIndex := toolLoopActiveUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return 0
	}
	replayStart := toolLoopLatestReplayBlockStart(messages, userIndex)
	if replayStart <= userIndex || replayStart >= len(messages) {
		return 0
	}
	return len(messages) - replayStart
}

func toolLoopActiveUserTurnStart(messages []types.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index
		}
	}
	return -1
}

func toolLoopLatestReplayBlockStart(messages []types.Message, userIndex int) int {
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return len(messages)
	}

	index := len(messages) - 1
	for index > userIndex && messages[index].Role == "tool" {
		index--
	}
	if index <= userIndex {
		return userIndex + 1
	}
	if messages[index].Role == "assistant" && len(messages[index].ToolCalls) > 0 {
		return index
	}
	return index
}

func emitChatEvent(sink func(ChatEvent), event ChatEvent) {
	if sink != nil {
		sink(event)
	}
}

func toolDefinitionMetadata(defs []types.ToolDefinition, toolName string) map[string]interface{} {
	for _, def := range defs {
		if def.Name == toolName {
			return def.Metadata
		}
	}
	return nil
}

func renderToolMessage(result ToolResult) string {
	var envelope *output.Envelope
	if len(result.Metadata) > 0 {
		envelope = &output.Envelope{
			Metadata: cloneInterfaceMap(result.Metadata),
		}
	}
	return output.RenderToolResultContentForModel(result.Content, result.Error, envelope)
}

func cloneMessage(message *types.Message) *types.Message {
	if message == nil {
		return nil
	}

	cloned := &types.Message{
		Role:       message.Role,
		Content:    message.Content,
		ToolCallID: message.ToolCallID,
		Metadata:   message.Metadata.Clone(),
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]types.ToolCall, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			cloned.ToolCalls[index] = types.ToolCall{
				ID:   call.ID,
				Name: call.Name,
				Args: cloneInterfaceMap(call.Args),
			}
		}
	}

	return cloned
}

func cloneMessages(input []types.Message) []types.Message {
	if len(input) == 0 {
		return nil
	}

	cloned := make([]types.Message, len(input))
	for index := range input {
		cloned[index] = *cloneMessage(&input[index])
	}

	return cloned
}

func cloneToolDefinitions(input []types.ToolDefinition) []types.ToolDefinition {
	if len(input) == 0 {
		return nil
	}

	cloned := make([]types.ToolDefinition, len(input))
	for index, tool := range input {
		cloned[index] = types.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneInterfaceMap(tool.Parameters),
			Metadata:    cloneInterfaceMap(tool.Metadata),
		}
	}

	return cloned
}

func cloneInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}

	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneInterfaceValue(value)
	}

	return cloned
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneInterfaceValue(item)
		}
		return cloned
	default:
		return typed
	}
}

func cloneUsage(input *types.TokenUsage) *types.TokenUsage {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}
