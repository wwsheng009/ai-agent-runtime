package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// handleBacktrackCommand implements the /backtrack slash command.
//
// Usage:
//
//	/backtrack                         list user turns
//	/backtrack list                    list user turns
//	/backtrack <N>                     preview conversation-only backtrack to user turn N (0-based)
//	/backtrack <N> --apply             apply conversation-only backtrack; prefill composer with anchor text
//	/backtrack <N> --both --apply      apply conversation + code restore
//	/backtrack <N> --edit "新提示"      preview with edit text
//	/backtrack <N> --edit "新提示" --submit
//	                                   apply + auto-submit edited prompt
//
// Alias: /rewind with a numeric first arg routes here (checkpoint ids stay on future restore path).
func handleBacktrackCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredBacktrackQueryCommand(session, command); handled {
			renderErr := renderChatCommandResult(session, result, false)
			if renderErr == nil && result.OpenBacktrackPicker != nil {
				openChatBacktrackPicker(session, *result.OpenBacktrackPicker)
			}
			if renderErr == nil && result.ApplyBacktrack != nil {
				applyUnifiedBacktrackRequest(session, result.ApplyBacktrack.Request)
			}
			return false
		}
		return rejectUnifiedInteractiveLegacyCommand(session, "/backtrack")
	}
	if rejectUnifiedInteractiveLegacyCommand(session, "/backtrack") {
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if session.RuntimeSession == nil {
		fmt.Println("错误: 当前没有可回退的持久化会话")
		return false
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		fmt.Println("错误: 当前会话未初始化本地 runtime host，无法执行 /backtrack")
		return false
	}

	args := strings.TrimSpace(extractCommandArgument(command))
	first := firstToken(args)
	if strings.EqualFold(first, "select") || strings.EqualFold(first, "pick") || strings.EqualFold(first, "ui") {
		return handleInteractiveBacktrackSelect(session)
	}
	if strings.EqualFold(first, "audit") || strings.EqualFold(first, "tombstones") || strings.EqualFold(first, "history") {
		return handleBacktrackAuditList(session)
	}
	if args == "" || strings.EqualFold(first, "list") || strings.EqualFold(first, "ls") {
		// Interactive terminals: empty `/backtrack` opens the picker (Esc 等价入口).
		// Non-interactive / JSON keeps the plain list for scripts.
		if args == "" && session != nil && !session.NoInteractive && !session.JSONOutput {
			return handleInteractiveBacktrackSelect(session)
		}
		if err := listChatBacktrackTurns(session); err != nil {
			fmt.Printf("错误: %v\n", err)
		}
		return false
	}

	req, apply, err := parseChatBacktrackArgs(args)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		printBacktrackUsage()
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

	if !apply {
		result, err := actor.PreviewBacktrack(ctx, req)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printChatBacktrackResult(result, true)
		fmt.Println("提示: 加上 --apply 执行截断；--both 同时恢复文件；--submit 自动发送编辑后的提示")
		return false
	}

	// Apply path: do not auto-submit via actor unless --submit; prefer composer prefill.
	autoSubmit := req.AutoSubmit
	req.AutoSubmit = false
	req.PreviewOnly = false
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
		// Surface transcript 是只追加的：不重放的话，被截断移除的旧消息
		// 会一直留在界面上，回退看起来"没有生效"。这里按 /resume 的约定
		// 把截断后的 canonical 历史重放到 transcript。
		replayVisibleChatHistoryAfterTruncation(session, fmt.Sprintf("已回退到 user turn %d", result.UserTurnIndex))
	}

	composerPrompt := ""
	if result != nil {
		composerPrompt = strings.TrimSpace(result.ComposerPrompt)
		if composerPrompt == "" {
			composerPrompt = strings.TrimSpace(result.EditedPrompt)
		}
	}

	if autoSubmit {
		if composerPrompt == "" {
			fmt.Println("警告: --submit 需要非空 edit 或锚点文本，已跳过自动发送")
			return false
		}
		// Re-submit through the actor so approvals/events stay on the normal path.
		submitReq := runtimechat.BacktrackRequest{
			// History already truncated; submit as a normal prompt.
		}
		_ = submitReq
		runResult, submitErr := actor.SubmitPrompt(ctx, composerPrompt, nil)
		if submitErr != nil {
			fmt.Printf("错误: 自动发送失败: %v\n", submitErr)
			_ = restoreChatRetryDraft(session, composerPrompt)
			return false
		}
		if syncErr := syncRuntimeSessionBackIntoCLI(session); syncErr != nil {
			fmt.Printf("警告: 自动发送后同步 CLI 会话失败: %v\n", syncErr)
		}
		if runResult != nil && strings.TrimSpace(runResult.Output) != "" {
			fmt.Printf("提示: 已自动发送编辑后的提示（output=%d chars）\n", len(runResult.Output))
		} else {
			fmt.Println("提示: 已自动发送编辑后的提示")
		}
		return false
	}

	if composerPrompt != "" {
		if err := restoreChatRetryDraft(session, composerPrompt); err != nil {
			fmt.Printf("提示: 历史已截断；composer 预填失败: %v\n", err)
			fmt.Printf("可手动重发: %s\n", summarizeMemoryNote(composerPrompt, 160))
		} else {
			fmt.Println("提示: 原提示/编辑文本已预填到输入区，检查后按 Enter 发送")
		}
	}
	return false
}

