package commands

import (
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatRuntimeContextTokenCount = sessionmeta.LegacyAICLITokenCount
const chatRuntimeContextInputTokenCount = sessionmeta.LegacyAICLIInputTokenCount
const chatRuntimeContextOutputTokenCount = sessionmeta.LegacyAICLIOutputTokenCount
const chatRuntimeContextContextTokenCount = sessionmeta.LegacyAICLIContextTokenCount
const chatRuntimeContextContextWindowTokenCount = sessionmeta.LegacyAICLIContextWindowCount
const chatRuntimeContextTurnContextTokenCount = sessionmeta.LegacyAICLITurnContextCount

func applyChatTokenUsage(session *ChatSession, usage *runtimetypes.TokenUsage) {
	if session == nil || usage == nil {
		return
	}
	inputTokens := usage.PromptTokens
	outputTokens := usage.CompletionTokens
	totalTokens := usage.TotalTokens
	if totalTokens <= 0 {
		totalTokens = inputTokens + outputTokens
	}
	if totalTokens <= 0 && inputTokens <= 0 && outputTokens <= 0 {
		return
	}
	if totalTokens > 0 {
		session.TokenCount += totalTokens
	}
	if inputTokens > 0 {
		session.InputTokenCount += inputTokens
	}
	if outputTokens > 0 {
		session.OutputTokenCount += outputTokens
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func resetChatTurnTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	session.TurnContextTokenCount = 0
}

func resetChatConversationTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	resetChatContextTokenUsage(session)
	session.TokenCount = 0
	session.InputTokenCount = 0
	session.OutputTokenCount = 0
}

func resetChatContextTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	session.ContextTokenCount = 0
	session.ContextWindowTokenCount = 0
	session.TurnContextTokenCount = 0
	session.providerContextTokenCount = 0
	session.providerContextWindowTokenCount = 0
}

// applyChatContextTokens updates the active context snapshot used by /status.
// Codex-aligned: overwrite last-turn context rather than keeping a monotonic high-water mark.
func applyChatContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	applyChatContextTokensLocked(session, promptTokens, windowTokens, forceRefresh, false, false)
}

// applyChatContextTokensReset is retained for call-site clarity after
// compact/recovery. Last-turn overwrite is already the default (monotonic=false),
// so this is intentionally identical to applyChatContextTokens.
func applyChatContextTokensReset(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	applyChatContextTokens(session, promptTokens, windowTokens, forceRefresh)
}

func applyChatContextTokensLocked(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool, providerReported bool, monotonic bool) {
	if session == nil {
		return
	}
	changed := false
	if !providerReported {
		session.providerContextTokenCount = 0
		session.providerContextWindowTokenCount = 0
	}
	// When monotonic is false (default Codex last-turn semantics), always overwrite
	// the displayed active context with the latest provider/local snapshot.
	// Monotonic remains available for rare restore paths that still need high-water behavior.
	if promptTokens > 0 && (!monotonic || session.ContextTokenCount <= 0 || promptTokens > session.ContextTokenCount) {
		session.ContextTokenCount = promptTokens
		changed = true
	}
	if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
		session.ContextWindowTokenCount = windowTokens
		changed = true
	}
	if (changed || forceRefresh) && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

// applyChatContextTokensFromUsage records the last-turn provider usage as the
// active context window occupancy (Codex last_token_usage.total_tokens).
// Cumulative billing remains on session.TokenCount / logger totals separately.
func applyChatContextTokensFromUsage(session *ChatSession, usage *runtimetypes.TokenUsage, windowTokens int, forceRefresh bool) int {
	if session == nil || usage == nil {
		return 0
	}
	contextTokens := chatContextTokensFromProviderUsage(session, usage)
	if contextTokens <= 0 {
		return 0
	}
	session.providerContextTokenCount = contextTokens
	session.providerContextWindowTokenCount = windowTokens
	// Codex: last_token_usage is overwritten each turn, never max()-clamped upward.
	applyChatContextTokensLocked(session, contextTokens, windowTokens, forceRefresh, true, false)
	return contextTokens
}

func chatContextTokensFromProviderUsage(session *ChatSession, usage *runtimetypes.TokenUsage) int {
	if usage == nil {
		return 0
	}
	if usage.TotalTokens > 0 {
		contextTokens := usage.TotalTokens
		if chatProviderUsageReportsCacheOutsidePrompt(session, usage) {
			expanded := usage.PromptTokens + usage.CompletionTokens + usage.CachedTokens
			if expanded > contextTokens {
				contextTokens = expanded
			}
		}
		return contextTokens
	}
	contextTokens := usage.PromptTokens + usage.CompletionTokens
	if chatProviderUsageReportsCacheOutsidePrompt(session, usage) {
		contextTokens += usage.CachedTokens
	}
	if contextTokens > 0 {
		return contextTokens
	}
	return 0
}

