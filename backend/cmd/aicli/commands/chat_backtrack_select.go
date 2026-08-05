package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// handleInteractiveBacktrackSelect opens a user-turn picker (fullscreen when
// available) and applies a conversation-only backtrack with composer prefill.
// Invoked by bare Esc on an empty composer, or by `/backtrack select`.
//
// The unified path does not enter this compatibility handler. It uses
// openChatBacktrackPicker below, which holds one ScreenLease for selection,
// releases it before any destructive work, then replaces the Scene from
// canonical history instead of writing/replaying terminal rows directly.
func handleInteractiveBacktrackSelect(session *ChatSession) bool {
	if rejectUnifiedInteractiveLegacyCommand(session, "/backtrack") {
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if session.NoInteractive || session.JSONOutput {
		fmt.Println("提示: 非交互模式请使用 /backtrack <index> --apply")
		return false
	}
	if session.RuntimeSession == nil {
		fmt.Println("错误: 当前没有可回退的持久化会话")
		return false
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		fmt.Println("错误: 当前会话未初始化本地 runtime host，无法执行 backtrack")
		return false
	}

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	actor, err := chatActorForSession(ctx, session)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	turns, err := actor.ListTurns(ctx)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if len(turns) == 0 {
		fmt.Println("当前会话没有可回退的 user turn")
		return false
	}

	beginDirectInteractiveOutput(session)
	selected, cancelled, err := readBacktrackTurnPick(session, turns)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if cancelled || selected == nil {
		fmt.Println("已取消回退，当前会话保持不变")
		return false
	}

	mode := runtimechat.BacktrackModeConversation
	if selected.HasLaterMutation && selected.BaseCheckpointID != "" {
		// Offer both only when later file mutations exist; keep default conversation-only.
		mode = promptBacktrackMode(session, *selected)
	}

	req := runtimechat.BacktrackRequest{
		UserTurnIndex: runtimechat.IntPtr(selected.Index),
		MessageID:     strings.TrimSpace(selected.MessageID),
		Mode:          mode,
		PreviewOnly:   false,
		AutoSubmit:    false,
	}
	// Prefer durable message_id alone so index drift cannot fail a stable anchor.
	if req.MessageID != "" {
		req.UserTurnIndex = nil
	}

	result, _, err := actor.Backtrack(ctx, req)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		if result != nil {
			printChatBacktrackResult(result, false)
		}
		return false
	}
	if err := syncRuntimeSessionBackIntoCLI(session); err != nil {
		fmt.Printf("警告: 回退后同步 CLI 会话失败: %v\n", err)
	}
	printChatBacktrackResult(result, false)
	if result != nil {
		// 与 /backtrack <N> --apply 一致：把截断后的 canonical 历史重放到
		// transcript，避免选择回退后界面上仍残留被移除的旧消息。
		replayVisibleChatHistoryAfterTruncation(session, fmt.Sprintf("已回退到 user turn %d", result.UserTurnIndex))
	}

	composerPrompt := ""
	if result != nil {
		composerPrompt = strings.TrimSpace(result.ComposerPrompt)
		if composerPrompt == "" {
			composerPrompt = strings.TrimSpace(result.EditedPrompt)
		}
		if composerPrompt == "" {
			composerPrompt = strings.TrimSpace(selected.Preview)
		}
	}
	if composerPrompt != "" {
		if err := restoreChatRetryDraft(session, composerPrompt); err != nil {
			fmt.Printf("提示: 历史已截断；composer 预填失败: %v\n", err)
			fmt.Printf("可手动重发: %s\n", summarizeMemoryNote(composerPrompt, 160))
		} else {
			fmt.Println("提示: 原提示已预填到输入区，检查后按 Enter 发送（Esc 再次打开选择器）")
		}
	}
	return false
}