// executeStructuredBacktrackQueryCommand accepts finite read-only reports and
// typed backtrack effects. Direct --apply/--submit carries its fully parsed
// request to dispatch, which runs the destructive canonical-replacement
// transaction without ever falling through to the legacy stdout handler.
// Bare /backtrack and its selection aliases open the typed picker when the
// surface allows it, and otherwise degrade to the finite turn list instead of
// the legacy migration fence (mirroring /skills' catalog degradation).
func executeStructuredBacktrackQueryCommand(session *ChatSession, command string) (CommandResult, bool) {
	args := strings.TrimSpace(extractCommandArgument(command))
	first := strings.ToLower(firstToken(args))
	isBacktrack := commandMatches(strings.ToLower(strings.TrimSpace(command)), "/backtrack")
	// Preserve /rewind's legacy checkpoint-id namespace until that command has
	// its own semantic action model. Only its established list alias is a safe
	// read-only projection here.
	if !isBacktrack && first != "list" && first != "ls" {
		return CommandResult{}, false
	}
	if args == "" || first == "select" || first == "pick" || first == "ui" {
		if isBacktrack {
			if canOpenChatBacktrackPicker(session) {
				return newBacktrackPickerCommandResult(), true
			}
			// No picker surface (non-TTY, run active, popup/lease held, or
			// runtime host not ready): degrade to the finite read-only turn
			// list so the bare command stays on the unified command cell
			// instead of being rejected as an unmigrated legacy writer.
			return executeStructuredBacktrackTurnsQuery(session), true
		}
		return CommandResult{}, false
	}

	if first == "list" || first == "ls" {
		return executeStructuredBacktrackTurnsQuery(session), true
	}
	if first == "audit" || first == "tombstones" || first == "history" {
		return executeStructuredBacktrackAuditQuery(session), true
	}

	req, apply, err := parseChatBacktrackArgs(args)
	if err != nil {
		if commandMatches(strings.ToLower(strings.TrimSpace(command)), "/rewind") {
			return CommandResult{}, false
		}
		return commandTextResult(buildBacktrackUsageText(err)), true
	}
	if apply {
		return CommandResult{
			Action:         CommandContinue,
			ApplyBacktrack: &BacktrackApplyRequest{Request: req},
		}, true
	}
	return executeStructuredBacktrackPreviewQuery(session, req), true
}

func newBacktrackPickerCommandResult() CommandResult {
	return CommandResult{
		Action:              CommandContinue,
		OpenBacktrackPicker: &BacktrackPickerRequest{},
	}
}

