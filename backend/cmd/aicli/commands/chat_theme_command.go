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
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type themeCommandAction int

const (
	themeCommandSelect themeCommandAction = iota
	themeCommandStatus
	themeCommandList
	themeCommandPreview
	themeCommandSet
)

type themeCommandRequest struct {
	Action  themeCommandAction
	Palette string
	Mode    string
	Syntax  string
}

func parseThemeCommandRequest(command string) (themeCommandRequest, error) {
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return themeCommandRequest{Action: themeCommandSelect}, nil
	}

	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return themeCommandRequest{Action: themeCommandSelect}, nil
	}
	if len(fields) > 3 {
		return themeCommandRequest{}, fmt.Errorf("无法识别的 /theme 参数: %s", arg)
	}

	// Single-token control verbs.
	if len(fields) == 1 {
		token := strings.ToLower(strings.TrimSpace(fields[0]))
		switch token {
		case "status", "show", "current":
			return themeCommandRequest{Action: themeCommandStatus}, nil
		case "list", "ls", "options":
			return themeCommandRequest{Action: themeCommandList}, nil
		case "preview", "sample", "swatch":
			return themeCommandRequest{Action: themeCommandPreview}, nil
		case "select", "pick", "choose":
			return themeCommandRequest{Action: themeCommandSelect}, nil
		}
	}

	// /theme syntax <name>
	if len(fields) >= 1 && strings.EqualFold(fields[0], "syntax") {
		if len(fields) == 1 {
			return themeCommandRequest{Action: themeCommandStatus}, nil
		}
		syntax := ui.NormalizeSyntaxThemeName(fields[1])
		if syntax == "" {
			return themeCommandRequest{}, fmt.Errorf("未知语法主题: %s", fields[1])
		}
		return themeCommandRequest{Action: themeCommandSet, Syntax: syntax}, nil
	}

	req := themeCommandRequest{Action: themeCommandSet}
	for _, field := range fields {
		token := strings.TrimSpace(field)
		if token == "" {
			continue
		}
		if mode := ui.NormalizeThemeModeName(token); mode != "" && isThemeModeToken(token) {
			if req.Mode != "" {
				return themeCommandRequest{}, fmt.Errorf("重复的明暗模式参数: %s", arg)
			}
			req.Mode = mode
			continue
		}
		if palette := ui.NormalizeThemePresetName(token); palette != "" {
			if req.Palette != "" {
				return themeCommandRequest{}, fmt.Errorf("重复的配色参数: %s", arg)
			}
			req.Palette = palette
			continue
		}
		if syntax := ui.NormalizeSyntaxThemeName(token); syntax != "" && !isThemeModeToken(token) {
			// Only accept curated names as bare tokens to avoid accidental matches.
			if isCuratedSyntaxToken(token) {
				if req.Syntax != "" {
					return themeCommandRequest{}, fmt.Errorf("重复的语法主题参数: %s", arg)
				}
				req.Syntax = syntax
				continue
			}
		}
		return themeCommandRequest{}, fmt.Errorf(
			"未知主题参数: %s（明暗: %s；配色: %s；语法: /theme syntax <name>）",
			token,
			strings.Join(ui.SupportedThemeModeNames(), "|"),
			strings.Join(ui.SupportedThemePresetNames(), "|"),
		)
	}
	if req.Palette == "" && req.Mode == "" && req.Syntax == "" {
		return themeCommandRequest{}, fmt.Errorf("无法识别的 /theme 参数: %s", arg)
	}
	return req, nil
}

func isCuratedSyntaxToken(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, n := range ui.CuratedSyntaxThemeNames() {
		if strings.EqualFold(n, raw) {
			return true
		}
	}
	return false
}

// isThemeModeToken returns true when the raw token is intentionally a mode word
// (not a palette that accidentally normalizes like empty/"default").
func isThemeModeToken(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto", "system", "dark", "night", "black", "light", "day", "white":
		return true
	default:
		return false
	}
}

