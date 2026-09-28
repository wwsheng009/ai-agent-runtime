package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// canOpenChatThemePicker mirrors the other lease-bound picker gates: the live
// theme preview mutates global theme state while browsing, so it may only begin
// while the unified primary presenter is idle, owns its viewport, and no
// competing popup or alternate screen owns input.
func canOpenChatThemePicker(session *ChatSession) bool {
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

// chatThemePickerLeaseHooks binds the shared lease lifecycle
// (chat_picker_common.go) to this picker's UI-actor action identity.
var chatThemePickerLeaseHooks = chatPickerLeaseHooks{
	Open:  func(leaseID uint64) ui.UIAction { return ui.OpenThemePicker{LeaseID: leaseID} },
	Close: func(leaseID uint64) ui.UIAction { return ui.CloseThemePicker{LeaseID: leaseID} },
}

// openChatThemePicker executes the typed alternate-screen theme selector. The
// lease ends before the confirmed theme is applied, so the primary
// TerminalSession keeps one clear recovery boundary: browsing mutates only the
// working snapshot inside the picker, and the apply step runs after lease
// release and primary presenter recovery.
//
// 批次 5（D-E）：生产点直接返回 chatThemePickerScreenSpec（CommandResult.Screen）；
// 本函数保留为「取租约并完成整个生命周期」的兼容包装（既有直接调用点走同一 Spec）。
func openChatThemePicker(session *ChatSession, request ThemePickerRequest) {
	if !canOpenChatThemePicker(session) {
		return
	}
	chatScreenOpenAndApply(session, chatThemePickerScreenSpec(session, request))
}

// chatThemePickerScreenSpec 构建 /theme select 的副屏 Spec（批次 5/D-E）：
// 交互体在框架租约内运行；选择/取消结果在 AfterClose（close 序列完成、主屏
// 恢复之后）应用，正文与批次 5 之前的 openChatThemePicker 逐行同源。
func chatThemePickerScreenSpec(_ *ChatSession, _ ThemePickerRequest) chatScreenSpec {
	snapPalette := ui.CurrentThemeName()
	snapMode := ui.CurrentThemeModeName()
	snapSyntax := ui.CurrentSyntaxThemeName()

	items, picks := buildThemePickerFullScreenItems(snapPalette, snapMode, snapSyntax)
	workPalette, workMode, workSyntax := snapPalette, snapMode, snapSyntax
	confirmed := false
	var picked ui.FullScreenListResult

	// 批次 5：租约/屏障/close 序列由统一框架承担（chatPickerScreenAsSpec）；
	// 键位导航与实时预览仍留在全屏列表原语内，选择/取消结果在闭包中捕获。
	return chatPickerScreenAsSpec(chatPickerScreen{
		ID:    "theme.picker",
		Title: "选择主题",
		Hooks: chatThemePickerLeaseHooks,
		Run: func(s *ChatSession, lease ui.ScreenLease) error {
			var err error
			picked, err = ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(s), ui.FullScreenListOptions{
				Title:        "选择主题",
				Subtitle:     "上下移动实时预览 · Esc 取消恢复 · Enter 确认并保存",
				ConfirmLabel: "应用主题",
				EmptyMessage: "没有匹配的主题",
				Items:        items,
				OnSelectionChanged: func(index int) {
					if index < 0 || index >= len(picks) {
						return
					}
					p := picks[index]
					switch p.kind {
					case pickMode:
						workMode = p.value
						_ = ui.ApplyThemeSelection("", workMode)
					case pickPalette:
						workPalette = p.value
						_ = ui.ApplyThemeSelection(workPalette, "")
					case pickSyntax:
						workSyntax = p.value
						_ = ui.SetSyntaxTheme(workSyntax)
					}
					if s != nil && s.Interaction != nil {
						s.Interaction.RefreshStatus("")
					}
				},
				OnCancel: func() {
					_ = ui.ApplyThemeSelection(snapPalette, snapMode)
					_ = ui.SetSyntaxTheme(snapSyntax)
				},
				OnConfirm: func(index int) error {
					if index < 0 || index >= len(picks) {
						return fmt.Errorf("无效选择")
					}
					confirmed = true
					return nil
				},
				PreviewForItem: func(index int) string {
					return ui.FormatThemePreviewRich(ui.ThemePreviewOptions{
						Width:       72,
						Palette:     workPalette,
						Mode:        workMode,
						SyntaxTheme: workSyntax,
						Compact:     true,
					})
				},
			}, lease)
			return err
		},
	}, func(session *ChatSession, res chatPickerScreenResult) {
		if res.Degraded {
			// 统一框架未进入副屏（能力不足/租约忙/嵌套）：保持批次 2 之前
			// opener 门禁失败即静默返回的行为。
			return
		}
		if res.Err != nil {
			if res.Phase != chatPickerPhaseOpen {
				// 浏览已经开始：先恢复进入前的主题快照。
				_ = ui.ApplyThemeSelection(snapPalette, snapMode)
				_ = ui.SetSyntaxTheme(snapSyntax)
			}
			_ = renderChatCommandResult(session, commandErrorResult(chatPickerScreenErrorText("主题选择器", res)), false)
			return
		}
		// LeaseReleased 是主屏恢复屏障：租约释放与 actor idle 之后才允许应用
		// 主题或改动会话状态。
		if picked.Cancelled || !confirmed {
			_ = ui.ApplyThemeSelection(snapPalette, snapMode)
			_ = ui.SetSyntaxTheme(snapSyntax)
			_ = renderChatCommandResult(session, commandTextResult("已取消，主题未变更"), false)
			return
		}

		warnings, notice := applyUnifiedThemeCommandSelection(session, workPalette, workMode, workSyntax)
		doc := buildChatThemeStatusDocument(session)
		if notice != "" {
			lines := []string{notice}
			lines = append(lines, strings.Split(strings.TrimRight(ui.RenderDocumentPlain(doc), "\n"), "\n")...)
			doc = textLinesDocument(lines)
		}
		_ = renderChatCommandResult(session, commandResultWithWarnings(doc, warnings...), false)
	})
}

