package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// aicli export 退出码约定（与 aicli stats 对齐）：
//
//	0 = 导出成功
//	1 = 参数错误（未知/冲突的导出选项、多余参数）
//	2 = 确定性错误（会话不存在、会话存储不可读、输出发布失败）
//
// chat 内的 /export 把错误打到 stdout 后仍返回 0，脚本无法据此判断成败；顶层
// 命令补齐退出码，同时复用同一份导出实现与输出摘要。
const (
	exportExitOK      = 0
	exportExitUsage   = 1
	exportExitFailure = 2
)

// exportExitHook 是退出钩子：生产路径为 os.Exit，测试注入以断言退出码。
var exportExitHook = os.Exit

func exportExit(code int) {
	if code == exportExitOK {
		return
	}
	runExitCleanup()
	exportExitHook(code)
}

const exportCommandLongHelp = `导出会话内容：完整 JSON（含 metadata、tool_calls、tool 结果）或 Markdown。

这是 chat 内 /export 的顶层等价入口，复用同一套导出实现与格式语义。

导出格式（默认 --full）：
  --full           完整 JSON：messages + metadata + tool_calls + tool 结果全量导出
  --body           正文 Markdown：仅用户/助手正文，不含工具链
  --tools          Markdown + 工具调用：正文后追加工具名、call id 与输入参数
  --trace          Markdown + 工具调用与结果：在 --tools 基础上按 tool_call_id 内联
                   输出（单条输出超过 32KB 自动截断，完整内容请用 --full）
  --format <fmt>   等价写法：full|body|md-tools|md-trace，也接受裸格式词

会话目标（默认 latest）：
  latest           最近一次可恢复会话
  current          顶层命令没有「当前会话」上下文，等价于 latest
  <session-id>     指定会话 ID（不校验用户归属，跨身份平面创建的会话同样可导出）

输出位置：
  默认写入 ~/.aicli/chat-logs/exports/<session-id>_<时间戳>_<格式>.<json|md>
  --output <path>  指定输出文件（指向已存在的目录时等价于 --dir）
  --dir <dir>      指定输出目录

退出码：
  0 = 导出成功
  1 = 参数错误
  2 = 确定性错误（会话不存在、会话存储不可读、输出发布失败）`

const exportCommandExampleHelp = `  aicli export
  aicli export latest --trace
  aicli export latest --body --output ./session.md
  aicli export --format md-trace --dir ./exports
  aicli export session_20260923112350_fny0opuB --full
  aicli export latest --trace --session-dir ./sessions --user alice`

// NewExportCommand 创建 `aicli export`：把 chat 内的 /export 提升为可直接从
// 命令行调用的顶层子命令，内部复用 exportChatSession（不经过 chat 引导、actor
// 与工具面），并把失败映射成非零退出码。
func NewExportCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "export [current|latest|<session-id>]",
		Short:   "导出会话为完整 JSON 或 Markdown（可选附带工具调用与结果）",
		Long:    exportCommandLongHelp,
		Example: exportCommandExampleHelp,
		Args:    cobra.MaximumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			exportExit(runExportCommand(cmd, getConfig, args))
		},
	}
	cmd.Flags().String("session-dir", "", "会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "会话用户 ID（优先于 AICLI_SESSION_USER 和 runtime sessions.defaultUserId）")
	cmd.Flags().StringP("format", "f", "", "导出格式（full|body|md-tools|md-trace，等价于 --full/--body/--tools/--trace）")
	cmd.Flags().Bool("full", false, "完整 JSON（包含 metadata、tool_calls、tool 结果等）")
	cmd.Flags().Bool("body", false, "正文 Markdown（仅用户/助手正文）")
	cmd.Flags().Bool("tools", false, "Markdown + 工具调用名称与输入参数")
	cmd.Flags().Bool("trace", false, "Markdown + 工具调用与结果（按 tool_call_id 配对）")
	cmd.Flags().StringP("output", "o", "", "输出文件路径（指向已存在的目录时等价于 --dir）")
	cmd.Flags().String("dir", "", "输出目录（默认: ~/.aicli/chat-logs/exports）")
	return cmd
}

// exportFormatFlagNames 是四个格式布尔 flag，顺序即帮助里的展示顺序。
var exportFormatFlagNames = []string{"full", "body", "tools", "trace"}