// canOpenChatBacktrackPicker is intentionally stricter than a generic list
// capability check. Backtrack changes canonical transcript state, so it may
// only begin while the unified primary presenter is idle, owns its viewport,
// and no competing popup or alternate screen owns input.
func canOpenChatBacktrackPicker(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil || session.RuntimeSession == nil ||
		session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	if session.RuntimeEventBridge != nil && session.RuntimeEventBridge.isRunActive() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatBacktrackPicker executes the typed alternate-screen interaction.
// The lease ends before actor.Backtrack, canonical Scene replacement, command
// commit, or composer mutation. This gives the primary TerminalSession one
// clear recovery boundary and prevents destructive transcript updates from
// interleaving with fullscreen list frames.
func openChatBacktrackPicker(session *ChatSession, _ BacktrackPickerRequest) {
	if !canOpenChatBacktrackPicker(session) {
		return
	}
	actor, err := backtrackQueryActor(session)
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}
	ctx := backtrackQueryContext(session)
	turns, err := actor.ListTurns(ctx)
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}
	if len(turns) == 0 {
		_ = renderChatCommandResult(session, commandTextResult("当前会话没有可回退的 user turn"), false)
		return
	}

	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "回退到历史 user turn",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开回退选择器失败: %w", err)), false)
		return
	}
	if !session.Interaction.postUIAction(ui.OpenBacktrackPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("回退选择器状态未提交")), false)
		return
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list and never waits
	// on the primary renderer.
	session.Interaction.waitUIActorIdle()

	picked, pickErr := ui.SelectFullScreenListWithLease(ctx, resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        "回退到历史 user turn",
		Subtitle:     formatBacktrackPickerSubtitle(len(turns)),
		EmptyMessage: "没有匹配的 user turn",
		ConfirmLabel: "回退到选中 turn（截断其后历史）",
		Items:        buildBacktrackFullScreenItems(turns),
	}, lease)

	selected := (*runtimechat.UserTurn)(nil)
	mode := runtimechat.BacktrackModeConversation
	if pickErr == nil && !picked.Cancelled && picked.Index >= 0 && picked.Index < len(turns) {
		turn := turns[picked.Index]
		selected = &turn
		if turn.HasLaterMutation && strings.TrimSpace(turn.BaseCheckpointID) != "" {
			mode, pickErr = selectBacktrackModeWithLease(ctx, resumeFullScreenTerminal(session), lease, turn)
			if pickErr == nil && mode == "" {
				selected = nil
			}
		}
	}

	_ = session.Interaction.postUIAction(ui.CloseBacktrackPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not mutate the Scene
	// until the actor has observed it, otherwise a replacement frame could race
	// the final alternate-screen exit/recovery transaction.
	session.Interaction.waitUIActorIdle()

	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭回退选择器失败: %w", releaseErr)), false)
		return
	}
	if pickErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("回退选择器失败: %w", pickErr)), false)
		return
	}
	if selected == nil || picked.Cancelled {
		_ = renderChatCommandResult(session, commandTextResult("已取消回退，当前会话保持不变"), false)
		return
	}

	applySelectedBacktrack(session, actor, ctx, *selected, mode)
}

// selectBacktrackModeWithLease preserves the legacy choice between a
// conversation-only truncation and conversation+code restoration without
// reopening raw stdin. It stays inside the same already-owned alternate screen.
func selectBacktrackModeWithLease(ctx context.Context, terminal *ui.Terminal, lease ui.ScreenLease, turn runtimechat.UserTurn) (string, error) {
	result, err := ui.SelectFullScreenListWithLease(ctx, terminal, ui.FullScreenListOptions{
		Title:        "选择回退范围",
		Subtitle:     "该 turn 之后存在文件变更；Enter 确认，Esc 取消",
		ConfirmLabel: "使用选中范围回退",
		Items: []ui.FullScreenListItem{
			{
				Title:      "仅回退对话",
				Detail:     "保留工作区文件，仅截断后续对话历史",
				SearchText: "conversation 对话 only",
			},
			{
				Title:      "回退对话和代码",
				Detail:     "从 base checkpoint 恢复文件：" + shortID(turn.BaseCheckpointID),
				SearchText: "both code 对话 代码 checkpoint",
			},
		},
	}, lease)
	if err != nil {
		return "", err
	}
	if result.Cancelled || result.Index < 0 {
		return "", nil
	}
	if result.Index == 1 {
		return runtimechat.BacktrackModeBoth, nil
	}
	return runtimechat.BacktrackModeConversation, nil
}

