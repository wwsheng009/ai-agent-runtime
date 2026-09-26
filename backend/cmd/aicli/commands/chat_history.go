package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func truncateAICLIMessages(session *ChatSession, keep int) {
	if session == nil {
		return
	}
	if keep <= 0 {
		session.Messages = nil
		return
	}
	if keep >= len(session.Messages) {
		return
	}
	truncated := make([]runtimetypes.Message, keep)
	for index := 0; index < keep; index++ {
		truncated[index] = *session.Messages[index].Clone()
	}
	session.Messages = truncated
}

func syncChatSystemPromptMessage(session *ChatSession) {
	if session == nil {
		return
	}
	// Durable history stores a frozen environment-aware prefix without turn-
	// volatile goal guidance. Goal text is injected as a frozen turn-context
	// message via agent Options["active_goal_guidance"], never via SystemPrompt.
	prompt := strings.TrimSpace(composeDurableChatSystemPromptWithGuidance(session))
	if prompt == "" {
		return
	}
	systemMessage := *runtimetypes.NewSystemMessage(prompt)
	if len(session.Messages) == 0 {
		replaceRuntimeMessages(session, []runtimetypes.Message{systemMessage})
		return
	}
	if strings.EqualFold(strings.TrimSpace(session.Messages[0].Role), "system") {
		// Content-equality short-circuit: never rewrite historical prefix when
		// the composed system prompt is unchanged (session-frozen environment
		// snapshot makes environment churn a no-op across multi-turn sends).
		if strings.TrimSpace(session.Messages[0].Content) == prompt {
			return
		}
		replaced := make([]runtimetypes.Message, len(session.Messages))
		copy(replaced, session.Messages)
		updatedSystem := *replaced[0].Clone()
		updatedSystem.Content = prompt
		replaced[0] = updatedSystem
		replaceRuntimeMessages(session, replaced)
		return
	}
	messages := make([]runtimetypes.Message, 0, len(session.Messages)+1)
	messages = append(messages, systemMessage)
	messages = append(messages, session.Messages...)
	replaceRuntimeMessages(session, messages)
}

func appendRuntimeMessage(session *ChatSession, message runtimetypes.Message) {
	if session == nil {
		return
	}
	session.Messages = append(session.Messages, *message.Clone())
	// 恢复后的 canonical 展示历史与投影保持同步，使继续对话的新消息
	// 也能出现在完整历史回放中。
	session.appendResumeHistoryMessage(*message.Clone())
	session.StatusMessageCount = countChatStatusMessages(session.Messages)
}

func replaceRuntimeMessages(session *ChatSession, messages []runtimetypes.Message) error {
	if session == nil {
		return nil
	}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("message role cannot be empty")
		}
	}
	session.Messages = cloneRuntimeMessages(messages)
	// 上下文整体替换（压缩/恢复）后，旧展示快照不再可信：恢复路径会
	// 重新从 canonical 转录加载完整历史，压缩路径则保持投影展示。
	session.clearResumeHistory()
	session.StatusMessageCount = countChatStatusMessages(session.Messages)
	return nil
}

func cloneRuntimeMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]runtimetypes.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func chatMessagesHaveConversation(messages []runtimetypes.Message) bool {
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			return true
		}
	}
	return false
}

func countChatStatusMessages(messages []runtimetypes.Message) int {
	count := 0
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "" && !strings.EqualFold(role, "system") {
			count++
		}
	}
	return count
}

func printVisibleChatHistory(session *ChatSession, header string) int {
	return printVisibleChatHistoryWithLoadGrant(session, header, false)
}

// printVisibleSessionLoadHistory is the session-load entry point (/resume,
// /load, startup restore). Unlike /history and truncation replays it also
// authorizes the one-shot native-scrollback replacement, because a loaded
// generation must reach the terminal owner even when the Scene already
// reconciled with the replayed runtime event log (seeded=false).
func printVisibleSessionLoadHistory(session *ChatSession, header string) int {
	return printVisibleChatHistoryWithLoadGrant(session, header, true)
}