func executeStructuredBacktrackTurnsQuery(session *ChatSession) CommandResult {
	actor, err := backtrackQueryActor(session)
	if err != nil {
		return commandErrorResult(err)
	}
	turns, err := actor.ListTurns(backtrackQueryContext(session))
	if err != nil {
		return commandErrorResult(err)
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatBacktrackTurnsDocument(turns)}},
		Action: CommandContinue,
	}
}

func executeStructuredBacktrackAuditQuery(session *ChatSession) CommandResult {
	actor, err := backtrackQueryActor(session)
	if err != nil {
		return commandErrorResult(err)
	}
	entries, err := actor.ListBacktrackAudit(backtrackQueryContext(session))
	if err != nil {
		return commandErrorResult(err)
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatBacktrackAuditDocument(entries)}},
		Action: CommandContinue,
	}
}

func executeStructuredBacktrackPreviewQuery(session *ChatSession, req runtimechat.BacktrackRequest) CommandResult {
	actor, err := backtrackQueryActor(session)
	if err != nil {
		return commandErrorResult(err)
	}
	result, err := actor.PreviewBacktrack(backtrackQueryContext(session), req)
	if err != nil {
		return commandErrorResult(err)
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatBacktrackPreviewDocument(result)}},
		Action: CommandContinue,
	}
}

func backtrackQueryContext(session *ChatSession) context.Context {
	if session != nil && session.cancelCtx != nil {
		return session.cancelCtx
	}
	return context.Background()
}

func backtrackQueryActor(session *ChatSession) (*runtimechat.SessionActor, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	if session.RuntimeSession == nil {
		return nil, fmt.Errorf("当前没有可回退的持久化会话")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return nil, fmt.Errorf("当前会话未初始化本地 runtime host，无法读取 backtrack 信息")
	}
	return chatActorForSession(backtrackQueryContext(session), session)
}

func buildChatBacktrackTurnsDocument(turns []runtimechat.UserTurn) render.Document {
	if len(turns) == 0 {
		return render.SingleLineDoc(render.TextSpan("当前会话没有可回退的 user turn"))
	}
	lines := []string{fmt.Sprintf("User turns（共 %d，0-based index）:", len(turns))}
	for _, turn := range turns {
		flags := make([]string, 0, 2)
		if turn.HasLaterMutation {
			flags = append(flags, "has_later_mutation")
		}
		if turn.BaseCheckpointID != "" {
			flags = append(flags, "base_chk="+shortID(turn.BaseCheckpointID))
		}
		flagText := ""
		if len(flags) > 0 {
			flagText = " [" + strings.Join(flags, ", ") + "]"
		}
		lines = append(lines, fmt.Sprintf("  [%d] msg#%d%s %s", turn.Index, turn.MessageIndex, flagText, turn.Preview))
	}
	lines = append(lines, "用法: /backtrack <index> [--apply] [--both] [--edit \"...\"] [--submit]")
	return textLinesDocument(lines)
}

func buildChatBacktrackAuditDocument(entries []runtimechat.BacktrackTombstone) render.Document {
	if len(entries) == 0 {
		return render.SingleLineDoc(render.TextSpan("backtrack audit: (empty)"))
	}
	lines := []string{fmt.Sprintf("backtrack audit: %d entries", len(entries))}
	for index, entry := range entries {
		lines = append(lines, formatBacktrackTombstoneLines(index, entry)...)
	}
	return textLinesDocument(lines)
}

func buildChatBacktrackPreviewDocument(result *runtimechat.BacktrackResult) render.Document {
	lines := formatChatBacktrackResultLines(result, true)
	lines = append(lines, "提示: 加上 --apply 执行截断；--both 同时恢复文件；--submit 自动发送编辑后的提示")
	return textLinesDocument(lines)
}

func buildBacktrackUsageText(err error) string {
	lines := []string{"错误: " + err.Error()}
	lines = append(lines, backtrackUsageLines()...)
	return strings.Join(lines, "\n")
}