// themeReadOnlyResult 在统一出口把 /theme 的只读报告投影为 ScreenDocument
// （批次 3 尾批）；结构化入口当前只在统一模式派发，非统一分支是防御性保留的
// 旧行为（内联文档单元格）。
func themeReadOnlyResult(session *ChatSession, variant string, doc render.Document) CommandResult {
	if unifiedDirectInteractiveOutput(session) {
		return chatScreenDocResult(chatScreenThemeReadOnlySpec(variant, doc))
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: doc}},
		Action: CommandContinue,
	}
}

// executeStructuredThemeCommand is the unified interactive entry point for all
// /theme variants. It owns the whole command: read-only reports stay finite
// documents, /theme select opens the typed live-preview picker, and explicit
// set variants apply through the unified command cell. When the picker is
// unavailable, select degrades to the read-only status document.
func executeStructuredThemeCommand(session *ChatSession, command string) (CommandResult, bool) {
	request, err := parseThemeCommandRequest(command)
	if err != nil {
		return commandTextResult(themeCommandUsageText(err)), true
	}

	switch request.Action {
	case themeCommandStatus:
		return themeReadOnlyResult(session, "status", buildChatThemeStatusDocument(session)), true
	case themeCommandList:
		return themeReadOnlyResult(session, "list", buildChatThemeListDocument(session)), true
	case themeCommandPreview:
		return themeReadOnlyResult(session, "preview", buildChatThemePreviewDocument()), true
	case themeCommandSelect:
		if !canOpenChatThemePicker(session) {
			return themeReadOnlyResult(session, "status", buildChatThemeStatusDocument(session)), true
		}
		return CommandResult{
			Action: CommandContinue,
			Screen: chatScreenEffectSpec("theme.picker", "选择主题", func(s *ChatSession) {
				openChatThemePicker(s, ThemePickerRequest{})
			}),
		}, true
	case themeCommandSet:
		warnings, notice := applyUnifiedThemeCommandSelection(session, request.Palette, request.Mode, request.Syntax)
		doc := buildChatThemeStatusDocument(session)
		if notice != "" {
			lines := []string{notice}
			lines = append(lines, strings.Split(strings.TrimRight(ui.RenderDocumentPlain(doc), "\n"), "\n")...)
			doc = textLinesDocument(lines)
		}
		return commandResultWithWarnings(doc, warnings...), true
	default:
		return CommandResult{}, false
	}
}