// handleThemeCommand switches the aicli terminal theme and persists preferences.
func handleThemeCommand(session *ChatSession, command string, noInteractive bool) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	req, err := parseThemeCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /theme [mode|palette|list|status|preview|select]")
		fmt.Printf("  明暗: %s\n", strings.Join(ui.SupportedThemeModeNames(), "|"))
		fmt.Printf("  配色: %s\n", strings.Join(ui.SupportedThemePresetNames(), "|"))
		return false
	}

	switch req.Action {
	case themeCommandStatus:
		printThemeCommandStatus(session)
	case themeCommandList:
		printThemeCommandList(session)
	case themeCommandPreview:
		printThemeCommandPreview(session)
	case themeCommandSelect:
		if noInteractive {
			printThemeCommandStatus(session)
			return false
		}
		palette, mode, syntax, cancelled, err := selectThemeInteractive(session)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if cancelled {
			fmt.Println("已取消，主题未变更")
			return false
		}
		if err := applyThemeCommandSelection(session, palette, mode, syntax); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printThemeCommandStatus(session)
	case themeCommandSet:
		if err := applyThemeCommandSelection(session, req.Palette, req.Mode, req.Syntax); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printThemeCommandStatus(session)
	}
	return false
}

func applyThemeCommandSelection(session *ChatSession, palette string, mode string, syntax string) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}

	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()

	if err := ui.ApplyThemeSelection(palette, mode); err != nil {
		return err
	}
	if strings.TrimSpace(syntax) != "" {
		if err := ui.SetSyntaxTheme(syntax); err != nil {
			return err
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
		fmt.Printf("提示: 主题未变更（mode=%s palette=%s syntax=%s）\n", nextMode, nextPalette, nextSyntax)
		return nil
	}

	persistThemeCommandPreference(session, nextPalette, nextMode, nextSyntax)

	parts := make([]string, 0, 3)
	if previousMode != nextMode || strings.TrimSpace(mode) != "" {
		parts = append(parts, "明暗="+nextMode)
	}
	if previousPalette != nextPalette || strings.TrimSpace(palette) != "" {
		parts = append(parts, "配色="+nextPalette)
	}
	if previousSyntax != nextSyntax || strings.TrimSpace(syntax) != "" {
		parts = append(parts, "语法="+nextSyntax)
	}
	if len(parts) == 0 {
		parts = append(parts, ui.ThemeSelectionDescription())
	}
	fmt.Printf("提示: 已切换主题（%s）\n", strings.Join(parts, ", "))
	return nil
}

func printThemeCommandStatus(session *ChatSession) {
	mode := ui.CurrentThemeModeName()
	palette := ui.CurrentThemeName()
	fmt.Printf("当前明暗: %s", mode)
	if mode == ui.ThemeModeAuto {
		fmt.Printf(" (实际: %s)", ui.CurrentThemeResolvedModeName())
	}
	if desc := ui.ThemeModeDescription(mode); desc != "" {
		fmt.Printf(" — %s", desc)
	}
	fmt.Println()
	fmt.Printf("当前配色: %s", palette)
	if desc := ui.ThemePresetDescription(palette); desc != "" {
		fmt.Printf(" — %s", desc)
	}
	fmt.Println()
	fmt.Printf("当前语法: %s\n", ui.CurrentSyntaxThemeName())
	fmt.Printf("可选明暗: %s\n", strings.Join(ui.SupportedThemeModeNames(), ", "))
	fmt.Printf("可选配色: %s\n", strings.Join(ui.SupportedThemePresetNames(), ", "))
	fmt.Printf("可选语法: %s\n", strings.Join(ui.CuratedSyntaxThemeNames(), ", "))
	if sample := ui.FormatThemePreviewSample(ui.BuildThemePreview(palette, mode)); sample != "" {
		fmt.Printf("预览: %s\n", sample)
	}
	printThemeConfigDefaults(session)
}

func printThemeCommandList(session *ChatSession) {
	currentMode := ui.CurrentThemeModeName()
	currentPalette := ui.CurrentThemeName()
	// Palette previews use the effective light/dark so samples match what users see.
	resolvedMode := ui.CurrentThemeResolvedModeName()

	fmt.Println("明暗模式:")
	for _, name := range ui.SupportedThemeModeNames() {
		marker := ""
		if name == currentMode {
			marker = " (当前)"
		}
		desc := ui.ThemeModeDescription(name)
		if desc != "" {
			fmt.Printf("  - %s%s — %s\n", name, marker, desc)
		} else {
			fmt.Printf("  - %s%s\n", name, marker)
		}
	}

	fmt.Println("配色方案:")
	for _, name := range ui.SupportedThemePresetNames() {
		marker := ""
		if name == currentPalette {
			marker = " (当前)"
		}
		desc := ui.ThemePresetDescription(name)
		preview := ui.BuildThemePreview(name, resolvedMode)
		sample := ui.FormatThemePreviewSample(preview)
		if desc != "" && sample != "" {
			fmt.Printf("  - %s%s — %s\n", name, marker, desc)
			fmt.Printf("      %s\n", sample)
		} else if desc != "" {
			fmt.Printf("  - %s%s — %s\n", name, marker, desc)
		} else {
			fmt.Printf("  - %s%s\n", name, marker)
		}
	}
	printThemeConfigDefaults(session)
}