func listChatBacktrackTurns(session *ChatSession) error {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredBacktrackTurnsQuery(session), false)
		return nil
	}
	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	actor, err := chatActorForSession(ctx, session)
	if err != nil {
		return err
	}
	turns, err := actor.ListTurns(ctx)
	if err != nil {
		return err
	}
	if len(turns) == 0 {
		fmt.Println("当前会话没有可回退的 user turn")
		return nil
	}
	fmt.Printf("User turns（共 %d，0-based index）:\n", len(turns))
	for _, turn := range turns {
		flags := make([]string, 0, 2)
		if turn.HasLaterMutation {
			flags = append(flags, "has_later_mutation")
		}
		if turn.BaseCheckpointID != "" {
			flags = append(flags, "base_chk="+shortID(turn.BaseCheckpointID))
		}
		flagText := ""
		if len(flags) > 0 {
			flagText = " [" + strings.Join(flags, ", ") + "]"
		}
		fmt.Printf("  [%d] msg#%d%s %s\n", turn.Index, turn.MessageIndex, flagText, turn.Preview)
	}
	fmt.Println("用法: /backtrack <index> [--apply] [--both] [--edit \"...\"] [--submit]")
	return nil
}

func parseChatBacktrackArgs(args string) (runtimechat.BacktrackRequest, bool, error) {
	tokens := tokenizeBacktrackArgs(args)
	if len(tokens) == 0 {
		return runtimechat.BacktrackRequest{}, false, fmt.Errorf("缺少 user turn index")
	}

	// First token must be turn index (0-based). Support 1-based with trailing '#'? No — stick to plan.
	idx, err := strconv.Atoi(tokens[0])
	if err != nil {
		return runtimechat.BacktrackRequest{}, false, fmt.Errorf("user turn index 必须是整数，收到 %q", tokens[0])
	}
	if idx < 0 {
		return runtimechat.BacktrackRequest{}, false, fmt.Errorf("user turn index 不能为负数")
	}

	req := runtimechat.BacktrackRequest{
		UserTurnIndex: runtimechat.IntPtr(idx),
		Mode:          runtimechat.BacktrackModeConversation,
		PreviewOnly:   true,
	}
	apply := false

	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		switch strings.ToLower(tok) {
		case "--apply", "-a", "apply":
			apply = true
			req.PreviewOnly = false
		case "--both", "both":
			req.Mode = runtimechat.BacktrackModeBoth
		case "--code", "code":
			req.Mode = runtimechat.BacktrackModeCode
		case "--conversation", "conversation":
			req.Mode = runtimechat.BacktrackModeConversation
		case "--submit", "submit":
			req.AutoSubmit = true
			apply = true
			req.PreviewOnly = false
		case "--include-anchor", "include-anchor":
			req.IncludeAnchor = true
		case "--edit", "-e", "edit":
			if i+1 >= len(tokens) {
				return runtimechat.BacktrackRequest{}, false, fmt.Errorf("--edit 需要文本参数")
			}
			i++
			req.EditPrompt = tokens[i]
		default:
			// Allow --edit=text form.
			if strings.HasPrefix(strings.ToLower(tok), "--edit=") {
				req.EditPrompt = strings.TrimSpace(tok[len("--edit="):])
				continue
			}
			return runtimechat.BacktrackRequest{}, false, fmt.Errorf("未知参数: %s", tok)
		}
	}

	if req.AutoSubmit && strings.TrimSpace(req.EditPrompt) == "" {
		// auto-submit without edit uses original anchor text; allowed by runtime.
	}
	return req, apply, nil
}

func tokenizeBacktrackArgs(args string) []string {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil
	}
	var tokens []string
	var b strings.Builder
	inQuote := false
	quoteChar := byte(0)
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tokens = append(tokens, b.String())
		b.Reset()
	}
	for i := 0; i < len(args); i++ {
		ch := args[i]
		if inQuote {
			if ch == quoteChar {
				inQuote = false
				continue
			}
			b.WriteByte(ch)
			continue
		}
		switch ch {
		case '"', '\'':
			inQuote = true
			quoteChar = ch
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			b.WriteByte(ch)
		}
	}
	flush()
	return tokens
}

