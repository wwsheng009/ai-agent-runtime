package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ResumeCommandLongHelp is the long help text for `aicli resume`.
const ResumeCommandLongHelp = `恢复历史 chat 会话并进入交互式对话。

行为约定（对齐 Claude Code 的 resume 体验，并复用 chat 会话契约）：
  - aicli resume                 从当前工作目录恢复最近一次可恢复会话（等价 aicli chat --resume）
  - aicli resume --cwd=false     跨工作目录恢复最近一次可恢复会话
  - aicli resume <session-id>    加载指定会话（等价 aicli chat --session <id>）
  - aicli resume --list-sessions 列出可筛选的会话后退出

可继续使用 chat 的 provider/model/profile/permission 等 flags。
chat 内也可用 /resume、/sessions、/load 继续切换会话。

相关文档：
  - docs/aicli/quickstart.md
  - docs/aicli/faq.md
  - docs/aicli/agents.md`

// ResumeCommandExampleHelp is the example help text for `aicli resume`.
const ResumeCommandExampleHelp = `  aicli resume                              # 恢复当前工作目录的最近会话
  aicli resume --cwd=false                  # 跨工作目录恢复最近会话
  aicli resume session_xxx                  # 加载指定会话
  aicli resume --list-sessions              # 默认仅列出当前工作目录的会话
  aicli resume --list-sessions --cwd=false  # 列出全部工作目录的会话
  aicli resume --list-sessions --session-query review
  aicli resume --provider openai --model gpt-4.1
  aicli resume session_xxx --stream
  aicli chat --resume                       # 兼容写法
  aicli chat --session session_xxx          # 兼容写法`

// NewResumeCommand builds the top-level resume subcommand.
// It reuses chat flags and HandleChat so session persistence, profile
// resolution, and interactive runtime stay on one code path.
func NewResumeCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resume [SESSION_ID]",
		Short:   "恢复历史 chat 会话",
		Long:    ResumeCommandLongHelp,
		Example: ResumeCommandExampleHelp,
		Args:    cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := applyResumeCommandArgs(cmd, args); err != nil {
				exitCommandError("resume", "json", err, nil)
			}
			HandleChat(cmd, getCfg())
		},
	}
	registerChatFlags(cmd)
	return cmd
}

// applyResumeCommandArgs maps positional SESSION_ID / bare resume onto the
// shared chat flags consumed by parseChatCommandOptions.
//
// Priority:
//  1. --list-sessions keeps listing semantics and does not force resume/session
//  2. positional SESSION_ID sets --session (conflicts with a different --session)
//  3. existing --session is left alone
//  4. otherwise default to --resume=true (latest resumable session)
func applyResumeCommandArgs(cmd *cobra.Command, args []string) error {
	if cmd == nil {
		return fmt.Errorf("resume command is nil")
	}

	listSessions := false
	if cmd.Flags().Lookup("list-sessions") != nil {
		listSessions, _ = cmd.Flags().GetBool("list-sessions")
	}
	if listSessions {
		if len(args) > 0 {
			return fmt.Errorf("resume --list-sessions 不接受 SESSION_ID 参数")
		}
		return nil
	}

	sessionFlag := ""
	if cmd.Flags().Lookup("session") != nil {
		sessionFlag, _ = cmd.Flags().GetString("session")
	}
	sessionFlag = strings.TrimSpace(sessionFlag)

	if len(args) > 1 {
		return fmt.Errorf("resume 最多接受一个 SESSION_ID")
	}
	if len(args) == 1 {
		sessionID := strings.TrimSpace(args[0])
		if sessionID == "" {
			return fmt.Errorf("SESSION_ID 不能为空")
		}
		if sessionFlag != "" && !strings.EqualFold(sessionFlag, sessionID) {
			return fmt.Errorf("冲突的会话 ID：位置参数 %q 与 --session %q", sessionID, sessionFlag)
		}
		if err := cmd.Flags().Set("session", sessionID); err != nil {
			return fmt.Errorf("设置 --session 失败: %w", err)
		}
		// Explicit session id supersedes bare --resume.
		if cmd.Flags().Lookup("resume") != nil && !cmd.Flags().Changed("resume") {
			_ = cmd.Flags().Set("resume", "false")
		}
		return nil
	}

	if sessionFlag != "" {
		return nil
	}

	// Bare `aicli resume`: restore latest session, matching `aicli chat --resume`
	// and Claude Code's continue/resume-latest style entrypoint.
	if cmd.Flags().Lookup("resume") == nil {
		return fmt.Errorf("resume flag is not registered")
	}
	if cmd.Flags().Changed("resume") {
		return nil
	}
	if err := cmd.Flags().Set("resume", "true"); err != nil {
		return fmt.Errorf("设置 --resume 失败: %w", err)
	}
	return nil
}
