package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// NewAgentCommand creates the aicli agent command group (ACP host, etc.).
func NewAgentCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "以外部 Agent 协议宿主运行 aicli",
		Long: `将 aicli 作为外部 Agent 协议宿主暴露协议入口。

当前支持：
  aicli agent stdio   Agent Client Protocol (ACP) 子集，stdin/stdout NDJSON

等价入口：
  aicli acp           与 aicli agent stdio 完全等价（通用协议快捷命令）
  aicli --acp [...]   根命令 flag 形式，转发到 aicli agent stdio

角色 / agent 定义、chat --agent 与 ACP 宿主说明见 docs/aicli/agents.md；
命令索引见 docs/aicli/install.md；headless 工具代理见 docs/aicli/exec.md。`,
	}
	cmd.AddCommand(newAgentStdioCommand(getCfg))
	return cmd
}

// NewACPCommand creates the top-level `aicli acp` shortcut. It is the same
// ACP stdio host as `aicli agent stdio`, exposed as a first-class command so
// generic ACP clients that treat "acp" as the canonical binary entry can
// launch `aicli acp` directly.
func NewACPCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := newAgentStdioCommand(getCfg)
	cmd.Use = "acp"
	cmd.Short = "以 ACP 协议在 stdin/stdout 上运行（等价 aicli agent stdio）"
	// Keep the canonical path in the long help so either entry documents the
	// other one.
	cmd.Long = strings.Replace(cmd.Long,
		"以 Agent Client Protocol (ACP) 子集模式在 stdio 上服务。",
		"以 Agent Client Protocol (ACP) 子集模式在 stdio 上服务（等价于 `aicli agent stdio`）。",
		1,
	)
	cmd.Example = strings.ReplaceAll(cmd.Example, "aicli agent stdio", "aicli acp")
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
  session/load   (loadSession=true；回放历史后返回空对象 {}
                  即 LoadSessionResponse，字段均可选)
  $/cancel_request   (JSON-RPC 标准取消通知；按请求 id 双向取消，
    数字与字符串 id 自动归一化匹配)

Agent → client：
  session/update
  session/request_permission

完整协议文档（事件类型、权限流程、取消语义、故障排查）见
docs/acp/README.md。

session/load 解析顺序：先内存中已附着的 session，再按 --session-dir
等非 ephemeral 配置从持久化存储恢复。默认 --ephemeral 时仅支持进程内
session/new 后再 load 同一 id。

MCP：session/new|load|resume 的 mcpServers 会被逐条容错解析并装配成会话级
MCP（stdio/http/sse），生命周期与会话一致；非法条目只产生诊断，不会让请求失败。
装配策略见 --acp-mcp（默认 merge：本地配置链 + 客户端下发）。未信任工作区会
拒绝启动客户端下发的 server 并在 stderr 给出原因。

stdin 是协议流，不是 prompt 文本。模型/权限等通过 flags 配置。
权限 / profile / agent 相关概念见 docs/aicli/agents.md 与 docs/aicli/exec.md。`,
		Example: `  aicli agent stdio --provider openai --model gpt-4o
  aicli agent stdio --profile default --permission-mode default
  aicli agent stdio --yolo --enable-tools
  aicli agent stdio --session-dir %USERPROFILE%\.aicli\sessions
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
	cmd.Flags().String("acp-mcp", "merge",
		"客户端下发 mcpServers 的装配策略：merge=本地配置链+客户端下发（默认）|local=仅本地|client=仅客户端|off=全部关闭")
}
