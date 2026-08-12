package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
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

// openChatThemePicker executes the typed alternate-screen theme selector. The
// lease ends before the confirmed theme is applied, so the primary
// TerminalSession keeps one clear recovery boundary: browsing mutates only the
// working snapshot inside the picker, and the apply step runs after lease
// release and primary presenter recovery.
func openChatThemePicker(session *ChatSession, _ ThemePickerRequest) {
	if !canOpenChatThemePicker(session) {
		return
	}

	snapPalette := ui.CurrentThemeName()
	snapMode := ui.CurrentThemeModeName()
	snapSyntax := ui.CurrentSyntaxThemeName()

	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "选择主题",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开主题选择器失败: %w", err)), false)
		return
	}
	if !session.Interaction.postUIAction(ui.OpenThemePicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("主题选择器状态未提交")), false)
		return
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list.
	if !session.Interaction.waitUIActorIdleBounded("open theme picker") {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("主题选择器渲染未就绪")), false)
		return
	}

	items, picks := buildThemePickerFullScreenItems(snapPalette, snapMode, snapSyntax)
	workPalette, workMode, workSyntax := snapPalette, snapMode, snapSyntax
	confirmed := false

	result, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
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
			if session != nil && session.Interaction != nil {
				session.Interaction.RefreshStatus("")
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

	_ = session.Interaction.postUIAction(ui.CloseThemePicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not apply the theme or
	// mutate session state until the actor has observed it.
	if !session.Interaction.waitUIActorIdleBounded("close theme picker") {
		_ = ui.ApplyThemeSelection(snapPalette, snapMode)
		_ = ui.SetSyntaxTheme(snapSyntax)
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("主题选择器关闭未就绪")), false)
		return
	}

	if releaseErr != nil {
		_ = ui.ApplyThemeSelection(snapPalette, snapMode)
		_ = ui.SetSyntaxTheme(snapSyntax)
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭主题选择器失败: %w", releaseErr)), false)
		return
	}
	if pickErr != nil {
		_ = ui.ApplyThemeSelection(snapPalette, snapMode)
		_ = ui.SetSyntaxTheme(snapSyntax)
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("主题选择器失败: %w", pickErr)), false)
		return
	}
	if result.Cancelled || !confirmed {
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
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatThemeStatusDocument(session)}},
			Action: CommandContinue,
		}, true
	case themeCommandList:
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatThemeListDocument(session)}},
			Action: CommandContinue,
		}, true
	case themeCommandPreview:
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatThemePreviewDocument()}},
			Action: CommandContinue,
		}, true
	case themeCommandSelect:
		if !canOpenChatThemePicker(session) {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatThemeStatusDocument(session)}},
				Action: CommandContinue,
			}, true
		}
		return CommandResult{
			Action:          CommandContinue,
			OpenThemePicker: &ThemePickerRequest{},
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
