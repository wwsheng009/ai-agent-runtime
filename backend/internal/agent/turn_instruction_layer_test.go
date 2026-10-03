package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P1 §4.5/§4.12：composeInitialHistory 的装配契约。
// 关键断言：session-scope 指令落在 leading 连续 system 前缀之后、持久历史之前；
// turn-scope 片段保持尾部顺序；历史增长时 catalog 偏移与前缀逐字节稳定；
// 输入不被原地修改；无 session 指令时与旧装配逐字节等价。

func turnInstructionTestMessage(t *testing.T, scope, source, body string) types.Message {
	t.Helper()
	message := prompt.NewInstructionMessage(scope, source, body)
	require.NotNil(t, message)
	return *message
}

func turnInstructionTestContents(messages []types.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Role+":"+message.Content)
	}
	return contents
}

func TestComposeInitialHistoryPlacesSessionInstructionsAfterLeadingSystemPrefix(t *testing.T) {
	catalog := turnInstructionTestMessage(t, types.InstructionScopeSession, types.InstructionSourceSkillsCatalog, "## Skills\ncatalog-body")
	guide := *types.NewSystemMessage("turn-guide")
	mention := turnInstructionTestMessage(t, types.InstructionScopeTurn, types.InstructionSourceSkillInstructions, "mention-body")
	history := []types.Message{*types.NewUserMessage("hi"), *types.NewAssistantMessage("hello")}

	composed := composeInitialHistory(history, []types.Message{catalog, guide, mention}, "  config-system  ")

	require.Equal(t, []string{
		"system:config-system",
		"system:## Skills\ncatalog-body",
		"user:hi",
		"assistant:hello",
		"system:turn-guide",
		"system:mention-body",
	}, turnInstructionTestContents(composed))
	require.Equal(t, types.InstructionScopeSession, types.InstructionScopeOf(composed[1]))
	require.Equal(t, types.InstructionSourceSkillsCatalog, types.InstructionSourceOf(composed[1]))
	require.Equal(t, types.InstructionScopeTurn, types.InstructionScopeOf(composed[5]))
}

func TestComposeInitialHistoryExtractsSessionInstructionsFromPersistedHistory(t *testing.T) {
	catalog := turnInstructionTestMessage(t, types.InstructionScopeSession, types.InstructionSourceSkillsCatalog, "catalog-body")
	history := []types.Message{*types.NewUserMessage("hi"), catalog, *types.NewAssistantMessage("ok")}

	composed := composeInitialHistory(history, nil, "sys")

	require.Equal(t, []string{"system:sys", "system:catalog-body", "user:hi", "assistant:ok"}, turnInstructionTestContents(composed))
}

func TestComposeInitialHistoryCatalogOffsetAndPrefixStayStableAsHistoryGrows(t *testing.T) {
	catalog := turnInstructionTestMessage(t, types.InstructionScopeSession, types.InstructionSourceSkillsCatalog, "catalog-body")
	first := composeInitialHistory([]types.Message{
		*types.NewUserMessage("u1"),
		*types.NewAssistantMessage("a1"),
	}, []types.Message{catalog}, "sys")
	require.Equal(t, []string{"system:sys", "system:catalog-body", "user:u1", "assistant:a1"}, turnInstructionTestContents(first))

	grown := []types.Message{
		*types.NewUserMessage("u1"),
		*types.NewAssistantMessage("a1"),
		*types.NewUserMessage("u2"),
		*types.NewAssistantMessage("a2"),
	}
	second := composeInitialHistory(grown, []types.Message{catalog}, "sys")
	require.Equal(t, []string{"system:sys", "system:catalog-body", "user:u1", "assistant:a1", "user:u2", "assistant:a2"}, turnInstructionTestContents(second))
	// catalog 仍固定在 index 1，稳定前缀（config → catalog）逐字节不变。
	require.Equal(t, promptMessageFingerprint(first[:2]), promptMessageFingerprint(second[:2]))

	// 同一输入重复构建必须逐字节稳定（fingerprint 不变不重排）。
	again := composeInitialHistory(grown, []types.Message{catalog}, "sys")
	require.Equal(t, promptMessageFingerprint(second), promptMessageFingerprint(again))
}

func TestComposeInitialHistoryDoesNotMutateInputs(t *testing.T) {
	catalog := turnInstructionTestMessage(t, types.InstructionScopeSession, types.InstructionSourceSkillsCatalog, "catalog-body")
	history := []types.Message{*types.NewUserMessage("hi")}
	turnMessages := []types.Message{catalog, *types.NewSystemMessage("turn-guide")}
	historyBefore := promptMessageFingerprint(history)
	turnBefore := promptMessageFingerprint(turnMessages)

	composed := composeInitialHistory(history, turnMessages, "sys")
	require.Equal(t, historyBefore, promptMessageFingerprint(history))
	require.Equal(t, turnBefore, promptMessageFingerprint(turnMessages))

	// 返回值不得与输入共享底层消息：改写结果不能回流到 catalog 输入。
	require.Equal(t, "catalog-body", composed[1].Content)
	composed[1].Content = "mutated"
	require.Equal(t, "catalog-body", turnMessages[0].Content)
	require.Equal(t, "catalog-body", catalog.Content)
}

func TestComposeInitialHistoryWithoutSessionInstructionsMatchesLegacyAssembly(t *testing.T) {
	history := []types.Message{*types.NewUserMessage("hi"), *types.NewAssistantMessage("ok")}
	turnMessages := []types.Message{*types.NewSystemMessage("turn-guide"), *types.NewSystemMessage("turn-routing")}

	got := composeInitialHistory(history, turnMessages, "sys")

	legacyBase := cloneMessageHistory(history)
	legacyBase = append(legacyBase, cloneMessages(turnMessages)...)
	legacy := mergeConfiguredSystemPrompt(legacyBase, "sys")
	require.Equal(t, promptMessageFingerprint(legacy), promptMessageFingerprint(got))
}

func TestComposeInitialHistoryWithoutLeadingSystemInsertsAtZero(t *testing.T) {
	catalog := turnInstructionTestMessage(t, types.InstructionScopeSession, types.InstructionSourceSkillsCatalog, "catalog-body")
	composed := composeInitialHistory([]types.Message{*types.NewUserMessage("hi")}, []types.Message{catalog}, "")
	require.Equal(t, []string{"system:catalog-body", "user:hi"}, turnInstructionTestContents(composed))
}
