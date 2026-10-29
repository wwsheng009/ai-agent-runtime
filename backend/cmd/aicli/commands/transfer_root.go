package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type transferRootAction struct {
	ID         string
	Title      string
	Detail     string
	Preview    string
	SearchText string
}

var transferRootUIAvailable = func() bool {
	return ui.CanUseFullScreenList(ui.NewTerminal())
}

func transferRootActions() []transferRootAction {
	return []transferRootAction{
		{
			ID:         "detect",
			Title:      "检测本机 Agent",
			Detail:     "探测 DSH / WorkBuddy 实例和会话",
			Preview:    "进入实例与会话浏览界面。只读，不会自动导入。",
			SearchText: "detect 检测 dsh deepseek workbuddy 实例 会话",
		},
		{
			ID:         "inspect",
			Title:      "检查可导入会话",
			Detail:     "列出默认实例，或检查 DSH ZIP 候选",
			Preview:    "只读输出实例、会话、格式、版本和 warning。",
			SearchText: "inspect 检查 导入 会话 zip",
		},
		{
			ID:         "import",
			Title:      "导入会话到 aicli",
			Detail:     "交互选择实例和会话",
			Preview:    "选择源会话后先预检，再确认写入 aicli。",
			SearchText: "import 导入 会话 aicli",
		},
		{
			ID:         "export",
			Title:      "导出会话 bundle",
			Detail:     "交互选择目标、会话和输出目录",
			Preview:    "生成 DSH v4 或 WorkBuddy bundle；默认不写目标 Agent 数据根。",
			SearchText: "export 导出 bundle dsh workbuddy",
		},
		{
			ID:         "help",
			Title:      "查看帮助",
			Detail:     "显示 transfer 子命令和 flags",
			Preview:    "保留完整的命令行帮助和脚本用法。",
			SearchText: "help 帮助 usage flags",
		},
		{
			ID:         "quit",
			Title:      "退出",
			Detail:     "返回终端",
			Preview:    "不执行任何操作。",
			SearchText: "quit exit 退出 返回",
		},
	}
}

func transferRootMenuOptions() ui.FullScreenListOptions {
	actions := transferRootActions()
	items := make([]ui.FullScreenListItem, 0, len(actions))
	for _, action := range actions {
		items = append(items, ui.FullScreenListItem{
			Title:      action.Title,
			Detail:     action.Detail,
			Preview:    action.Preview,
			SearchText: action.SearchText,
		})
	}
	return ui.FullScreenListOptions{
		Title:        "会话迁移",
		Subtitle:     "选择操作 · Enter 执行 · Esc/q 退出",
		EmptyMessage: "没有可用操作",
		ConfirmLabel: "执行",
		Items:        items,
	}
}

func runTransferRoot(cmd *cobra.Command, getConfig func() *config.Config) int {
	if !transferRootUIAvailable() {
		_ = cmd.Help()
		return transferExitOK
	}
	terminal := ui.NewTerminal()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	picked, err := ui.RunStartupFullScreenList(ctx, terminal, transferRootMenuOptions())
	if err != nil {
		if errors.Is(err, ui.ErrFullScreenUnavailable) {
			_ = cmd.Help()
			return transferExitOK
		}
		printTransferError("transfer", structuredOutputOptions{Format: "text"}, err)
		return transferExitFailure
	}
	if picked.Cancelled || picked.Index < 0 || picked.Index >= len(transferRootActions()) {
		return transferExitOK
	}
	return executeTransferRootAction(cmd, transferRootActions()[picked.Index].ID, getConfig)
}

func executeTransferRootAction(cmd *cobra.Command, action string, getConfig func() *config.Config) int {
	switch action {
	case "detect":
		return runTransferDetect(newTransferDetectCommand())
	case "inspect":
		return runTransferInspect(newTransferInspectCommand())
	case "import":
		importCmd := newTransferImportCommand(getConfig)
		if err := importCmd.Flags().Set("interactive", "true"); err != nil {
			printTransferError("transfer", structuredOutputOptions{Format: "text"}, err)
			return transferExitUsage
		}
		return runTransferImport(importCmd, getConfig)
	case "export":
		return runTransferExportWizard(cmd, getConfig, false)
	case "help":
		if err := cmd.Help(); err != nil {
			printTransferError("transfer", structuredOutputOptions{Format: "text"}, err)
			return transferExitFailure
		}
		return transferExitOK
	case "quit":
		return transferExitOK
	default:
		printTransferError("transfer", structuredOutputOptions{Format: "text"}, fmt.Errorf("未知操作: %s", action))
		return transferExitUsage
	}
}