func chatProviderUsageReportsCacheOutsidePrompt(session *ChatSession, usage *runtimetypes.TokenUsage) bool {
	if session == nil || usage == nil || usage.CachedTokens <= 0 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(session.Provider.GetProtocol()), "anthropic") {
		return false
	}
	// Anthropic-style usage reports cache read/creation input separately from
	// input_tokens, so active context must include it even when total_tokens is
	// missing or provider-normalized too low.
	return true
}

func applyChatContextTokensFromMessages(session *ChatSession, messages []runtimetypes.Message, windowTokens int, forceRefresh bool) int {
	promptTokens := countChatContextTokensForMessages(session, messages)
	if promptTokens > 0 {
		applyChatContextTokens(session, promptTokens, windowTokens, forceRefresh)
		return promptTokens
	}
	if len(messages) == 0 {
		resetChatContextTokenUsage(session)
		if forceRefresh && session != nil && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}
	return 0
}

func refreshChatContextTokenSnapshotFromMessages(session *ChatSession, windowTokens int, forceRefresh bool) int {
	if session == nil {
		return 0
	}
	if !chatMessagesHaveConversation(session.Messages) {
		resetChatContextTokenUsage(session)
		if forceRefresh && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		return 0
	}
	if windowTokens <= 0 {
		windowTokens = session.ContextWindowTokenCount
	}
	if windowTokens <= 0 {
		budget := resolveSharedChatPromptBudget(session)
		windowTokens = budget.ModelCapabilityMaxContextTokens
		if windowTokens <= 0 {
			windowTokens = budget.ProviderContextLimit
		}
		if windowTokens <= 0 {
			windowTokens = budget.ActiveTurnMaxTokens
		}
	}
	return applyChatContextTokensFromMessages(session, session.Messages, windowTokens, forceRefresh)
}

func resolveChatContextSnapshotTokens(session *ChatSession, fallbackMessages []runtimetypes.Message) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	messages := fallbackMessages
	if len(messages) == 0 && len(session.Messages) > 0 {
		messages = session.Messages
	}
	if len(messages) == 0 && session.RuntimeSession != nil && len(session.RuntimeSession.History) > 0 {
		messages = session.RuntimeSession.History
	}
	if !chatMessagesHaveConversation(messages) {
		return 0
	}
	return countChatContextTokensForMessages(session, messages)
}

func resolveChatObservedTokenUsage(session *ChatSession, fallbackMessages []runtimetypes.Message) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	return resolveChatContextSnapshotTokens(session, fallbackMessages)
}

func applyChatTurnContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	if session == nil {
		return
	}
	changed := false
	if promptTokens > 0 {
		session.TurnContextTokenCount += promptTokens
	}
	if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
		session.ContextWindowTokenCount = windowTokens
		changed = true
	}
	if (changed || forceRefresh) && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func applyChatTurnContextTokensFromMessages(session *ChatSession, messages []runtimetypes.Message, forceRefresh bool) int {
	promptTokens := countChatContextTokensForMessages(session, messages)
	applyChatTurnContextTokens(session, promptTokens, 0, forceRefresh)
	return promptTokens
}

func countChatContextTokensForMessages(session *ChatSession, messages []runtimetypes.Message) int {
	if len(messages) == 0 {
		return 0
	}
	llmRuntime, err := buildSharedChatAutoCompactRuntime(session)
	if err == nil && llmRuntime != nil {
		if count := llmRuntime.CountMessagesTokens(messages); count > 0 {
			return count
		}
	}
	return countSharedChatMessagesTokensForSession(session, messages)
}

func restoreChatTokenCount(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil {
		return
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextTokenCount); ok {
		session.TokenCount = count
	} else {
		session.TokenCount = 0
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextInputTokenCount); ok {
		session.InputTokenCount = count
	} else {
		session.InputTokenCount = 0
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextOutputTokenCount); ok {
		session.OutputTokenCount = count
	} else {
		session.OutputTokenCount = 0
	}
}

func restoreChatContextTokenUsage(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil {
		return
	}
	resetChatContextTokenUsage(session)
	if count, _, ok := runtimeSessionAnchoredContextInt(runtimeSession); ok {
		session.ContextTokenCount = count
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextContextWindowTokenCount); ok {
		session.ContextWindowTokenCount = count
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextTurnContextTokenCount); ok {
		session.TurnContextTokenCount = count
	}
}