// applySelectedBacktrack performs the destructive half of the typed picker
// after alternate-screen ownership has ended. It shares the direct apply
// transaction, while preserving the picker item's preview as a last-resort
// composer fallback.
func applySelectedBacktrack(session *ChatSession, actor *runtimechat.SessionActor, ctx context.Context, selected runtimechat.UserTurn, mode string) {
	if session == nil || actor == nil {
		return
	}
	req := runtimechat.BacktrackRequest{
		UserTurnIndex: runtimechat.IntPtr(selected.Index),
		MessageID:     strings.TrimSpace(selected.MessageID),
		Mode:          mode,
		PreviewOnly:   false,
		AutoSubmit:    false,
	}
	// Stable durable message identity wins over a list-time positional index.
	// This keeps a delayed picker result from targeting the wrong turn after
	// unrelated index shifts in the actor's persisted history.
	if req.MessageID != "" {
		req.UserTurnIndex = nil
	}
	applyUnifiedBacktrackWithActor(session, actor, ctx, req, selected.Preview)
}

// applyUnifiedBacktrackRequest is the typed command-effect executor for a
// direct `/backtrack <index> --apply|--submit`. Unlike the historical handler,
// it cannot emit raw terminal output: the destructive mutation, replacement
// and follow-up composer/submit behavior all remain in the unified pipeline.
func applyUnifiedBacktrackRequest(session *ChatSession, request runtimechat.BacktrackRequest) {
	actor, err := backtrackQueryActor(session)
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}
	applyUnifiedBacktrackWithActor(session, actor, backtrackQueryContext(session), request, "")
}

// applyUnifiedBacktrackWithActor owns every post-parse destructive backtrack
// path. The caller must already have released any ScreenLease. Its fixed order
// is actor mutation -> CLI sync -> canonical Scene replacement -> one command
// cell -> composer restoration or normal send pipeline.
func applyUnifiedBacktrackWithActor(session *ChatSession, actor *runtimechat.SessionActor, ctx context.Context, request runtimechat.BacktrackRequest, fallbackPrompt string) {
	if session == nil || actor == nil {
		return
	}
	autoSubmit := request.AutoSubmit
	request.AutoSubmit = false
	request.PreviewOnly = false

	result, _, err := actor.Backtrack(ctx, request)
	if err != nil {
		lines := []string{"错误: " + err.Error()}
		lines = append(lines, formatChatBacktrackResultLines(result, false)...)
		_ = renderChatCommandResult(session, CommandResult{
			Blocks: []RenderBlock{{Document: textLinesDocument(lines)}},
			Action: CommandContinue,
		}, false)
		return
	}

	var warnings []string
	if err := syncRuntimeSessionBackIntoCLI(session); err != nil {
		warnings = append(warnings, "回退后同步 CLI 会话失败: "+err.Error())
	}
	if result == nil {
		warnings = append(warnings, "回退操作未返回摘要；已按当前 canonical history 重建转录")
	}

	turnIndex := backtrackRequestTurnIndex(request)
	if result != nil {
		turnIndex = result.UserTurnIndex
	} else if request.UserTurnIndex == nil {
		turnIndex = -1
	}
	header := "已回退：上方旧消息已失效"
	if turnIndex >= 0 {
		header = fmt.Sprintf("已回退到 user turn %d：上方旧消息已失效", turnIndex)
	}
	bridge := ensureChatRuntimeEventBridge(session)
	if bridge == nil || !bridge.replaceCanonicalHistoryProjection(collectVisibleChatHistory(session), header) {
		warnings = append(warnings, "回退已完成，但统一转录替换未提交；请重新打开会话以重建 canonical history")
	}

	lines := formatChatBacktrackResultLines(result, false)
	if len(lines) == 0 {
		if turnIndex >= 0 {
			lines = append(lines, fmt.Sprintf("backtrack apply: turn=%d mode=%s", turnIndex, request.Mode))
		} else {
			lines = append(lines, fmt.Sprintf("backtrack apply: mode=%s", request.Mode))
		}
	}
	for _, warning := range warnings {
		lines = append(lines, "  warning: "+warning)
	}

	composerPrompt := ""
	if result != nil {
		composerPrompt = strings.TrimSpace(result.ComposerPrompt)
		if composerPrompt == "" {
			composerPrompt = strings.TrimSpace(result.EditedPrompt)
		}
	}
	if composerPrompt == "" {
		composerPrompt = strings.TrimSpace(fallbackPrompt)
	}
	if autoSubmit && composerPrompt == "" {
		lines = append(lines, "警告: --submit 需要非空 edit 或锚点文本，已跳过自动发送")
	} else if autoSubmit {
		lines = append(lines, "提示: 已截断历史，正在自动发送原提示/编辑文本")
	} else if composerPrompt != "" {
		lines = append(lines, "提示: 原提示已预填到输入区，检查后按 Enter 发送（Esc 再次打开选择器）")
	}

	command := CommandResult{
		Blocks: []RenderBlock{{Document: textLinesDocument(lines)}},
		Action: CommandContinue,
	}
	if err := renderChatCommandResult(session, command, false); err != nil {
		return
	}
	if autoSubmit && composerPrompt != "" {
		renderSubmittedUserInputEcho(session, composerPrompt)
		response, sendErr := sendMessage(session, composerPrompt)
		if sendErr != nil {
			interrupted := session.IsInterrupted()
			rememberChatTurnRecovery(session, composerPrompt, interrupted)
			if session.Interaction != nil {
				session.Interaction.RenderError(sendErr)
			}
			renderChatTurnRecoveryHintForError(session, sendErr)
			return
		}
		finishSuccessfulChatSend(session, response, false)
		return
	}
	if composerPrompt != "" {
		if err := restoreChatRetryDraft(session, composerPrompt); err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("历史已截断；composer 预填失败: %w", err)), false)
		}
	}
}