// exportOptionFields 把 cobra 解析出的 flag 与位置参数还原成 /export 的选项
// token 序列，交给 parseChatExportOptionFields 解析，保证两条入口语义一致。
//
// 与 /export 的一处刻意差异：格式来源（--full/--body/--tools/--trace/--format
// 与裸格式词）互相冲突时直接报参数错误，而不是静默取最后一个——命令行上同时
// 写 --full 和 --trace 显然是误用，静默选一个会导出用户没要的内容。
func exportOptionFields(cmd *cobra.Command, args []string) ([]string, error) {
	fields := make([]string, 0, len(args)+8)
	sources := make([]string, 0, len(exportFormatFlagNames)+1)
	formats := make([]chatExportFormat, 0, len(exportFormatFlagNames)+1)
	record := func(source string, format chatExportFormat) {
		sources = append(sources, source)
		formats = append(formats, format)
	}

	if cmd != nil {
		for _, name := range exportFormatFlagNames {
			if !cmd.Flags().Changed(name) {
				continue
			}
			format, ok := matchChatExportFormatToken(name)
			if !ok {
				return nil, fmt.Errorf("未知导出格式: --%s", name)
			}
			record("--"+name, format)
			fields = append(fields, "--"+name)
		}
		if cmd.Flags().Changed("format") {
			value := stringFlag(cmd, "format")
			if value == "" {
				return nil, fmt.Errorf("--format 需要指定 full、body、md-tools 或 md-trace")
			}
			format, ok := matchChatExportFormatToken(value)
			if !ok {
				return nil, fmt.Errorf("未知导出格式: %s", value)
			}
			record("--format "+value, format)
			fields = append(fields, "--format", value)
		}
		if cmd.Flags().Changed("output") {
			value := stringFlag(cmd, "output")
			if value == "" {
				return nil, fmt.Errorf("--output 需要指定输出文件路径")
			}
			fields = append(fields, "--output", value)
		}
		if cmd.Flags().Changed("dir") {
			value := stringFlag(cmd, "dir")
			if value == "" {
				return nil, fmt.Errorf("--dir 需要指定输出目录")
			}
			fields = append(fields, "--dir", value)
		}
	}

	for _, arg := range args {
		token := strings.TrimSpace(arg)
		if token == "" {
			continue
		}
		if format, ok := matchChatExportFormatToken(token); ok {
			record(token, format)
		}
		fields = append(fields, token)
	}

	for index := 1; index < len(formats); index++ {
		if formats[index] != formats[0] {
			return nil, fmt.Errorf("导出格式冲突: %s 与 %s 只能选一个", sources[0], sources[index])
		}
	}
	return fields, nil
}

func runExportCommand(cmd *cobra.Command, getConfig func() *config.Config, args []string) int {
	fields, err := exportOptionFields(cmd, args)
	if err != nil {
		printExportCommandError(err)
		return exportExitUsage
	}
	opts, err := parseChatExportOptionFields(fields)
	if err != nil {
		printExportCommandError(err)
		return exportExitUsage
	}
	if !opts.ExplicitTarget {
		// 顶层命令没有会话上下文：无参数等价于导出最近一次会话。
		opts.Target = "latest"
		opts.ExplicitTarget = true
	}
	switch strings.ToLower(strings.TrimSpace(opts.Target)) {
	case "current", "now", ".":
		// 与 /export 的 current 不同，顶层命令不存在「当前会话」，落到最近一次会话；
		// 实际导出的会话 ID 会出现在导出摘要里，不会静默换目标。
		opts.Target = "latest"
	}

	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	session, err := newChatExportCommandSession(cfg, stringFlag(cmd, "session-dir"), stringFlag(cmd, "user"))
	if err != nil {
		printExportCommandError(err)
		return exportExitFailure
	}
	defer session.SessionManager.Stop()

	result, err := exportChatSession(session, opts)
	if err != nil {
		printExportCommandError(err)
		return exportExitFailure
	}
	printChatExportResult(result)
	return exportExitOK
}

// newChatExportCommandSession 构造一个只用于导出的最小 ChatSession：顶层导出
// 不需要 chat 的 provider/adapter/actor/工具面，只需要会话存储（latest 与
// <session-id> 的解析、canonical 历史流式读取）与导出所需的目录信息。
func newChatExportCommandSession(cfg *config.Config, sessionDir, user string) (*ChatSession, error) {
	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(cfg, nil)
	manager, userID, resolvedDir, err := newChatSessionManagerWithRuntimeConfig(sessionDir, runtimeConfig, runtimeConfigPath, user)
	if err != nil {
		return nil, fmt.Errorf("初始化会话存储失败: %w", err)
	}
	return &ChatSession{
		SessionManager: manager,
		SessionUserID:  userID,
		SessionDir:     resolvedDir,
		NoInteractive:  true,
	}, nil
}

func printExportCommandError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	if errors.Is(err, runtimechat.ErrSessionNotFound) {
		fmt.Fprintln(os.Stderr, "提示: 会话存储中没有匹配的会话；可用 --session-dir/--user 指向其他会话存储，或用 aicli resume --list-sessions 查看可用会话 ID。")
	}
}