func printVisibleChatHistoryWithLoadGrant(session *ChatSession, header string, sessionLoad bool) int {
	messages := collectVisibleChatHistory(session)
	if len(messages) == 0 {
		return 0
	}
	if session != nil && session.Interaction != nil && session.Interaction.UnifiedRendererEnabled() {
		// Unified production history is semantic input to AppState. Never replay
		// persisted rows through the compatibility surface, because that path is
		// physically fenced and would otherwise silently drop the conversation.
		bridge := ensureChatRuntimeEventBridge(session)
		if bridge != nil {
			seedHeader := ""
			if strings.TrimSpace(header) != "" {
				seedHeader = fmt.Sprintf("%s (%d 条消息):", strings.TrimSpace(header), len(messages))
			}
			// Runtime event logs are deliberately best-effort and can cover only a
			// suffix/subset of a persisted conversation. Reconcile every time
			// against canonical visible history; the bridge owns stable identities
			// so this remains idempotent and never falls back to surface replay.
			if sessionLoad {
				bridge.seedPersistedHistoryForSessionLoad(messages, seedHeader)
			} else {
				bridge.seedPersistedHistory(messages, seedHeader)
			}
			// seed 是「进入恢复」的可见内容起点：它决定了第一帧有多少 cell，
			// 也是布局规划与行投递的输入。单独计量以便区分「读日志慢」与
			// 「协调 6197 条历史慢」。
			markChatStartup("history_seed")
			session.Interaction.RequestUnifiedFrame()
		}
		return len(messages)
	}
	// History is already-final content. Settle any ClearPrompt layout debt
	// (pendingScrollDown / blank-row flag) BEFORE the first content write so
	// live surface compensation is not attached to transcript replay.
	settleInteractiveOutputLayout(session)
	// Replay is a pure content-plane operation: the replay renderer routes user
	// echo through RenderReplayedUserInput, which never restores the composer, so
	// replaying already-final history cannot grow the bottom reserve or bill
	// surface scroll compensation into the transcript. The caller re-shows the
	// prompt once after replay completes.
	renderer := newAICLIReplayTranscriptRenderer(session)
	if strings.TrimSpace(header) != "" {
		renderer.RenderSupplement(fmt.Sprintf("%s (%d 条消息):", strings.TrimSpace(header), len(messages)))
	}
	toolCalls := indexChatHistoryToolCalls(messages)
	for index := range messages {
		renderVisibleChatHistoryMessage(renderer, messages[index], toolCalls)
	}
	return len(messages)
}

// resumeHistorySnapshotMode 决定一次逐页补齐如何处理「非授权式语义转录快照」。
//
// 背景（实测，见 docs/e2e/resume-incremental-publish-coalescing.md §10）：逐页发布里
// 真正贵的是**投递**而不是构造——场景快照构造只要 0-134ms，而把 action 投进 UI actor
// 要 74ms-1.12s（首次最贵，正在与首帧投递争用）。因此增量载荷不是正解，控制「这次
// 中间态值不值得让后台线程停等」才是。
type resumeHistorySnapshotMode int

const (
	// resumeHistorySnapshotSkip：完全跳过。这一页之后紧跟装载收尾的授权式替换，会整份
	// 覆盖语义转录与原生 scrollback，本次快照必被立刻覆盖。
	resumeHistorySnapshotSkip resumeHistorySnapshotMode = iota
	// resumeHistorySnapshotAwait：阻塞投递，保证这一帧真的进邮箱并被画出来。用于首步
	// ——它是「边读边画」对用户的第一次承诺，也是小历史下唯一的可见更新。
	resumeHistorySnapshotAwait
	// resumeHistorySnapshotTry：非阻塞投递，actor 忙时放弃这次中间态（数据面已在 Scene
	// 里，下一次发布或收尾的授权式替换会补上）。用于首步之后的各个步子。
	resumeHistorySnapshotTry
)