func backtrackRequestTurnIndex(request runtimechat.BacktrackRequest) int {
	if request.UserTurnIndex == nil {
		return 0
	}
	return *request.UserTurnIndex
}

func readBacktrackTurnPick(session *ChatSession, turns []runtimechat.UserTurn) (*runtimechat.UserTurn, bool, error) {
	if len(turns) == 0 {
		return nil, true, nil
	}
	if terminal := resumeFullScreenTerminal(session); ui.CanUseFullScreenList(terminal) {
		picked, err := readBacktrackTurnPickFullScreen(session, terminal, turns)
		if err == nil || !errors.Is(err, ui.ErrFullScreenUnavailable) {
			return picked, picked == nil, err
		}
	}
	return readBacktrackTurnPickPlain(session, turns)
}

func readBacktrackTurnPickFullScreen(session *ChatSession, terminal *ui.Terminal, turns []runtimechat.UserTurn) (*runtimechat.UserTurn, error) {
	items := buildBacktrackFullScreenItems(turns)
	if len(items) == 0 {
		return nil, nil
	}

	var lease ui.ScreenLease
	if session != nil && session.Surface != nil && session.Surface.Enabled() {
		// Suspend the primary presenter while the picker owns the alternate
		// screen; release repaints from retained state.
		var acquireErr error
		lease, acquireErr = session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
			Title: "回退到历史 user turn",
		})
		if acquireErr != nil && errors.Is(acquireErr, ui.ErrScreenLeaseBusy) {
			return nil, acquireErr
		}
	}
	if session != nil && session.Interaction != nil {
		session.Interaction.ClearPrompt()
		session.Interaction.ResetPromptState()
	}
	defer func() {
		if lease != nil {
			_ = lease.Release(context.Background())
		}
		if session != nil && session.Interaction != nil {
			session.Interaction.ResetPromptState()
			session.Interaction.RefreshStatus("")
		}
	}()

	result, err := ui.SelectFullScreenListWithLease(context.Background(), terminal, ui.FullScreenListOptions{
		Title:        "回退到历史 user turn",
		Subtitle:     formatBacktrackPickerSubtitle(len(turns)),
		EmptyMessage: "没有匹配的 user turn",
		ConfirmLabel: "回退到选中 turn（截断其后历史）",
		Items:        items,
	}, lease)
	if err != nil {
		return nil, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(turns) {
		return nil, nil
	}
	turn := turns[result.Index]
	return &turn, nil
}

func buildBacktrackFullScreenItems(turns []runtimechat.UserTurn) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(turns))
	for _, turn := range turns {
		items = append(items, ui.FullScreenListItem{
			Title:      formatBacktrackTurnTitle(turn),
			Detail:     formatBacktrackTurnDetail(turn),
			Preview:    formatBacktrackTurnPreview(turn),
			SearchText: formatBacktrackTurnSearchText(turn),
		})
	}
	return items
}

func formatBacktrackPickerSubtitle(count int) string {
	return fmt.Sprintf("共 %d 个 user turn · ↑/↓ 选择 · Enter 确认 · Esc 取消 · / 搜索", count)
}

