package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// NewAgentCommand creates the aicli agent command group (ACP host, etc.).
func NewAgentCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "以外部 Agent 协议宿主运行 aicli",
		Long: `将 aicli 作为外部 Agent 宿主暴露协议入口。

当前支持：
  aicli agent stdio   Agent Client Protocol (ACP) 子集，stdin/stdout NDJSON

角色 / agent 定义、chat --agent 与 ACP 宿主说明见 docs/aicli/agents.md；
命令索引见 docs/aicli/install.md；headless 工具代理见 docs/aicli/exec.md。`,
	}
	cmd.AddCommand(newAgentStdioCommand(getCfg))
	return cmd
}

func newAgentStdioCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stdio",
		Short: "在 stdin/stdout 上运行 ACP 子集",
		Long: `以 Agent Client Protocol (ACP) 子集模式在 stdio 上服务。

传输：JSON-RPC 2.0 over NDJSON（每行一条消息）。
Stdout 仅用于协议消息；日志与诊断写入 stderr / 日志文件。

支持的方法：
  initialize
  session/new
  session/prompt
  session/cancel

Agent → client：
  session/update
  session/request_permission

stdin 是协议流，不是 prompt 文本。模型/权限等通过 flags 配置。
权限 / profile / agent 相关概念见 docs/aicli/agents.md 与 docs/aicli/exec.md。`,
		Example: `  aicli agent stdio --provider openai --model gpt-4o
  aicli agent stdio --profile default --permission-mode default
  aicli agent stdio --yolo --enable-tools
  aicli agent stdio --ephemeral --log-dir %USERPROFILE%\.aicli\logs`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cfg := getCfg()
			if err := runAgentStdio(cmd, cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				runExitCleanup()
				os.Exit(1)
			}
		},
	}
	registerAgentStdioFlags(cmd)
	return cmd
}

func registerAgentStdioFlags(cmd *cobra.Command) {
	// Reuse exec shared flags but exclude prompt/image/output-schema surfaces
	// that don't apply to a multi-turn protocol host. stdin is the protocol.
	registerExecSharedFlags(cmd, map[string]bool{
		"prompt":        true,
		"image":         true,
		"output-schema": true,
	})
	// ACP host defaults to enabling tools (clients can approve via RPC).
	// Keep --disable-tools available for pure-text hosts.
	if f := cmd.Flags().Lookup("disable-tools"); f != nil {
		f.DefValue = "false"
		_ = f.Value.Set("false")
	}
	if f := cmd.Flags().Lookup("enable-tools"); f != nil {
		f.Usage = "显式启用 tools/skills 暴露（agent stdio 默认启用）"
	}
	if f := cmd.Flags().Lookup("ephemeral"); f != nil {
		f.DefValue = "true"
		_ = f.Value.Set("true")
		f.Usage = "不持久化会话文件（agent stdio 默认 true）"
	}
}