// resumeHistorySnapshotModeForStep 选择第 visited 次发布（1-based）的快照模式。
// estimatedPages 未知（0）时按「不是最后一步」处理，只影响是否多花一次快照。
func resumeHistorySnapshotModeForStep(visited, stride, estimatedPages int, hasMore bool) resumeHistorySnapshotMode {
	if !hasMore || resumeHistoryIncrementalStepIsLast(visited, stride, estimatedPages) {
		return resumeHistorySnapshotSkip
	}
	if visited <= 1 {
		return resumeHistorySnapshotAwait
	}
	return resumeHistorySnapshotTry
}

// renderResumeHistoryPageIncremental 把「刚取回的较早一页」增量装配进统一渲染
// 数据面：只 reconcile 这一页（锚点插入 + 可选的非授权式快照 + 统一帧），不重算
// 已经装载过的部分，也不铸造原生 scrollback 的销毁式替换——那属于整次会话装载的
// 收尾（装载收尾那一次 seed 会把完整 generation 一次性替换进原生 scrollback）。
//
// 没有统一渲染通道（plain / JSON / legacy / 无协调器）时是 no-op：这些平面的
// 输出顺序保持原有的一次性装载语义，不会被逐页绘制打断。
//
// mode 决定这次发布的非授权快照怎么走：跳过 / 阻塞投递（保证可见）/ 非阻塞投递
// （actor 忙时放弃中间态）。三种模式的选择见 resumeHistorySnapshotModeForStep。
func renderResumeHistoryPageIncremental(session *ChatSession, page []runtimetypes.Message, mode resumeHistorySnapshotMode) bool {
	if session == nil || len(page) == 0 {
		return false
	}
	if session.NoInteractive || session.JSONOutput || session.Interaction == nil ||
		!session.Interaction.UnifiedRendererEnabled() {
		return false
	}
	bridge := ensureChatRuntimeEventBridge(session)
	if bridge == nil {
		return false
	}
	if !bridge.seedPersistedHistoryPageWithSnapshot(page, mode) {
		return false
	}
	// 增量装载的成本要分两笔看：数据面 reconcile（建 unit + 锚点插入 + 非授权
	// 快照）与呈现面统一帧（整份转录重规划）。二者合并成一个标记时无法判断该
	// 按页合并发布还是该优化 reconcile，因此分开计量。
	markChatStartup("resume_history_reconcile")
	markChatStartup("resume_history_publish")
	session.Interaction.RequestUnifiedFrame()
	return true
}

// replayLoadedSessionHistory 在会话装载（/resume、/load、resume picker）的确认
// 单元提交之后回放已装载的展示历史，并启动窗口化装载的较早页后台补齐。两者必须
// 保持这个顺序：先画出用户最关心的尾部，再逐页往前补齐。
func replayLoadedSessionHistory(session *ChatSession, header string) int {
	// 同步回放（canonical seed + 统一帧）是恢复过程中最可见的一段：动态栏亮出
	// 进度，startDeferredResumeHistoryLoad 随后接手（有较早页时更新「已加载
	// N/M」计数，没有时立即清除并恢复空闲状态行）。
	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 0, 0)
	count := printVisibleChatHistory(session, header)
	startDeferredResumeHistoryLoad(session)
	return count
}

// replayVisibleChatHistoryAfterTruncation re-renders the already-truncated
// canonical history into the transcript after backtrack/rewind, so the UI no
// longer presents removed turns as live state. The surface transcript is
// append-only, so without this replay the old (now-removed) messages stay on
// screen and the truncation is invisible. It follows the same "real dispatch
// path" as resume/startup (beginDirectInteractiveOutput -> printVisibleChatHistory):
// clear prompt (defers shrink), settle layout debt, then replay as a pure
// content-plane operation that cannot grow the bottom reserve. Safe no-op for
// non-interactive / JSON modes (guards live inside the renderer).
//
// Before replaying, the retained visible region (rewriteable soft tail) is
// cleared so removed turns do not linger as ghost rows under the replay; the
// archive marker appended to the header tells the user that everything above
// it is stale. Rows already handed off into native scrollback are physically
// irreversible and cannot be erased — the marker is the only distinction for
// those.
func replayVisibleChatHistoryAfterTruncation(session *ChatSession, header string) int {
	if session == nil || !hasVisibleChatHistory(session) {
		return 0
	}
	beginDirectInteractiveOutput(session)
	clearRetainedTranscriptTail(session)
	if strings.TrimSpace(header) != "" {
		header = strings.TrimSpace(header) + "：上方旧消息已失效"
	}
	return printVisibleChatHistory(session, header)
}

