package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

type transferExportWizardDefaults struct {
	Target      string
	SessionID   string
	Compression string
	BundleDir   string
	CWD         string
	SessionDir  string
	User        string
	Title       string
	TargetUser  string
}

func runTransferExportWizard(cmd *cobra.Command, getConfig func() *config.Config, skipConfirm bool) int {
	output := structuredOutputOptions{Format: "text"}
	terminal := ui.NewTerminal()
	if !ui.CanUseFullScreenList(terminal) {
		printTransferError("transfer export", output, fmt.Errorf("交互式导出需要可用的 ANSI 交互终端；请直接使用 transfer export 参数或 --json"))
		return transferExitUsage
	}
	defaults, err := transferExportWizardDefaultsFromFlags(cmd)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	target, cancelled, err := transferExportWizardSelectTarget(ctx, terminal, defaults.Target)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	if cancelled {
		fmt.Println("已取消。")
		return transferExitOK
	}

	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	sessions, err := transferExportWizardSessions(ctx, cfg, defaults.SessionDir, defaults.User)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	if len(sessions) == 0 {
		printTransferError("transfer export", output, fmt.Errorf("aicli 会话存储中没有可导出的会话"))
		return transferExitFailure
	}
	source, cancelled, err := transferExportWizardSelectSession(ctx, terminal, sessions, defaults.SessionID)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	if cancelled {
		fmt.Println("已取消。")
		return transferExitOK
	}

	compression := "none"
	if target == string(sessionport.AgentDeepSeekHarness) {
		compression, cancelled, err = transferExportWizardSelectCompression(ctx, terminal, defaults.Compression)
		if err != nil {
			printTransferError("transfer export", output, err)
			return transferExitFailure
		}
		if cancelled {
			fmt.Println("已取消。")
			return transferExitOK
		}
	}

	bundleDir := strings.TrimSpace(defaults.BundleDir)
	if bundleDir == "" {
		bundleDir, err = transferExportWizardDefaultBundleDir()
		if err != nil {
			printTransferError("transfer export", output, err)
			return transferExitFailure
		}
	}
	bundleDir, cancelled, err = transferExportWizardText(ctx, terminal,
		"导出目录",
		"输入 bundle 输出目录；目录不能为空",
		bundleDir,
		"例如 C:\\Users\\vince\\.aicli\\transfer\\out\\session",
		true,
	)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	if cancelled {
		fmt.Println("已取消。")
		return transferExitOK
	}

	cwd := strings.TrimSpace(defaults.CWD)
	if cwd == "" {
		cwd = runtimeSessionWorkspacePath(source)
	}
	cwd, cancelled, err = transferExportWizardText(ctx, terminal,
		"目标工作目录",
		"留空沿用源会话 workspace",
		cwd,
		"DSH project key 会使用这个 cwd",
		false,
	)
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	if cancelled {
		fmt.Println("已取消。")
		return transferExitOK
	}

	prompt := newTransferPrompt(cmd)
	printTransferExportWizardSummary(prompt.out, target, source, defaults.Title, compression, bundleDir, cwd)
	if !skipConfirm {
		confirmed, confirmErr := prompt.confirm("确认生成 bundle?", true)
		if confirmErr != nil {
			printTransferError("transfer export", output, confirmErr)
			return transferExitFailure
		}
		if !confirmed {
			fmt.Fprintln(prompt.out, "已取消，未写入任何文件。")
			return transferExitOK
		}
	}

	exportCmd := newTransferExportCommand(getConfig)
	set := func(name, value string) error {
		if err := exportCmd.Flags().Set(name, value); err != nil {
			return fmt.Errorf("设置 --%s 失败: %w", name, err)
		}
		return nil
	}
	if err := set("to", target); err != nil {
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	if err := set("session", source.ID); err != nil {
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	if err := set("bundle-dir", bundleDir); err != nil {
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	if err := set("compression", compression); err != nil {
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	if cwd != "" {
		if err := set("cwd", cwd); err != nil {
			printTransferError("transfer export", output, err)
			return transferExitUsage
		}
	}
	if defaults.SessionDir != "" {
		if err := set("session-dir", defaults.SessionDir); err != nil {
			printTransferError("transfer export", output, err)
			return transferExitUsage
		}
	}
	if defaults.User != "" {
		if err := set("user", defaults.User); err != nil {
			printTransferError("transfer export", output, err)
			return transferExitUsage
		}
	}
	if defaults.Title != "" {
		if err := set("title", defaults.Title); err != nil {
			printTransferError("transfer export", output, err)
			return transferExitUsage
		}
	}
	if defaults.TargetUser != "" {
		if err := set("target-user", defaults.TargetUser); err != nil {
			printTransferError("transfer export", output, err)
			return transferExitUsage
		}
	}
	return runTransferExport(exportCmd, getConfig)
}

func transferExportWizardDefaultsFromFlags(cmd *cobra.Command) (transferExportWizardDefaults, error) {
	defaults := transferExportWizardDefaults{
		Target:      string(sessionport.AgentDeepSeekHarness),
		Compression: "zstd",
	}
	if rawTarget := stringFlag(cmd, "to"); rawTarget != "" {
		target, err := registry.NormalizeAgent(rawTarget)
		if err != nil || target == "auto" {
			if err == nil {
				err = fmt.Errorf("--to 必须显式指定 deepseek-harness 或 workbuddy")
			}
			return defaults, err
		}
		defaults.Target = target
	}
	if cmd.Flags().Lookup("compression") != nil && cmd.Flags().Changed("compression") {
		defaults.Compression = strings.ToLower(stringFlag(cmd, "compression"))
	}
	if defaults.Compression != "none" && defaults.Compression != "zstd" {
		return defaults, fmt.Errorf("--compression 只支持 none|zstd")
	}
	if defaults.Target == string(sessionport.AgentWorkBuddy) {
		if cmd.Flags().Changed("compression") && defaults.Compression != "none" {
			return defaults, fmt.Errorf("WorkBuddy bundle 只支持 --compression none")
		}
		defaults.Compression = "none"
	}
	defaults.SessionID = stringFlag(cmd, "session")
	if strings.EqualFold(defaults.SessionID, "latest") || strings.EqualFold(defaults.SessionID, "last") {
		defaults.SessionID = ""
	}
	defaults.BundleDir = stringFlag(cmd, "bundle-dir")
	defaults.CWD = stringFlag(cmd, "cwd")
	defaults.SessionDir = stringFlag(cmd, "session-dir")
	defaults.User = stringFlag(cmd, "user")
	defaults.Title = stringFlag(cmd, "title")
	defaults.TargetUser = stringFlag(cmd, "target-user")
	return defaults, nil
}

func transferExportWizardSelectTarget(ctx context.Context, terminal *ui.Terminal, defaultTarget string) (string, bool, error) {
	items := []ui.FullScreenListItem{
		{
			Title:      "DeepSeek Harness",
			Detail:     "生成 DSH v4 JSONL / zstd bundle",
			Preview:    "目标目录：sessions/<project-key>/<session-id>/session.v4.jsonl[.zstd]\n同时生成 manifest.json。默认不写 DSH_HOME。",
			SearchText: "deepseek harness dsh",
		},
		{
			Title:      "WorkBuddy",
			Detail:     "生成 projects/<slug>/<id>.jsonl bundle",
			Preview:    "同时生成 session-index.json 和 manifest.json。默认不写 workbuddy.db。",
			SearchText: "workbuddy workbuddy-ai",
		},
	}
	initial := 0
	if defaultTarget == string(sessionport.AgentWorkBuddy) {
		initial = 1
	}
	result, err := ui.RunStartupFullScreenList(ctx, terminal, ui.FullScreenListOptions{
		Title:        "导出目标",
		Subtitle:     "选择目标 Agent 的 bundle 格式",
		EmptyMessage: "没有可用目标",
		ConfirmLabel: "选择目标",
		InitialIndex: initial,
		Items:        items,
	})
	if err != nil {
		return "", false, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(items) {
		return "", true, nil
	}
	if result.Index == 0 {
		return string(sessionport.AgentDeepSeekHarness), false, nil
	}
	return string(sessionport.AgentWorkBuddy), false, nil
}

func transferExportWizardSessions(ctx context.Context, cfg *config.Config, sessionDir, user string) ([]*runtimechat.Session, error) {
	chatSession, err := newChatExportCommandSession(cfg, sessionDir, user)
	if err != nil {
		return nil, err
	}
	if chatSession.SessionManager == nil {
		return nil, fmt.Errorf("会话管理未启用")
	}
	defer chatSession.SessionManager.Stop()
	sessions, err := chatSession.SessionManager.List(ctx, chatSession.SessionUserID)
	if err != nil {
		return nil, fmt.Errorf("列出 aicli 会话失败: %w", err)
	}
	result := make([]*runtimechat.Session, 0, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			result = append(result, session)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func transferExportWizardSelectSession(ctx context.Context, terminal *ui.Terminal, sessions []*runtimechat.Session, defaultSessionID string) (*runtimechat.Session, bool, error) {
	if len(sessions) == 0 {
		return nil, false, fmt.Errorf("没有可导出的 aicli 会话")
	}
	items := make([]ui.FullScreenListItem, 0, len(sessions))
	initial := 0
	foundDefault := strings.TrimSpace(defaultSessionID) == ""
	for index, session := range sessions {
		preview := session.BuildPreview()
		title := strings.TrimSpace(session.ID)
		summary := ""
		if preview != nil {
			if strings.TrimSpace(preview.Title) != "" {
				title = strings.TrimSpace(preview.Title)
			}
			summary = strings.TrimSpace(preview.Summary)
		}
		if defaultSessionID != "" && session.ID == defaultSessionID {
			initial = index
			foundDefault = true
		}
		detail := fmt.Sprintf("%s · %s · %d 条消息",
			session.ID, transferExportSessionTime(session), session.MessageCount())
		items = append(items, ui.FullScreenListItem{
			Leading:    fmt.Sprintf("%d 条", session.MessageCount()),
			Title:      title,
			Detail:     detail,
			Preview:    transferExportSessionPreview(session, summary),
			SearchText: strings.Join([]string{session.ID, title, summary, runtimeSessionWorkspacePath(session)}, " "),
		})
	}
	if !foundDefault {
		return nil, false, fmt.Errorf("找不到指定的 aicli 会话: %s", defaultSessionID)
	}
	result, err := ui.RunStartupFullScreenList(ctx, terminal, ui.FullScreenListOptions{
		Title:        "选择 aicli 会话",
		Subtitle:     fmt.Sprintf("共 %d 个会话，按更新时间排序", len(sessions)),
		EmptyMessage: "没有可导出的会话",
		ConfirmLabel: "选择会话",
		InitialIndex: initial,
		Items:        items,
	})
	if err != nil {
		return nil, false, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(sessions) {
		return nil, true, nil
	}
	return sessions[result.Index], false, nil
}

func transferExportWizardSelectCompression(ctx context.Context, terminal *ui.Terminal, defaultCompression string) (string, bool, error) {
	items := []ui.FullScreenListItem{
		{
			Title:      "zstd",
			Detail:     "压缩 JSONL，匹配当前 DSH 默认存储",
			Preview:    "生成 session.v4.jsonl.zstd。",
			SearchText: "zstd compressed 压缩",
		},
		{
			Title:      "none",
			Detail:     "plain JSONL，便于检查",
			Preview:    "生成 session.v4.jsonl。",
			SearchText: "none plain 明文",
		},
	}
	initial := 0
	if defaultCompression == "none" {
		initial = 1
	}
	result, err := ui.RunStartupFullScreenList(ctx, terminal, ui.FullScreenListOptions{
		Title:        "DSH 压缩方式",
		Subtitle:     "zstd 与当前 DSH 存储一致；none 更便于人工检查",
		ConfirmLabel: "选择压缩",
		InitialIndex: initial,
		Items:        items,
	})
	if err != nil {
		return "", false, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(items) {
		return "", true, nil
	}
	if result.Index == 1 {
		return "none", false, nil
	}
	return "zstd", false, nil
}

func transferExportWizardText(ctx context.Context, terminal *ui.Terminal, title, subtitle, initial, hint string, required bool) (string, bool, error) {
	result, err := ui.RunStartupFullScreenList(ctx, terminal, ui.FullScreenListOptions{
		Title:         title,
		Subtitle:      subtitle,
		ConfirmLabel:  "继续",
		FreeTextMode:  true,
		FreeTextValue: initial,
		FreeTextHint:  hint,
		OnConfirmText: func(value string) error {
			if required && strings.TrimSpace(value) == "" {
				return fmt.Errorf("不能为空")
			}
			return nil
		},
	})
	if err != nil {
		return "", false, err
	}
	if result.Cancelled {
		return "", true, nil
	}
	return strings.TrimSpace(result.Text), false, nil
}

func transferExportWizardDefaultBundleDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户目录: %w", err)
	}
	return filepath.Join(home, ".aicli", "transfer", "out", time.Now().Format("20060102-150405")), nil
}

func printTransferExportWizardSummary(out io.Writer, target string, source *runtimechat.Session, titleOverride, compression, bundleDir, cwd string) {
	title := source.ID
	if preview := source.BuildPreview(); preview != nil && strings.TrimSpace(preview.Title) != "" {
		title = preview.Title
	}
	if strings.TrimSpace(titleOverride) != "" {
		title = strings.TrimSpace(titleOverride)
	}
	fmt.Fprintf(out, "\n导出摘要:\n")
	fmt.Fprintf(out, "  Target:   %s\n", target)
	fmt.Fprintf(out, "  Session:  %s\n", source.ID)
	fmt.Fprintf(out, "  Title:    %s\n", title)
	fmt.Fprintf(out, "  Messages: %d\n", source.MessageCount())
	if target == string(sessionport.AgentDeepSeekHarness) {
		fmt.Fprintf(out, "  Compress: %s\n", compression)
	}
	fmt.Fprintf(out, "  Bundle:   %s\n", bundleDir)
	if cwd != "" {
		fmt.Fprintf(out, "  CWD:      %s\n", cwd)
	}
}

func transferExportSessionTime(session *runtimechat.Session) string {
	if session == nil {
		return "unknown"
	}
	activityAt := session.ActivityAt()
	if activityAt.IsZero() {
		return "unknown"
	}
	return activityAt.Format("2006-01-02 15:04")
}

func transferExportSessionPreview(session *runtimechat.Session, summary string) string {
	if session == nil {
		return ""
	}
	lines := []string{
		"Session: " + session.ID,
		"State:   " + string(session.State),
		"Updated: " + transferExportSessionTime(session),
		"CWD:     " + blankTransferValue(runtimeSessionWorkspacePath(session)),
		fmt.Sprintf("Messages: %d", session.MessageCount()),
	}
	if summary != "" {
		lines = append(lines, "Summary: "+summary)
	}
	return strings.Join(lines, "\n")
}