func runtimeSessionContextInt(session *runtimechat.Session, key string) (int, bool) {
	if session == nil || session.Metadata.Context == nil {
		return 0, false
	}
	return sessionmeta.Int(session.Metadata.Context, key)
}

// runtimeSessionContextRawInt 逐字读取单个 context 键，不做 canonical/legacy
// 别名解析：sessionmeta.Int 按 CandidateKeys 以 canonical 优先返回值，当
// canonical 残留陈旧小值而 legacy 是实时锚点时会把锚点遮蔽掉（2026-10-12
// 状态栏 “Context 0%” 事件）。需要读取锚点原值的调用方使用本函数。
func runtimeSessionContextRawInt(session *runtimechat.Session, key string) (int, bool) {
	if session == nil {
		return 0, false
	}
	return contextRawIntValue(session.Metadata.Context, key)
}

// contextRawIntValue 逐字读取单个 context 键并做数值转换（不做候选键别名解析）。
func contextRawIntValue(ctx map[string]interface{}, key string) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	value, ok := ctx[key]
	if !ok || value == nil {
		return 0, false
	}
	// 单键包装后走 sessionmeta.Int 的数值转换，避免复制其类型分支。
	return sessionmeta.Int(map[string]interface{}{key: value}, key)
}

// 上下文 used 值（状态栏/恢复路径）的解析来源，用于调试归因与 web 快照暴露。
const (
	chatContextUsedSourceAnchorCanonical = "anchor_canonical"
	chatContextUsedSourceAnchorLegacy    = "anchor_legacy"
	chatContextUsedSourceSnapshot        = "snapshot"
	chatContextUsedSourceEstimate        = "estimate"
)

// runtimeSessionAnchoredContextInt 解析 runtime session 上的活跃上下文锚点并返回来源：
//  1. 结构化锚点（prompt 身份 fingerprint 存在）→ 逐字读取 legacy 锚点键，绕开
//     canonical 优先顺序，防止陈旧 canonical 遮蔽实时锚点（2026-10-12 修复的读取端
//     纵深防御）；
//  2. 无结构化锚点时保持历史顺序：canonical → legacy。
func runtimeSessionAnchoredContextInt(session *runtimechat.Session) (int, string, bool) {
	if session == nil {
		return 0, "", false
	}
	return runtimeContextAnchoredInt(session.Metadata.Context)
}

// runtimeContextAnchoredInt 是 runtimeSessionAnchoredContextInt 的 map 形态核心：
// 调用方负责保证 ctx 的并发访问已被 ChatSession.runtimeContextMapMu 保护（或 ctx 是
// 私有快照）。
func runtimeContextAnchoredInt(ctx map[string]interface{}) (int, string, bool) {
	if ctx == nil {
		return 0, "", false
	}
	if _, structured := ctx[runtimechat.RuntimeContextAnchorFingerprintKey]; structured {
		if anchor, ok := contextRawIntValue(ctx, chatRuntimeContextContextTokenCount); ok && anchor > 0 {
			return anchor, chatContextUsedSourceAnchorLegacy, true
		}
	}
	if value, ok := contextRawIntValue(ctx, sessionmeta.ContextTokenCount); ok && value > 0 {
		return value, chatContextUsedSourceAnchorCanonical, true
	}
	if value, ok := contextRawIntValue(ctx, chatRuntimeContextContextTokenCount); ok && value > 0 {
		return value, chatContextUsedSourceAnchorLegacy, true
	}
	return 0, "", false
}

// isChatProviderReportedUsageSource 判断事件 usage_source 是否属于 provider
// 上报类（与 loop 锚点准入条件一致，估算值不得写回锚点）。
func isChatProviderReportedUsageSource(source string) bool {
	return strings.EqualFold(strings.TrimSpace(source), "provider_reported")
}

// updateChatRuntimeSessionContextAnchor 把 provider 确认的输入总量写回 CLI 侧
// runtime session 副本的锚点双键（legacy + canonical）。actor 在自己的会话对象上
// 按次写锚点，而 CLI 副本此前只在 resume/compact 刷新——长回合中状态栏 tier-1
// 因此读到冻结旧值（2026-10-12 现场：请求结束后 used 不更新，resume 后正常）。
func updateChatRuntimeSessionContextAnchor(session *ChatSession, inputTokens int) {
	if session == nil || inputTokens <= 0 {
		return
	}
	// 本函数由 bridge worker 在流式回合中调用，与会话属主 / actor 的 context 写
	// 并发；必须走共享 map 锁，否则就是 2026-10-16 fatal 的写方。
	withRuntimeContextWrite(session, func(ctx map[string]interface{}) {
		ctx[chatRuntimeContextContextTokenCount] = inputTokens
		ctx[sessionmeta.ContextTokenCount] = inputTokens
	})
}