// clearRetainedTranscriptTail erases the visible rows of the committed
// history region so a post-backtrack replay starts from a clean viewport
// instead of stacking on top of ghost rows of removed turns. Rows already
// handed off into native scrollback are physically irreversible and stay; the
// archive marker in the replay header is the only distinction for those. It
// is a no-op when the session has no enabled surface or nothing is committed.
func clearRetainedTranscriptTail(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Surface == nil || !session.Surface.Enabled() {
		return
	}
	// Full-region wipe: replay re-prints every surviving canonical message
	// afterwards, so clearing the whole visible output region (not just the
	// narrow soft window, which only ever covers the last assistant chunk) is
	// the correct retained-visible-region erase for truncation.
	session.Surface.ClearCommittedHistoryForReplay()
}

func hasVisibleChatHistory(session *ChatSession) bool {
	return len(collectVisibleChatHistory(session)) > 0
}

func collectVisibleChatHistory(session *ChatSession) []runtimetypes.Message {
	if session == nil {
		return nil
	}
	// 恢复会话后优先回放 canonical 完整转录；未恢复（或后端不支持
	// canonical 分页）时回退到模型热上下文投影。
	source := session.Messages
	if resumeHistory := session.resumeHistorySnapshot(); len(resumeHistory) > 0 {
		source = resumeHistory
	}
	if len(source) == 0 {
		return nil
	}

	// Hide both durable and outbound system prefixes. Goal guidance is injected as
	// turn-context messages (not system text); older sessions may still store
	// outbound-with-goal system text.
	hiddenSystemPrompt := strings.TrimSpace(composeDurableChatSystemPromptWithGuidance(session))
	outboundSystemPrompt := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))
	rawSystemPrompt := strings.TrimSpace(session.SystemPromptText)
	messages := make([]runtimetypes.Message, 0, len(source))
	for _, message := range source {
		if !isVisibleChatHistoryMessage(session, message, hiddenSystemPrompt, rawSystemPrompt) {
			continue
		}
		if outboundSystemPrompt != "" &&
			strings.EqualFold(strings.TrimSpace(message.Role), "system") &&
			strings.TrimSpace(message.Content) == outboundSystemPrompt {
			continue
		}
		messages = append(messages, *message.Clone())
	}
	return messages
}

func isVisibleChatHistoryMessage(session *ChatSession, message runtimetypes.Message, hiddenSystemPrompt string, rawSystemPrompt string) bool {
	if strings.TrimSpace(message.Role) == "" {
		return false
	}
	// Frozen turn-context snapshots (fact/recall/goal/todo/etc.) are prompt-only
	// infrastructure and must not pollute the user-visible transcript.
	if message.Metadata.GetBool("context_snapshot", false) ||
		strings.TrimSpace(message.Metadata.GetString("context_stage", "")) != "" {
		return false
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	// Legacy defense: older sessions may have fact ledgers as assistant text with
	// stripped metadata. Hide the well-known ledger header so "继续" replays stay clean.
	if isLegacyFactLedgerTranscript(content) {
		return false
	}
	switch role {
	case "system":
		if content == "" || (hiddenSystemPrompt != "" && content == hiddenSystemPrompt) || (rawSystemPrompt != "" && content == rawSystemPrompt) {
			return false
		}
	case "developer":
		// Developer messages are prompt infrastructure, never user-visible chat.
		return false
	case "assistant":
		return content != "" || len(message.ToolCalls) > 0 || (chatReasoningOutputEnabled(session) && finalReasoningBlock(&message) != nil)
	case "tool":
		return content != "" || strings.TrimSpace(chatHistoryToolError(message)) != ""
	default:
		return content != ""
	}
	return true
}

const legacyFactLedgerHeader = "Verified fact ledger (authoritative over compacted prose):"

func isLegacyFactLedgerTranscript(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, legacyFactLedgerHeader)
}