func handleBacktrackAuditList(session *ChatSession) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredBacktrackAuditQuery(session), false)
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if session.RuntimeSession == nil {
		fmt.Println("错误: 当前没有可回退的持久化会话")
		return false
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		fmt.Println("错误: 当前会话未初始化本地 runtime host，无法读取 backtrack audit")
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
	entries, err := actor.ListBacktrackAudit(ctx)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if len(entries) == 0 {
		fmt.Println("backtrack audit: (empty)")
		return false
	}
	fmt.Printf("backtrack audit: %d entries\n", len(entries))
	for i, entry := range entries {
		printBacktrackTombstone(i, entry)
	}
	return false
}

func printBacktrackTombstone(index int, entry runtimechat.BacktrackTombstone) {
	when := entry.CreatedAt.UTC().Format(time.RFC3339)
	if entry.CreatedAt.IsZero() {
		when = "-"
	}
	fmt.Printf("  [%d] %s mode=%s turn=%d msg#%d removed_msgs=%d removed_turns=%d truncate_to=%d\n",
		index,
		shortID(entry.ID),
		entry.Mode,
		entry.UserTurnIndex,
		entry.MessageIndex,
		entry.RemovedMessageCount,
		entry.RemovedUserTurns,
		entry.TruncatedToMessageCount,
	)
	fmt.Printf("      at=%s\n", when)
	if strings.TrimSpace(entry.AnchorPreview) != "" {
		fmt.Printf("      anchor: %s\n", summarizeMemoryNote(entry.AnchorPreview, 100))
	}
	if entry.BaseCheckpointID != "" {
		fmt.Printf("      base_checkpoint: %s\n", shortID(entry.BaseCheckpointID))
	}
}

func formatBacktrackTombstoneLines(index int, entry runtimechat.BacktrackTombstone) []string {
	when := entry.CreatedAt.UTC().Format(time.RFC3339)
	if entry.CreatedAt.IsZero() {
		when = "-"
	}
	lines := []string{fmt.Sprintf(
		"  [%d] %s mode=%s turn=%d msg#%d removed_msgs=%d removed_turns=%d truncate_to=%d",
		index,
		shortID(entry.ID),
		entry.Mode,
		entry.UserTurnIndex,
		entry.MessageIndex,
		entry.RemovedMessageCount,
		entry.RemovedUserTurns,
		entry.TruncatedToMessageCount,
	)}
	lines = append(lines, "      at="+when)
	if anchor := strings.TrimSpace(entry.AnchorPreview); anchor != "" {
		lines = append(lines, "      anchor: "+summarizeMemoryNote(anchor, 100))
	}
	if entry.BaseCheckpointID != "" {
		lines = append(lines, "      base_checkpoint: "+shortID(entry.BaseCheckpointID))
	}
	return lines
}

func printChatBacktrackResult(result *runtimechat.BacktrackResult, preview bool) {
	if result == nil {
		return
	}
	kind := "apply"
	if preview || result.PreviewOnly {
		kind = "preview"
	}
	fmt.Printf("backtrack %s: turn=%d msg#%d mode=%s truncate_to=%d removed_msgs=%d removed_turns=%d\n",
		kind,
		result.UserTurnIndex,
		result.MessageIndex,
		result.Mode,
		result.TruncatedToMessageCount,
		result.RemovedMessageCount,
		result.RemovedUserTurns,
	)
	if result.AnchorPreview != "" {
		fmt.Printf("  anchor: %s\n", result.AnchorPreview)
	}
	if strings.TrimSpace(result.EditedPrompt) != "" {
		fmt.Printf("  edit: %s\n", summarizeMemoryNote(result.EditedPrompt, 120))
	}
	if strings.TrimSpace(result.ComposerPrompt) != "" {
		fmt.Printf("  composer: %s\n", summarizeMemoryNote(result.ComposerPrompt, 120))
	}
	if result.BaseCheckpointID != "" {
		fmt.Printf("  base_checkpoint: %s\n", shortID(result.BaseCheckpointID))
	}
	if result.Tombstone != nil {
		fmt.Printf("  tombstone: %s removed_msgs=%d removed_turns=%d\n",
			shortID(result.Tombstone.ID),
			result.Tombstone.RemovedMessageCount,
			result.Tombstone.RemovedUserTurns,
		)
	}
	if result.CodeRestore != nil {
		fmt.Printf("  code_restore: chk=%s applied=%d errors=%d\n",
			shortID(result.CodeRestore.CheckpointID),
			len(result.CodeRestore.AppliedPaths),
			len(result.CodeRestore.Errors),
		)
		for _, path := range result.CodeRestore.AppliedPaths {
			fmt.Printf("    + %s\n", path)
		}
		for _, e := range result.CodeRestore.Errors {
			fmt.Printf("    ! %s\n", e)
		}
	}
	for _, w := range result.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
}

