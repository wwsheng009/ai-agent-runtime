package commands

import runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"

func splitChatCommandFields(argument string) []string {
	return runtimeexecutor.SplitCommandTokens(argument)
}