func renderVisibleChatHistoryMessage(renderer *aicliTranscriptRenderer, message runtimetypes.Message, toolCalls map[string]runtimetypes.ToolCall) {
	if renderer == nil {
		return
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := message.Content
	switch role {
	case "assistant":
		renderer.RenderReasoning(finalReasoningBlock(&message))
		renderer.RenderAssistant(content)
		// P5.6: Running is viewport-only (ActiveBand). History/replay only
		// emits the final Completed cell once the matching tool message
		// arrives — never a Running row in scrollback.
	case "tool":
		call := toolCalls[strings.TrimSpace(message.ToolCallID)]
		toolName := firstNonEmptyChatValue(
			strings.TrimSpace(call.Name),
			chatHistoryToolNameFromMetadata(message.Metadata),
			strings.TrimSpace(message.ToolCallID),
			"tool",
		)
		output, toolErr := splitChatHistoryToolResult(message)
		renderer.RenderToolEvent(chatHistoryToolReplayEvent(message, toolName, call.Args, output, toolErr))
	case "system":
		renderer.RenderSystem(content)
	case "user":
		renderer.RenderUser(content)
	default:
		renderer.RenderSystem(fmt.Sprintf("[%s] %s", role, content))
	}
}

func indexChatHistoryToolCalls(messages []runtimetypes.Message) map[string]runtimetypes.ToolCall {
	indexed := make(map[string]runtimetypes.ToolCall)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				indexed[callID] = call
			}
		}
	}
	return indexed
}

// chatHistoryToolReplayEvent 用持久化消息重建与实时同形的 tool_result
// ChatEvent。
//
// 实时链路的 ChatEvent 由 provider loop 携带完整 Arguments（含 command）
// 与工具元数据；持久化回放只有消息本体。若直接以空 Arguments 构造事件，
// compact 渲染器会退化为 "• Completed <tool 名>" 并放弃摘要，导致 live 与
// replay 的工具单元格不一致。这里从消息元数据恢复参数（tool_invocation.
// attempted_args 是实时调用参数的持久化副本），并复用实时同款的 metadata
// 提升逻辑，使两个路径的 renderSharedChatToolEvent 输入一致。
func chatHistoryToolReplayEvent(
	message runtimetypes.Message,
	toolName string,
	callArgs map[string]interface{},
	output string,
	toolErr string,
) runtimechatcore.ChatEvent {
	event := runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   toolName,
		ToolCallID: message.ToolCallID,
		Arguments:  cloneFunctionSchema(callArgs),
		Output:     output,
		Error:      toolErr,
		Success:    strings.TrimSpace(toolErr) == "",
		Metadata:   chatHistoryToolMetadataMap(message.Metadata),
	}
	if len(event.Arguments) == 0 {
		event.Arguments = chatHistoryToolAttemptedArgs(event.Metadata)
	}
	return event
}

// chatHistoryToolDisplay 返回持久化工具消息在实时链路上等价的 compact
// 显示文本；无可用信息时返回空串，调用方回退到原始输出。
func chatHistoryToolDisplay(message runtimetypes.Message, toolName string, callArgs map[string]interface{}) string {
	output, toolErr := splitChatHistoryToolResult(message)
	event := chatHistoryToolReplayEvent(message, toolName, callArgs, output, toolErr)
	return strings.TrimSpace(renderSharedChatToolEvent(event))
}

