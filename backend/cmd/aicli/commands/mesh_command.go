// aicli mesh <子命令>：节点网格工具的别名入口（架构 §7.5 / Q10）。
//
// 与独立二进制 `aicli-mesh` 共用 `internal/mesh.CLI`——参数解析、渲染、JSON 信封与
// §5 退出码契约都只有一份实现；本文件只做两件事：把参数原样透传、把退出码透传。
// 想改行为就改 internal/mesh/cli.go，两边同时生效（避免「文档说 A、命令做 B」）。
//
// 它不是节点：mesh_bootstrap.go 的 meshNodeCommands 只含 chat/resume，所以
// `aicli mesh ...` 不写节点档案、不占租约、不出现在 `ls` 里（与 aicli-mesh 同）。
package commands

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// meshAliasExitOK 与 mesh.ExitOK 同值：唯一不需要走 os.Exit 的退出码。
const meshAliasExitOK = 0

// meshAliasExitHook 是退出钩子：生产路径为 os.Exit，测试注入以断言退出码
// （与 export.go 的 exportExitHook 同一惯例）。
var meshAliasExitHook = os.Exit

func meshAliasExit(code int) {
	if code == meshAliasExitOK {
		return
	}
	runExitCleanup()
	meshAliasExitHook(code)
}

// NewMeshCommand returns the `aicli mesh` alias of the aicli-mesh tool.
func NewMeshCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh [子命令] [参数...]",
		Short: "节点网格工具（ls / show / url / call / send / screen / open / new / stop / watch / gc / doctor / version）",
		Long: `节点网格工具：发现本机节点、跨进程调用、拉起/停止节点、复盘 journal 事件。

与独立二进制 aicli-mesh 是同一套实现（internal/mesh.CLI），参数与退出码完全一致：

  aicli mesh ls --json
  aicli mesh show session_20260924093535
  aicli mesh new --workspace E:\work\proj-a
  aicli mesh watch --since 5m
  aicli mesh gc --apply

它不是节点：不写网格档案、不占租约、不出现在 ls 列表里。
完整文档：docs/aicli/mesh-cli.md`,
		// 参数（含 --flag）原样交给 internal/mesh.CLI：网格旗标（--since / --limit /
		// --once / --no-color ...）的语义与 cobra 的解析规则无关，在这里再声明一遍
		// 等于维护第二份真相（也会与 mesh-cli.md 的用法总览分叉）。
		DisableFlagParsing: true,
		SilenceUsage:       true,
		Run: func(cmd *cobra.Command, args []string) {
			cli := &mesh.CLI{
				Stdout: cmd.OutOrStdout(),
				Stderr: cmd.ErrOrStderr(),
				// version 子命令报 aicli 自身的版本（main 用
				// SetChatStatusBuildInfo 注入），而不是 mesh.CLI 的 dev 兜底。
				Version: chatStatusVersion,
			}
			meshAliasExit(cli.Run(args))
		},
	}
	return cmd
}