// applyUnifiedThemeCommandSelection applies a resolved /theme mutation without
// writing to stdout/stderr. It mirrors applyThemeCommandSelection but collects
// persist failures as warnings rendered through the unified result cell; the
// returned notice is an ordinary informational line (e.g. "主题未变更"), not an
// error.
func applyUnifiedThemeCommandSelection(session *ChatSession, palette string, mode string, syntax string) ([]error, string) {
	if session == nil {
		return []error{fmt.Errorf("当前没有活动会话")}, ""
	}

	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()

	if err := ui.ApplyThemeSelection(palette, mode); err != nil {
		return []error{err}, ""
	}
	if strings.TrimSpace(syntax) != "" {
		if err := ui.SetSyntaxTheme(syntax); err != nil {
			return []error{err}, ""
		}
	}

	nextPalette := ui.CurrentThemeName()
	nextMode := ui.CurrentThemeModeName()
	nextSyntax := ui.CurrentSyntaxThemeName()

	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
		session.Interaction.RefreshActiveStreamViewport()
	}

	changed := previousPalette != nextPalette || previousMode != nextMode || previousSyntax != nextSyntax
	if !changed {
		return nil, fmt.Sprintf("提示: 主题未变更（mode=%s palette=%s syntax=%s）", nextMode, nextPalette, nextSyntax)
	}

	var warnings []error
	if err := persistUnifiedThemeCommandPreference(session, nextPalette, nextMode, nextSyntax); err != nil {
		warnings = append(warnings, fmt.Errorf("保存 /theme 偏好失败: %w", err))
	}
	return warnings, ""
}

// persistUnifiedThemeCommandPreference writes theme preferences without stderr
// output; errors are returned to be rendered as warnings.
func persistUnifiedThemeCommandPreference(session *ChatSession, palette string, mode string, syntax string) error {
	if session == nil || session.Config == nil {
		return nil
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		return err
	}
	paletteValue := strings.TrimSpace(palette)
	modeValue := strings.TrimSpace(mode)
	syntaxValue := strings.TrimSpace(syntax)
	update := config.AICLIThemePreferenceUpdate{}
	if paletteValue != "" {
		update.Name = &paletteValue
	}
	if modeValue != "" {
		update.Mode = &modeValue
	}
	if syntaxValue != "" {
		update.Syntax = &syntaxValue
	}
	if update.Name == nil && update.Mode == nil && update.Syntax == nil {
		return nil
	}
	if _, err := config.UpdateAICLIThemePreferences(configPath, update); err != nil {
		return err
	}
	if session.Config.AICLI == nil {
		session.Config.AICLI = &config.AICLIConfig{}
	}
	if session.Config.AICLI.Theme == nil {
		session.Config.AICLI.Theme = &config.AICLIThemeConfig{}
	}
	if update.Name != nil {
		session.Config.AICLI.Theme.Name = paletteValue
	}
	if update.Mode != nil {
		session.Config.AICLI.Theme.Mode = modeValue
	}
	if update.Syntax != nil {
		session.Config.AICLI.Theme.Syntax = syntaxValue
	}
	return nil
}
