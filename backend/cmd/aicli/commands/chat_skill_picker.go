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
		session.Surface.LeaseActive() || chatSessionPopupPort(session).HasActivePopup() {
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
//
// 只由 /skills select|pick|choose 显式触发——清单本身不需要它。副屏能力不足时
// 明确报错并给出替代路径，绝不静默返回：静默返回会让用户敲了 /skills select
// 却什么都看不到，也不知道该改用什么。
func openChatSkillPicker(session *ChatSession, _ SkillPickerRequest) {
	if session == nil {
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
		skills := chatSkillCatalogEntries(session, catalog, report, "")
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

// skillPickerUnavailableHint 说明全屏选择器为何不可用并给出替代路径。
// 显式请求的交互失败必须可见——否则用户只会看到“敲了没反应”。
func skillPickerUnavailableHint() string {
	return "当前环境不支持全屏选择器（需要 ANSI TTY、未被其他副屏/弹层占用、且回合已结束）。\n" +
		"改用 /skills 查看技能清单，再用 /skill <name> <prompt> 直接执行。"
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

// chatSkillPickerLeaseHooks binds the shared lease lifecycle
// (chat_picker_common.go) to this picker's UI-actor action identity.
var chatSkillPickerLeaseHooks = chatPickerLeaseHooks{
	Open:  func(leaseID uint64) ui.UIAction { return ui.OpenSkillPicker{LeaseID: leaseID} },
	Close: func(leaseID uint64) ui.UIAction { return ui.CloseSkillPicker{LeaseID: leaseID} },
}

// selectChatSkillPickerList 执行一次"接管备用屏 → 全屏列表 → 释放备用屏"的
// 生命周期：返回结果前必须等 actor 观察到 LeaseReleased，调用方才能安全地
// 碰 composer（或再次开列表）。onToggle 非 nil 时启用 x/X/Delete 键。
func selectChatSkillPickerList(session *ChatSession, skills []aicliFunctionDescriptorReport, onToggle func(index int) error) (ui.FullScreenListResult, error) {
	// 批次 2：租约、open 屏障与 close 序列由统一框架承担；键位导航仍留在全屏
	// 列表原语内，选择结果在闭包中捕获、待租约释放后返回给调用方。
	var picked ui.FullScreenListResult
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "skill.picker",
		Title: "选择 Skill",
		Hooks: chatSkillPickerLeaseHooks,
		Run: func(s *ChatSession, lease ui.ScreenLease) error {
			var pickErr error
			picked, pickErr = ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(s), ui.FullScreenListOptions{
				Title:        "选择 Skill",
				Subtitle:     "Enter 选择 · x 启用/停用 · Esc 取消",
				EmptyMessage: "没有匹配的 skill",
				ConfirmLabel: "使用选中 skill",
				Items:        buildSkillPickerFullScreenItems(skills),
				OnDelete:     onToggle,
			}, lease)
			return pickErr
		},
	})
	if res.Degraded {
		// 与批次 2 之前 opener 取租约失败的文案一致（此时并未进入副屏）。
		return ui.FullScreenListResult{}, fmt.Errorf("打开 skill 选择器失败: %w", errChatPickerScreenUnavailable)
	}
	if res.Err != nil {
		return ui.FullScreenListResult{}, chatPickerScreenErrorText("skill 选择器", res)
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
		return commandTextResult("错误: 当前会话已禁用 tools；/call 和 /skill 不可执行"), true
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
	args, err := parseDirectFunctionArgs(session, rawPrompt, true, resolvedName)
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

// executeStructuredSkillsMenuCommand 是 /skills 的统一入口。
//
// /skills 是只读清单命令，列出**当前会话实际加载**的技能（工作区 .agents/skills、
// 用户 ~/.aicli/skills 等已扫描根，Codex/Claude 兼容格式，profile 与
// disabled_skills 过滤后的结果），并回显这些根。
//
//	/skills                   → 全量清单
//	/skills list|ls|status     → 同上
//	/skills <query>            → 按名称/描述/分类/标签/路径过滤的清单
//	/skills select|pick|choose → 显式打开全屏选择器（交互式选择，不属于清单职责）
//	/skills enable|disable <n> → 启停
//	/skills --json             → 结构化投影
//
// 清单分支没有任何能力门：会话里加载到的技能必须全部可见。此前 bare /skills 会
// 改开全屏选择器、且“选择器不可用”才降级成清单，于是它既不是清单（弹了个
// picker），又可能在能力翻转时什么都不显示。选择是显式动作，只在 /skills select
// 时发生。
func executeStructuredSkillsMenuCommand(session *ChatSession, command string) (CommandResult, bool) {
	query, jsonOutput := extractCommandArgumentOptions(command)
	query = strings.TrimSpace(query)
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	// TUI 路径上能力面是「首帧后异步发现、首个 turn 才挂载」：discover 在后台
	// goroutine 跑，attach 由 ensureChatExecutor 在 turn 入口触发。而 /skills 是
	// 斜杠命令、不经过 turn —— 用户在新会话里先敲 /skills（还没发过消息）时，
	// attach 尚未执行，清单必然显示 total=0，诊断则是 binding=nil +
	// shared_manager{absent} + load=...err=nil，看上去像"加载失败"，实际是
	// "还没挂载"。这里主动补齐挂载（attach 写 session，必须在主 goroutine 上执行），
	// 让读数反映真实能力面而不是时序上的半成品状态；失败时
	// awaitChatCapabilitiesForTurn 会把 error 记进 session.CapabilitiesInitError，
	// 诊断照常复述，不会把真失败伪装成"没挂"。
	_ = awaitChatCapabilitiesForTurn(context.Background(), session)
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

	// 手工重载：/skills reload（重扫技能目录 + 热刷新函数面）。
	if parseSkillsReloadQuery(query) {
		return executeStructuredSkillReloadCommand(session, jsonOutput), true
	}

	// --json is a finite structured projection: render the JSON payload as one
	// plain command cell instead of falling back to legacy stdout.
	if jsonOutput {
		skills := chatSkillCatalogEntries(session, catalog, report, query)
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

	// select/pick/choose 是显式的交互式选择；其余一律是清单。
	switch strings.ToLower(query) {
	case "select", "pick", "choose":
		if !canOpenChatSkillPicker(session) {
			// 显式请求的交互失败必须可见，并给出替代路径。
			return commandTextResult(skillPickerUnavailableHint()), true
		}
		return CommandResult{
			Action: CommandContinue,
			Screen: chatScreenEffectSpec("skill.picker", "选择 Skill", func(s *ChatSession) {
				openChatSkillPicker(s, SkillPickerRequest{})
			}),
		}, true
	case "list", "ls", "status":
		query = ""
	}
	doc := buildChatSkillCatalogDocument(
		chatSkillCatalogEntries(session, catalog, report, query),
		query,
		sessionSkillRoots(session),
	)
	if unifiedDirectInteractiveOutput(session) && strings.TrimSpace(query) == "" {
		// 全量目录走副屏；过滤查询仍是短结果，保持主屏内联单元格。
		return chatScreenDocResult(chatScreenSkillsListSpec(doc)), true
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: doc}},
		Action: CommandContinue,
	}, true
}
