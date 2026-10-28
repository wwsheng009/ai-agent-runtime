package commands

// resume_detach.go 让 `aicli resume <id> --detach` 变成「只开窗口、不接管终端」的
// 入口：本进程定位会话 → 在新的分离终端窗口里按原样重放它 → 打印结果并退出。
//
// 为什么值得单独一个入口（而不是只有 `sessions` 界面里的动作）：脚本、mesh 编排
// 与「我在 tmux/CI 里想把某个会话丢回桌面窗口」都需要一条非交互命令，而交互界面
// 依赖 TTY。两者共用同一个执行体（openSessionInNewTerminal），所以行为不会分叉。

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// resumeDetachRequest 把 resume 的旗标翻译成 sessions 浏览器的范围请求。
//
// 范围口径与 `aicli resume` 完全一致：默认只看当前工作目录（--cwd=false 看全部），
// 筛选走 --session-query/--session-state/--session-provider/--session-model。
// 这样「--detach 打开的那个会话」与「不带 --detach 时 resume 会恢复的那个会话」
// 是同一个，不会出现两条入口挑出不同会话的分裂。
func resumeDetachRequest(cmd *cobra.Command, args []string) (sessionsCommandRequest, string, error) {
	if cmd == nil {
		return sessionsCommandRequest{}, "", fmt.Errorf("resume command is nil")
	}
	if len(args) > 1 {
		return sessionsCommandRequest{}, "", fmt.Errorf("resume 最多接受一个 SESSION_ID")
	}
	if listSessions, _ := cmd.Flags().GetBool("list-sessions"); listSessions {
		return sessionsCommandRequest{}, "", fmt.Errorf("resume --detach 不能与 --list-sessions 同用（前者拉起新窗口后退出，后者只列出不进入）")
	}

	sessionID := ""
	if len(args) == 1 {
		sessionID = strings.TrimSpace(args[0])
	}
	if flagSession, err := cmd.Flags().GetString("session"); err == nil && strings.TrimSpace(flagSession) != "" {
		if sessionID != "" && !strings.EqualFold(sessionID, strings.TrimSpace(flagSession)) {
			return sessionsCommandRequest{}, "", fmt.Errorf("冲突的会话 ID：位置参数 %q 与 --session %q", sessionID, flagSession)
		}
		sessionID = strings.TrimSpace(flagSession)
	}

	cwd := true
	if value, err := cmd.Flags().GetBool("cwd"); err == nil {
		cwd = value
	}
	limit := sessionsDefaultLimit
	if value, err := cmd.Flags().GetInt("session-limit"); err == nil && value > 0 {
		limit = value
	}
	req := sessionsCommandRequest{
		All:           !cwd,
		SessionDir:    flagString(cmd, "session-dir"),
		User:          flagString(cmd, "user"),
		Query:         flagString(cmd, "session-query"),
		State:         flagString(cmd, "session-state"),
		Provider:      flagString(cmd, "session-provider"),
		Model:         flagString(cmd, "session-model"),
		Terminal:      flagString(cmd, "terminal"),
		Limit:         limit,
		LimitExplicit: cmd.Flags().Changed("session-limit"),
		Plain:         true,
		Entry:         "resume",
	}
	return req, strings.TrimSpace(sessionID), nil
}

func flagString(cmd *cobra.Command, name string) string {
	if cmd == nil {
		return ""
	}
	value, err := cmd.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// runResumeDetachCommand 执行一次 detach 拉起；返回 error 由调用方映射成退出码。
func runResumeDetachCommand(cmd *cobra.Command, args []string, cfg *config.Config) error {
	req, sessionID, err := resumeDetachRequest(cmd, args)
	if err != nil {
		return err
	}
	launcher := sessionsTerminalLauncher(req.Terminal)
	if _, err := launcher.Detect(); err != nil {
		// 先探测再打开存储：没有终端后端时不该为一个注定失败的动作去读会话库。
		return err
	}
	store, err := openSessionsBrowserStore(cfg, req)
	if err != nil {
		return err
	}
	defer store.Stop()

	item, err := store.itemOrLatest(sessionID)
	if err != nil {
		return err
	}
	message, err := openSessionInNewTerminal(item, launcher)
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

// itemOrLatest 在「显式 ID」与「范围内最近一个可恢复会话」之间取目标。
func (s *sessionsBrowserStore) itemOrLatest(sessionID string) (sessionsCommandItem, error) {
	if strings.TrimSpace(sessionID) != "" {
		return s.itemByID(sessionID)
	}
	return s.latestSessionItem()
}