func printThemeCommandPreview(session *ChatSession) {
	_ = session
	mode := ui.CurrentThemeModeName()
	palette := ui.CurrentThemeName()
	resolved := ui.CurrentThemeResolvedModeName()
	syntax := ui.CurrentSyntaxThemeName()
	fmt.Printf("主题预览: mode=%s", mode)
	if mode == ui.ThemeModeAuto {
		fmt.Printf(" (实际: %s)", resolved)
	}
	fmt.Printf(" palette=%s syntax=%s\n", palette, syntax)

	rich := ui.FormatThemePreviewRich(ui.ThemePreviewOptions{
		Width:       ui.GetTerminalWidth(),
		Palette:     palette,
		Mode:        mode,
		SyntaxTheme: syntax,
		Compact:     false,
	})
	if rich != "" {
		fmt.Println(rich)
	}
	fmt.Println("各配色（按当前有效明暗）:")
	for _, name := range ui.SupportedThemePresetNames() {
		sample := ui.FormatThemePreviewSample(ui.BuildThemePreview(name, resolved))
		marker := ""
		if name == palette {
			marker = " *"
		}
		fmt.Printf("  %s%s: %s\n", name, marker, sample)
	}
}

func printThemeConfigDefaults(session *ChatSession) {
	if session == nil || session.Config == nil || session.Config.AICLI == nil || session.Config.AICLI.Theme == nil {
		fmt.Println("配置默认: (未设置)")
		return
	}
	cfg := session.Config.AICLI.Theme
	configuredName := strings.TrimSpace(cfg.Name)
	configuredMode := strings.TrimSpace(cfg.Mode)
	if configuredName == "" && configuredMode == "" {
		fmt.Println("配置默认: (未设置)")
		return
	}

	parts := make([]string, 0, 2)
	if configuredMode != "" {
		if normalized := ui.NormalizeThemeModeName(configuredMode); normalized != "" {
			parts = append(parts, "mode="+normalized)
		} else {
			parts = append(parts, "mode="+configuredMode+" (无效)")
		}
	}
	if configuredName != "" {
		if normalized := ui.NormalizeThemePresetName(configuredName); normalized != "" {
			parts = append(parts, "palette="+normalized)
		} else {
			parts = append(parts, "palette="+configuredName+" (无效)")
		}
	}
	fmt.Printf("配置默认: %s\n", strings.Join(parts, ", "))
}

// selectThemeInteractive prefers fullscreen live-preview picker; falls back to prompts.
func selectThemeInteractive(session *ChatSession) (palette, mode, syntax string, cancelled bool, err error) {
	fp, fm, fs, ok, ferr := selectThemeFullScreen(session)
	switch {
	case ferr == nil && ok:
		return fp, fm, fs, false, nil
	case ferr == nil && !ok:
		return "", "", "", true, nil
	case ferr != nil && errors.Is(ferr, ui.ErrFullScreenUnavailable):
		// legacy text prompts
	default:
		return "", "", "", false, ferr
	}
	palette, mode, err = selectThemeWithReader(session, bufio.NewReader(os.Stdin))
	if err != nil {
		return "", "", "", false, err
	}
	if palette == "" && mode == "" {
		return "", "", "", true, nil
	}
	return palette, mode, "", false, nil
}

