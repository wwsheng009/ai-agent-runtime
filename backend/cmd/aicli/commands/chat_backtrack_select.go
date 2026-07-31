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
func handleInteractiveBacktrackSelect(session *ChatSession) bool {
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
