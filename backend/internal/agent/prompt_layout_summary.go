package agent

import (
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func summarizePromptLayoutForEvent(messages []types.Message, tokenCountFunc func(string) int) runtimeprompt.InstructionMessagesSummary {
	return runtimeprompt.SummarizeInstructionMessagesWithTokens(messages, tokenCountFunc)
}