func formatBacktrackTurnTitle(turn runtimechat.UserTurn) string {
	preview := strings.TrimSpace(turn.Preview)
	if preview == "" {
		preview = "(空消息)"
	}
	return fmt.Sprintf("[%d] %s", turn.Index, summarizeMemoryNote(preview, 72))
}

func formatBacktrackTurnDetail(turn runtimechat.UserTurn) string {
	parts := []string{fmt.Sprintf("msg#%d", turn.MessageIndex)}
	if turn.MessageID != "" {
		parts = append(parts, "id="+shortID(turn.MessageID))
	}
	if turn.HasLaterMutation {
		parts = append(parts, "has_later_mutation")
	}
	if turn.BaseCheckpointID != "" {
		parts = append(parts, "base_chk="+shortID(turn.BaseCheckpointID))
	}
	return strings.Join(parts, " · ")
}

func formatBacktrackTurnPreview(turn runtimechat.UserTurn) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("User turn #%d\n", turn.Index))
	b.WriteString(fmt.Sprintf("message_index: %d\n", turn.MessageIndex))
	if turn.MessageID != "" {
		b.WriteString(fmt.Sprintf("message_id: %s\n", turn.MessageID))
	}
	if turn.TurnID != "" {
		b.WriteString(fmt.Sprintf("turn_id: %s\n", turn.TurnID))
	}
	if turn.HasLaterMutation {
		b.WriteString("later_mutation: yes\n")
	}
	if turn.BaseCheckpointID != "" {
		b.WriteString(fmt.Sprintf("base_checkpoint: %s\n", turn.BaseCheckpointID))
	}
	b.WriteString("\n")
	preview := strings.TrimSpace(turn.Preview)
	if preview == "" {
		preview = "(空消息)"
	}
	b.WriteString(preview)
	return b.String()
}

func formatBacktrackTurnSearchText(turn runtimechat.UserTurn) string {
	parts := []string{
		strconv.Itoa(turn.Index),
		strconv.Itoa(turn.MessageIndex),
		turn.MessageID,
		turn.TurnID,
		turn.Preview,
	}
	if turn.HasLaterMutation {
		parts = append(parts, "mutation", "checkpoint")
	}
	return strings.Join(parts, " ")
}

func readBacktrackTurnPickPlain(session *ChatSession, turns []runtimechat.UserTurn) (*runtimechat.UserTurn, bool, error) {
	fmt.Printf("User turns（共 %d，0-based index；输入序号回退，空行/q 取消）:\n", len(turns))
	for _, turn := range turns {
		flag := ""
		if turn.HasLaterMutation {
			flag = " [has_later_mutation]"
		}
		fmt.Printf("  [%d] msg#%d%s %s\n", turn.Index, turn.MessageIndex, flag, turn.Preview)
	}
	fmt.Print("选择 user turn index: ")

	reader := backtrackPlainInputReader(session)
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return nil, true, nil
	}
	choice := strings.TrimSpace(line)
	if choice == "" || strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") || strings.EqualFold(choice, "cancel") {
		return nil, true, nil
	}
	idx, err := strconv.Atoi(choice)
	if err != nil {
		return nil, false, fmt.Errorf("无效序号 %q", choice)
	}
	for i := range turns {
		if turns[i].Index == idx {
			turn := turns[i]
			return &turn, false, nil
		}
	}
	return nil, false, fmt.Errorf("user turn index %d 不存在", idx)
}

func backtrackPlainInputReader(session *ChatSession) *bufio.Reader {
	_ = session
	return bufio.NewReader(os.Stdin)
}

func promptBacktrackMode(session *ChatSession, turn runtimechat.UserTurn) string {
	if session != nil && (session.NoInteractive || session.JSONOutput) {
		return runtimechat.BacktrackModeConversation
	}
	fmt.Printf("该 turn 之后存在文件变更（base_chk=%s）。\n", shortID(turn.BaseCheckpointID))
	fmt.Print("模式 [c=仅对话 / b=对话+代码 / Enter=仅对话]: ")
	reader := backtrackPlainInputReader(session)
	line, err := reader.ReadString('\n')
	if err != nil {
		return runtimechat.BacktrackModeConversation
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "both", "y", "yes":
		return runtimechat.BacktrackModeBoth
	default:
		return runtimechat.BacktrackModeConversation
	}
}