func selectThemeFullScreen(session *ChatSession) (palette, mode, syntax string, ok bool, err error) {
	terminal := resumeFullScreenTerminal(session)
	if !ui.CanUseFullScreenList(terminal) {
		return "", "", "", false, ui.ErrFullScreenUnavailable
	}

	snapPalette := ui.CurrentThemeName()
	snapMode := ui.CurrentThemeModeName()
	snapSyntax := ui.CurrentSyntaxThemeName()

	type themePickKind int
	const (
		pickPalette themePickKind = iota
		pickMode
		pickSyntax
	)
	type themePick struct {
		kind  themePickKind
		value string
	}

	var picks []themePick
	items := make([]ui.FullScreenListItem, 0, 32)
	// Section: modes
	items = append(items, ui.FullScreenListItem{
		Title:      "── 明暗模式 ──",
		Disabled:   true,
		SearchText: "mode",
	})
	picks = append(picks, themePick{}) // placeholder aligned? better track only enabled
	// Rebuild with index map for enabled only.
	items = items[:0]
	picks = picks[:0]

	for _, name := range ui.SupportedThemeModeNames() {
		desc := ui.ThemeModeDescription(name)
		title := name
		if desc != "" {
			title = name + " — " + desc
		}
		if name == snapMode {
			title += " (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "mode",
			SearchText: "mode " + name + " " + desc,
			Preview:    ui.FormatThemePreviewSample(ui.BuildThemePreview(snapPalette, name)),
		})
		picks = append(picks, themePick{kind: pickMode, value: name})
	}
	for _, name := range ui.SupportedThemePresetNames() {
		desc := ui.ThemePresetDescription(name)
		title := name
		if desc != "" {
			title = name + " — " + desc
		}
		if name == snapPalette {
			title += " (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "palette · light/dark compatible",
			SearchText: "palette " + name + " " + desc,
			Preview:    ui.FormatThemePreviewSample(ui.BuildThemePreview(name, snapMode)),
		})
		picks = append(picks, themePick{kind: pickPalette, value: name})
	}
	for _, name := range ui.CuratedSyntaxThemeNames() {
		title := name + " — 语法高亮"
		if name == snapSyntax {
			title += " (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "syntax · builtin",
			SearchText: "syntax " + name,
		})
		picks = append(picks, themePick{kind: pickSyntax, value: name})
	}

	// Working selection accumulates axes during browsing; cancel restores snap.
	workPalette, workMode, workSyntax := snapPalette, snapMode, snapSyntax
	confirmed := false

	surfaceEnabled := session != nil && session.Surface != nil && session.Surface.Enabled()
	if surfaceEnabled {
		session.Surface.Disable()
	}
	defer func() {
		if surfaceEnabled {
			session.Surface.Enable()
		}
		if session != nil && session.Interaction != nil {
			session.Interaction.ResetPromptState()
			session.Interaction.RefreshStatus("")
		}
	}()

	result, err := ui.SelectFullScreenList(context.Background(), terminal, ui.FullScreenListOptions{
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
			// Commit working axes (already applied by selection hooks).
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
	})
	if err != nil {
		_ = ui.ApplyThemeSelection(snapPalette, snapMode)
		_ = ui.SetSyntaxTheme(snapSyntax)
		return "", "", "", false, err
	}
	if result.Cancelled || !confirmed {
		_ = ui.ApplyThemeSelection(snapPalette, snapMode)
		_ = ui.SetSyntaxTheme(snapSyntax)
		return "", "", "", false, nil
	}
	return workPalette, workMode, workSyntax, true, nil
}

func selectThemeWithReader(session *ChatSession, reader *bufio.Reader) (palette string, mode string, err error) {
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	_ = session

	currentMode := ui.CurrentThemeModeName()
	currentPalette := ui.CurrentThemeName()
	modes := ui.SupportedThemeModeNames()
	palettes := ui.SupportedThemePresetNames()

	printChatSelectionSection("选择明暗模式")
	for i, name := range modes {
		label := name
		if desc := ui.ThemeModeDescription(name); desc != "" {
			label = name + " — " + desc
		}
		if name == currentMode {
			printChatSelectionMutedSuffix(fmt.Sprintf("  [%d] %s ", i+1, label), "(当前)")
			continue
		}
		printChatSelectionLine("  [%d] %s", i+1, label)
	}
	printChatSelectionBlankLine()

	selectedMode, err := readThemeOption(reader, modes, currentMode, "明暗模式", true)
	if err != nil {
		return "", "", err
	}

	printChatSelectionSection("选择配色方案")
	// Preview samples for the mode the user just picked (or kept).
	previewMode := selectedMode
	if previewMode == "" {
		previewMode = currentMode
	}
	for i, name := range palettes {
		label := name
		if desc := ui.ThemePresetDescription(name); desc != "" {
			label = name + " — " + desc
		}
		sample := ui.FormatThemePreviewSample(ui.BuildThemePreview(name, previewMode))
		if name == currentPalette {
			printChatSelectionMutedSuffix(fmt.Sprintf("  [%d] %s ", i+1, label), "(当前)")
		} else {
			printChatSelectionLine("  [%d] %s", i+1, label)
		}
		if sample != "" {
			printChatSelectionLine("      %s", sample)
		}
	}
	printChatSelectionBlankLine()

	selectedPalette, err := readThemeOption(reader, palettes, currentPalette, "配色方案", true)
	if err != nil {
		return "", "", err
	}

	// Empty keep-current still applies if either axis changed via number/name entry.
	if selectedMode == currentMode && selectedPalette == currentPalette {
		return currentPalette, currentMode, nil
	}
	return selectedPalette, selectedMode, nil
}

