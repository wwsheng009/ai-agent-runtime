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

	for {
		report := buildFunctionCatalogReport(catalog)
		if report == nil {
			_ = renderChatCommandResult(session, commandTextResult("错误: Function Catalog: 未初始化"), false)
			return
		}
		skills := filterSkillCatalogEntries(report.Skills, "")
		skills = filterUserInvocableSkillEntries(catalog, skills)
		skills = buildSkillPickerCatalogEntries(session, skills)
		if len(skills) == 0 {
			_ = renderChatCommandResult(session, commandTextResult("错误: 未找到匹配 skill"), false)
			return
		}

		toggleIndex := -1
		picked, pickErr := selectChatSkillPickerList(session, skills, func(index int) error {
			toggleIndex = index
			return nil
		})
		if pickErr != nil {
			_ = renderChatCommandResult(session, commandErrorResult(pickErr), false)
			return
		}

		// x/X/Delete：停用/启用高亮行，落盘 + 热刷新后带着新状态重开列表。
		if picked.DeleteRequested {
			if toggleIndex < 0 || toggleIndex >= len(skills) {
				continue
			}
			entry := skills[toggleIndex]
			message, toggleErr := runSkillToggleCommand(session, entry.Disabled, skillCatalogEntryLabel(entry))
			if toggleErr != nil {
				_ = renderChatCommandResult(session, commandErrorResult(toggleErr), false)
				return
			}
			_ = renderChatCommandResult(session, commandTextResult(message), false)
			continue
		}

		if picked.Cancelled || picked.Index < 0 || picked.Index >= len(skills) {
			_ = renderChatCommandResult(session, commandTextResult("已取消选择 skill"), false)
			return
		}

		selected := skills[picked.Index]
		if selected.Disabled {
			// 停用行不可选中：提示后重开列表（Enter 不是启停键，避免误操作）。
			_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("skill %s 已停用；按 x 键启用后再选择", skillCatalogEntryLabel(selected))), false)
			continue
		}

		draft := "/skill " + strings.TrimSpace(selected.FunctionName) + " "
		result := commandTextResult(fmt.Sprintf("已选择 skill: %s\n请在输入区输入 prompt 后按 Enter 执行。", skillCatalogEntryLabel(selected)))
		result.RestoreComposerDraft = draft
		_ = renderChatCommandResult(session, result, false)
		// renderChatCommandResult only commits the document; the composer draft is a
		// typed post-commit effect consumed here (mirroring the /retry dispatch).
		if err := restoreChatRetryDraft(session, draft); err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
		}
		return
	}
}

// buildSkillPickerFullScreenItems builds fullscreen rows for the skill picker.
func buildSkillPickerFullScreenItems(skills []aicliFunctionDescriptorReport) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(skills))
	for _, skill := range skills {
		item := ui.FullScreenListItem{
			Title:      skillCatalogEntryLabel(skill),
			Detail:     skillCatalogEntryDetail(skill),
			SearchText: skillCatalogEntrySearchText(skill),
		}
		if skill.Disabled {
			// 刻意不设 item.Disabled：列表会跳过停用行，x 键就再也够不到它，
			// 等于"停用了却启不回来"。改用可见标记 + Enter 时的提示。
			item.Leading = "已停用"
			item.Detail = "按 x 启用"
		}
		items = append(items, item)
	}
	return items
}

// selectChatSkillPickerList 执行一次"接管备用屏 → 全屏列表 → 释放备用屏"的
// 生命周期：返回结果前必须等 actor 观察到 LeaseReleased，调用方才能安全地
// 碰 composer（或再次开列表）。onToggle 非 nil 时启用 x/X/Delete 键。
func selectChatSkillPickerList(session *ChatSession, skills []aicliFunctionDescriptorReport, onToggle func(index int) error) (ui.FullScreenListResult, error) {
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "选择 Skill",
	})
	if err != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("打开 skill 选择器失败: %w", err)
	}
	if !session.Interaction.postUIAction(ui.OpenSkillPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		return ui.FullScreenListResult{}, fmt.Errorf("skill 选择器状态未提交")
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list.
	if !session.Interaction.waitUIActorIdleBounded("open skill picker") {
		_ = lease.Release(context.Background())
		return ui.FullScreenListResult{}, fmt.Errorf("skill 选择器渲染未就绪")
	}

	picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        "选择 Skill",
		Subtitle:     "Enter 选择 · x 启用/停用 · Esc 取消",
		EmptyMessage: "没有匹配的 skill",
		ConfirmLabel: "使用选中 skill",
		Items:        buildSkillPickerFullScreenItems(skills),
		OnDelete:     onToggle,
	}, lease)

	_ = session.Interaction.postUIAction(ui.CloseSkillPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not touch the composer
	// until the actor has observed it.
	if !session.Interaction.waitUIActorIdleBounded("close skill picker") {
		return ui.FullScreenListResult{}, fmt.Errorf("skill 选择器关闭未就绪")
	}
	if releaseErr != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("关闭 skill 选择器失败: %w", releaseErr)
	}
	if pickErr != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("skill 选择器失败: %w", pickErr)
	}
	return picked, nil
}

// executeStructuredSkillCommand is the unified interactive entry point for
// `/skill <name> <prompt>`. By default it no longer executes the skill: it
// resolves and validates the invocation and returns a SendSkillTurn effect, so
// dispatch submits a normal chat turn whose per-turn pin injects the skill's
// ProgramGuide and overlays the skill function plus its declared programs onto
// the turn function surface — the model then chooses which programs to call.
// `/skill --direct <name> <prompt>` keeps the legacy deterministic path
// (executeDirectFunction rendered as one unified command cell).
func executeStructuredSkillCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	if session.DisableTools {
		return commandTextResult("错误: 当前会话已禁用 tools；/call、/tool 和 /skill 不可执行"), true
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	payload, directRequested := stripSkillDirectOption(payload)
	requestedName, rawPrompt := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		return commandTextResult("错误: 需要指定 skill 名称\n用法: /skill [--direct] <name> <prompt> 或 /skill <name> {\"prompt\":\"...\"}"), true
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, requestedName, true)
	if err != nil {
		return commandErrorResult(err), true
	}
	args, err := parseDirectFunctionArgs(rawPrompt, true, resolvedName)
	if err != nil {
		return commandErrorResult(err), true
	}

	if directRequested {
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

	// 默认路径：不渲染命令单元、不直执。登记一次性 pin 后由 dispatch 经既有 send
	// 管线提交普通 chat 回合；pin 只属于这一个回合。
	return CommandResult{
		Action: CommandContinue,
		SendSkillTurn: &SendSkillTurnRequest{
			SkillName:     resolvedName,
			Prompt:        rawPrompt,
			VisiblePrompt: buildSkillTurnVisiblePrompt(requestedName, rawPrompt),
		},
	}, true
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

	// per-skill 启停：/skills disable|enable <name>（写配置 + 运行面热刷新）。
	if enable, name, ok := parseSkillToggleQuery(query); ok {
		return executeStructuredSkillToggleCommand(session, enable, name, jsonOutput), true
	}

	// --json is a finite structured projection: render the JSON payload as one
	// plain command cell instead of falling back to legacy stdout.
	if jsonOutput {
		skills := filterSkillCatalogEntries(report.Skills, query)
		skills = filterUserInvocableSkillEntries(catalog, skills)
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
				Action:          CommandContinue,
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