func formatChatBacktrackResultLines(result *runtimechat.BacktrackResult, preview bool) []string {
	if result == nil {
		return nil
	}
	kind := "apply"
	if preview || result.PreviewOnly {
		kind = "preview"
	}
	lines := []string{fmt.Sprintf(
		"backtrack %s: turn=%d msg#%d mode=%s truncate_to=%d removed_msgs=%d removed_turns=%d",
		kind,
		result.UserTurnIndex,
		result.MessageIndex,
		result.Mode,
		result.TruncatedToMessageCount,
		result.RemovedMessageCount,
		result.RemovedUserTurns,
	)}
	if result.AnchorPreview != "" {
		lines = append(lines, "  anchor: "+result.AnchorPreview)
	}
	if edited := strings.TrimSpace(result.EditedPrompt); edited != "" {
		lines = append(lines, "  edit: "+summarizeMemoryNote(edited, 120))
	}
	if composer := strings.TrimSpace(result.ComposerPrompt); composer != "" {
		lines = append(lines, "  composer: "+summarizeMemoryNote(composer, 120))
	}
	if result.BaseCheckpointID != "" {
		lines = append(lines, "  base_checkpoint: "+shortID(result.BaseCheckpointID))
	}
	if result.Tombstone != nil {
		lines = append(lines, fmt.Sprintf(
			"  tombstone: %s removed_msgs=%d removed_turns=%d",
			shortID(result.Tombstone.ID),
			result.Tombstone.RemovedMessageCount,
			result.Tombstone.RemovedUserTurns,
		))
	}
	if result.CodeRestore != nil {
		lines = append(lines, fmt.Sprintf(
			"  code_restore: chk=%s applied=%d errors=%d",
			shortID(result.CodeRestore.CheckpointID),
			len(result.CodeRestore.AppliedPaths),
			len(result.CodeRestore.Errors),
		))
		for _, path := range result.CodeRestore.AppliedPaths {
			lines = append(lines, "    + "+path)
		}
		for _, err := range result.CodeRestore.Errors {
			lines = append(lines, "    ! "+err)
		}
	}
	for _, warning := range result.Warnings {
		lines = append(lines, "  warning: "+warning)
	}
	return lines
}

func printBacktrackUsage() {
	fmt.Println("用法:")
	fmt.Println("  /backtrack                 # 交互选择（Esc 空输入等价）")
	fmt.Println("  /backtrack list            # 列出 user turns")
	fmt.Println("  /backtrack select          # 强制打开选择器")
	fmt.Println("  /backtrack audit           # 列出 durable tombstone 审计摘要")
	fmt.Println("  /backtrack <user_turn_index> [--apply] [--both|--code] [--edit \"text\"] [--submit]")
	fmt.Println("  /rewind <user_turn_index> ...   # 数字参数时等价于 /backtrack")
}

func backtrackUsageLines() []string {
	return []string{
		"用法:",
		"  /backtrack                 # 交互选择（Esc 空输入等价）",
		"  /backtrack list            # 列出 user turns",
		"  /backtrack select          # 强制打开选择器",
		"  /backtrack audit           # 列出 durable tombstone 审计摘要",
		"  /backtrack <user_turn_index> [--apply] [--both|--code] [--edit \"text\"] [--submit]",
		"  /rewind <user_turn_index> ...   # 数字参数时等价于 /backtrack",
	}
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