func readThemeOption(reader *bufio.Reader, options []string, current string, label string, allowName bool) (string, error) {
	for {
		printChatSelectionPrompt("请输入%s选项 (或直接回车保持当前): ", label)
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return current, nil
		}
		if num, err := strconv.Atoi(input); err == nil {
			if num >= 1 && num <= len(options) {
				return options[num-1], nil
			}
			printChatSelectionWarning("无效的选择，请重新输入")
			continue
		}
		if !allowName {
			printChatSelectionWarning("请输入编号")
			continue
		}
		// Accept either mode or palette names depending on option list.
		lower := strings.ToLower(input)
		for _, opt := range options {
			if strings.EqualFold(opt, lower) {
				return opt, nil
			}
		}
		// Also accept mode/palette aliases.
		if mode := ui.NormalizeThemeModeName(input); mode != "" && isThemeModeToken(input) {
			for _, opt := range options {
				if opt == mode {
					return mode, nil
				}
			}
		}
		if palette := ui.NormalizeThemePresetName(input); palette != "" {
			for _, opt := range options {
				if opt == palette {
					return palette, nil
				}
			}
		}
		printChatSelectionWarning("未知选项，可选值: %s", strings.Join(options, "|"))
	}
}

func persistThemeCommandPreference(session *ChatSession, palette string, mode string, syntax string) {
	if session == nil || session.Config == nil {
		return
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /theme 偏好失败: %v\n", err)
		return
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
		return
	}
	if _, err := config.UpdateAICLIThemePreferences(configPath, update); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /theme 偏好失败: %v\n", err)
		return
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
}

func themeSlashArgumentCandidates() []chatSlashCompletionCandidate {
	candidates := make([]chatSlashCompletionCandidate, 0, len(ui.SupportedThemePresetNames())+len(ui.SupportedThemeModeNames())+4)
	for _, name := range ui.SupportedThemeModeNames() {
		summary := ui.ThemeModeDescription(name)
		if summary == "" {
			summary = "切换明暗到 " + name
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: name,
			Summary: summary,
			Group:   string(chatSlashCommandGroupBasics),
		})
	}
	for _, name := range ui.SupportedThemePresetNames() {
		summary := ui.ThemePresetDescription(name)
		if summary == "" {
			summary = "切换配色到 " + name
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: name,
			Summary: summary,
			Group:   string(chatSlashCommandGroupBasics),
		})
	}
	candidates = append(candidates,
		chatSlashCompletionCandidate{Command: "list", Summary: "列出明暗与配色（含预览）", Group: string(chatSlashCommandGroupBasics)},
		chatSlashCompletionCandidate{Command: "status", Summary: "查看当前主题", Group: string(chatSlashCommandGroupBasics)},
		chatSlashCompletionCandidate{Command: "preview", Summary: "预览当前与各配色样例", Group: string(chatSlashCommandGroupBasics)},
		chatSlashCompletionCandidate{Command: "select", Summary: "交互选择主题（全屏实时预览）", Group: string(chatSlashCommandGroupBasics)},
		chatSlashCompletionCandidate{Command: "syntax", Summary: "设置语法高亮主题", Group: string(chatSlashCommandGroupBasics)},
	)
	for _, name := range ui.CuratedSyntaxThemeNames() {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: "syntax " + name,
			Summary: "语法高亮 " + name,
			Group:   string(chatSlashCommandGroupBasics),
		})
	}
	return candidates
}
