package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// canOpenChatSkillPicker mirrors the other lease-bound picker gates. Skill
// selection eventually mutates the composer, so it may only begin while the
// unified primary presenter is idle, owns its viewport, and no competing popup
// or alternate screen owns input.
func canOpenChatSkillPicker(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
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

// openChatSkillPicker executes the typed alternate-screen skill selector. The
// lease ends before the composer is touched: the confirmed skill becomes a
// composer draft (`/skill <name> `) only after lease release and primary
// presenter recovery, so the user composes the prompt in the unified composer.
func openChatSkillPicker(session *ChatSession, _ SkillPickerRequest) {
	if !canOpenChatSkillPicker(session) {
		return
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		_ = renderChatCommandResult(session, commandTextResult("错误: Function Catalog: 未初始化"), false)
		return
	}
	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		_ = renderChatCommandResult(session, commandTextResult("错误: Function Catalog: 未初始化"), false)
		return
	}
	skills := filterSkillCatalogEntries(report.Skills, "")
	if len(skills) == 0 {
		_ = renderChatCommandResult(session, commandTextResult("错误: 未找到匹配 skill"), false)
		return
	}

	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "选择 Skill",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开 skill 选择器失败: %w", err)), false)
		return
	}
	if !session.Interaction.postUIAction(ui.OpenSkillPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("skill 选择器状态未提交")), false)
		return
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list.
	session.Interaction.waitUIActorIdle()

	picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        "选择 Skill",
		Subtitle:     "Enter 确认 · Esc 取消",
		EmptyMessage: "没有匹配的 skill",
		ConfirmLabel: "使用选中 skill",
		Items:        buildSkillPickerFullScreenItems(skills),
	}, lease)

	_ = session.Interaction.postUIAction(ui.CloseSkillPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not touch the composer
	// until the actor has observed it.
	session.Interaction.waitUIActorIdle()

	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭 skill 选择器失败: %w", releaseErr)), false)
		return
	}
	if pickErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("skill 选择器失败: %w", pickErr)), false)
		return
	}
	if picked.Cancelled || picked.Index < 0 || picked.Index >= len(skills) {
		_ = renderChatCommandResult(session, commandTextResult("已取消选择 skill"), false)
		return
	}

	selected := skills[picked.Index]
	draft := "/skill " + strings.TrimSpace(selected.FunctionName) + " "
	result := commandTextResult(fmt.Sprintf("已选择 skill: %s\n请在输入区输入 prompt 后按 Enter 执行。", skillCatalogEntryLabel(selected)))
	result.RestoreComposerDraft = draft
	_ = renderChatCommandResult(session, result, false)
	// renderChatCommandResult only commits the document; the composer draft is a
	// typed post-commit effect consumed here (mirroring the /retry dispatch).
	if err := restoreChatRetryDraft(session, draft); err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
	}
}

// buildSkillPickerFullScreenItems builds fullscreen rows for the skill picker.
func buildSkillPickerFullScreenItems(skills []aicliFunctionDescriptorReport) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(skills))
	for _, skill := range skills {
		items = append(items, ui.FullScreenListItem{
			Title:      skillCatalogEntryLabel(skill),
			Detail:     skillCatalogEntryDetail(skill),
			SearchText: skillCatalogEntrySearchText(skill),
		})
	}
	return items
}

// executeStructuredSkillCommand is the unified interactive entry point for
// `/skill <name> <prompt>`. It owns the whole command: resolution, argument
// parsing, authorization and execution all reuse the legacy direct-invoke
// chain, but the result is rendered as one unified command cell instead of raw
// stdout. The invocation-started supplement stays surface-aware (or is skipped
// for non-interactive projections) exactly like the legacy path.
func executeStructuredSkillCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	if session.DisableTools {
		return commandTextResult("错误: 当前会话已禁用 tools；/call、/tool 和 /skill 不可执行"), true
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	requestedName, rawPrompt := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		return commandTextResult("错误: 需要指定 skill 名称\n用法: /skill <name> <prompt> 或 /skill <name> {\"prompt\":\"...\"}"), true
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, requestedName, true)
	if err != nil {
		return commandErrorResult(err), true
	}
	args, err := parseDirectFunctionArgs(rawPrompt, true, resolvedName)
	if err != nil {
		return commandErrorResult(err), true
	}
	args, err = authorizeDirectFunctionInvocation(session, resolvedName, args, !jsonOutput)
	if err != nil {
		return commandErrorResult(err), true
	}

	renderDirectSkillInvocationStarted(session, command, requestedName, resolvedName, args, jsonOutput)
	report, err := executeDirectFunction(session, requestedName, resolvedName, args)
	if err != nil {
		return commandErrorResult(err), true
	}

	text := formatDirectFunctionInvokeReport(report, jsonOutput)
	if text == "" {
		text = fmt.Sprintf("Skill %s 执行完成", resolvedName)
	}
	return commandTextResult(strings.TrimRight(text, "\n")), true
}

// executeStructuredSkillsMenuCommand is the unified interactive entry point for
// /skills. Explicit list queries stay finite documents; bare /skills and
// /skills select open the typed skill picker, whose confirmed selection becomes
// a composer draft. When the picker is unavailable, the menu degrades to the
// catalog report.
func executeStructuredSkillsMenuCommand(session *ChatSession, command string) (CommandResult, bool) {
	query, jsonOutput := extractCommandArgumentOptions(command)
	query = strings.TrimSpace(query)
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return commandTextResult("错误: Function Catalog: 未初始化"), true
	}
	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		return commandTextResult("错误: Function Catalog: 未初始化"), true
	}

	// --json is a finite structured projection: render the JSON payload as one
	// plain command cell instead of falling back to legacy stdout.
	if jsonOutput {
		skills := filterSkillCatalogEntries(report.Skills, query)
		payload := struct {
			Count  int                             `json:"count"`
			Query  string                          `json:"query,omitempty"`
			Skills []aicliFunctionDescriptorReport `json:"skills,omitempty"`
		}{
			Count:  len(skills),
			Query:  query,
			Skills: append([]aicliFunctionDescriptorReport(nil), skills...),
		}
		return commandTextResult(marshalIndentedJSON(payload)), true
	}

	// select/pick/choose and bare /skills open the picker.
	opensPicker := false
	switch strings.ToLower(query) {
	case "select", "pick", "choose":
		opensPicker = true
		query = ""
	case "list", "ls", "status":
		query = ""
	default:
		opensPicker = query == ""
	}

	if opensPicker {
		if canOpenChatSkillPicker(session) {
			return CommandResult{
				Action:         CommandContinue,
				OpenSkillPicker: &SkillPickerRequest{},
			}, true
		}
		// No picker surface: degrade to the full catalog report.
		query = ""
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatSkillCatalogDocument(filterSkillCatalogEntries(report.Skills, query), query)}},
		Action: CommandContinue,
	}, true
}

