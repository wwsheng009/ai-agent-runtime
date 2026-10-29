package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

const (
	transferExitOK      = 0
	transferExitUsage   = 1
	transferExitFailure = 2
)

var transferExitHook = os.Exit

func transferExit(code int) {
	if code == transferExitOK {
		return
	}
	runExitCleanup()
	transferExitHook(code)
}

func NewTransferCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "检测、检查、列出和导入其它 Agent 的本地会话历史",
		Long: `跨 Agent 会话迁移入口。

当前支持 DeepSeek Harness 与 WorkBuddy：
  无子命令  在交互终端进入操作菜单；非 TTY 显示帮助；--help 查看命令帮助
  detect  检测 DSH_HOME/~/.dsh、~/.workbuddy、~/.workbuddy-ai 实例和能力（终端默认交互）
  inspect 只读检查实例或 DSH ZIP 中的可导入会话
  list    列出某个实例的会话
  import  读取 DSH ZIP/v4 JSONL 或 WorkBuddy projects/*.jsonl，并写入 aicli
  export  生成 DSH v4 或 WorkBuddy JSONL bundle（默认不写目标数据根）

现有 aicli 原生 export/import 保持不变；transfer 走独立的适配器目录。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferRoot(cmd, getConfig))
		},
	}
	cmd.AddCommand(newTransferDetectCommand())
	cmd.AddCommand(newTransferInspectCommand())
	cmd.AddCommand(newTransferListCommand())
	cmd.AddCommand(newTransferImportCommand(getConfig))
	cmd.AddCommand(newTransferExportCommand(getConfig))
	return cmd
}

func parseTransferOutputOptions(cmd *cobra.Command) (structuredOutputOptions, error) {
	return resolveStructuredOutputOptions(cmd, "text", "text", "json")
}

func printTransferError(command string, output structuredOutputOptions, err error) {
	if err == nil {
		return
	}
	if isJSONOutputFormat(output.Format) {
		emitCommandError(command, output.Format, err, nil)
		return
	}
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
}

func transferRoots(cmd *cobra.Command) []string {
	if cmd == nil || cmd.Flags().Lookup("root") == nil {
		return nil
	}
	values, err := cmd.Flags().GetStringArray("root")
	if err != nil {
		return nil
	}
	return values
}

func transferInstanceRoot(instance string) (string, error) {
	return registry.ResolveInstance(strings.TrimSpace(instance))
}

func transferUsageError(message string) error {
	return fmt.Errorf("参数错误: %s", message)
}

func transferImportErrorHint(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errImportSessionExists):
		return "加 --new-id 让导入自动生成新 ID（两份会话并存）。"
	case strings.Contains(err.Error(), "历史格式"):
		return "请先在 DeepSeek Harness Web 中执行 /export，导入生成的当前格式 ZIP。"
	case strings.Contains(err.Error(), "没有找到 DSH"):
		return "用 aicli transfer detect 查看实例，或显式传 --root 指向 DSH_HOME。"
	default:
		return ""
	}
}