// chatHistoryToolAttemptedArgs 从元数据的 tool_invocation.attempted_args
// 恢复工具调用参数（兼容 map 与 JSON 字符串两种历史编码）。
func chatHistoryToolAttemptedArgs(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["tool_invocation"]
	if !ok || raw == nil {
		return nil
	}
	invocation, ok := raw.(map[string]interface{})
	if !ok {
		decoded, err := decodeChatHistoryJSONObject(raw)
		if err != nil {
			return nil
		}
		invocation = decoded
	}
	if invocation == nil {
		return nil
	}
	attempted, ok := invocation["attempted_args"].(map[string]interface{})
	if !ok || len(attempted) == 0 {
		if decoded, err := decodeChatHistoryJSONObject(invocation["attempted_args"]); err == nil {
			attempted = decoded
		}
	}
	if len(attempted) == 0 {
		return nil
	}
	return cloneFunctionSchema(attempted)
}

func decodeChatHistoryJSONObject(value interface{}) (map[string]interface{}, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		decoded := map[string]interface{}{}
		if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		decoded := map[string]interface{}{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
}

func chatHistoryToolNameFromMetadata(metadata runtimetypes.Metadata) string {
	return payloadStringValue(chatHistoryToolMetadataMap(metadata)["tool_name"])
}

func chatHistoryToolMetadataMap(metadata runtimetypes.Metadata) map[string]interface{} {
	flat := cloneFunctionSchema(map[string]interface{}(metadata))
	if len(flat) == 0 {
		return nil
	}

	var nested map[string]interface{}
	switch value := flat["tool_metadata"].(type) {
	case map[string]interface{}:
		nested = value
	case runtimetypes.Metadata:
		nested = map[string]interface{}(value)
	}
	for _, key := range []string{
		"tool_name", "tool_source", "tool_error", "error",
		"workdir", "cwd",
		// 以下键来自嵌套 tool_metadata，是 compact 工具结果渲染的输入
		// （output_kind 决定 "stdout" 段标签、capture_limit_* 决定是否
		// 降级为纯文本预览）。重放时把它们提升到顶层，保证与实时
		// ChatEvent 的 metadata 形状一致，否则工具单元格会退化为
		// "• Completed <tool>" + 全量原文。
		//
		// 注意不要提升 shell_type/shell_path/shell_display：实时 ChatEvent
		// 的顶层 metadata 没有它们（它们在嵌套 tool_metadata 里），提升会让
		// compactToolContextLines 多渲染一行 "shell: pwsh (C:\...\pwsh.exe)"，
		// 与 live 单元格不一致且把可执行文件绝对路径带进转录。
		"output_kind", "capture_limit_reached", "output_capture_complete",
		"exit_code", "command", "attempted_args",
	} {
		if payloadStringValue(flat[key]) == "" && payloadStringValue(nested[key]) != "" {
			flat[key] = nested[key]
		}
	}
	if intPayloadValue(flat, "duration_ms") <= 0 && intPayloadValue(nested, "duration_ms") > 0 {
		flat["duration_ms"] = intPayloadValue(nested, "duration_ms")
	}
	return flat
}

func chatHistoryToolError(message runtimetypes.Message) string {
	metadata := chatHistoryToolMetadataMap(message.Metadata)
	for _, key := range []string{"tool_error", "error"} {
		if errText := strings.TrimSpace(payloadStringValue(metadata[key])); errText != "" {
			return errText
		}
	}
	const prefix = "Tool execution failed:"
	content := strings.TrimSpace(message.Content)
	if !strings.HasPrefix(content, prefix) {
		return ""
	}
	firstLine := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(content, prefix)), "\n", 2)[0]
	return strings.TrimSpace(firstLine)
}

func splitChatHistoryToolResult(message runtimetypes.Message) (string, string) {
	content := strings.TrimSpace(message.Content)
	toolErr := chatHistoryToolError(message)
	if toolErr == "" {
		return content, ""
	}
	const failurePrefix = "Tool execution failed:"
	if strings.HasPrefix(content, failurePrefix) {
		if newline := strings.IndexByte(content, '\n'); newline >= 0 {
			content = strings.TrimSpace(content[newline+1:])
		} else {
			content = ""
		}
	}
	return content, toolErr
}
