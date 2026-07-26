package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func NewExecCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [OPTIONS] [PROMPT]",
		Short: "非交互式执行 AI 代理",
		Long: `以 headless 模式运行 AI 代理，适用于 CI/CD、脚本集成和自动化任务。

支持：
  aicli exec [OPTIONS] [PROMPT]
  aicli exec resume [SESSION_ID] [PROMPT]
  aicli exec review [--uncommitted|--base|--commit] [指令]

使用前请先完成 provider 登录（aicli login）并可用 aicli doctor provider 做连通性检查。
输出契约、退出码与 CI 示例见 docs/aicli/exec.md；排错见 docs/aicli/faq.md。`,
		Example: `  aicli exec "解释这段代码的作用"
  cat main.go | aicli exec -p "分析代码质量"
  aicli exec --provider openai --model gpt-4.1 "生成变更摘要"
  aicli exec --json "创建一个 Hello World 程序"
  aicli exec resume --last
  aicli exec review --uncommitted`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := runExec(cmd, getCfg(), args); err != nil {
				exitExecCommandError(cmd, err)
			}
		},
	}
	cmd.AddCommand(newExecResumeCommand(getCfg))
	cmd.AddCommand(newExecReviewCommand(getCfg))
	registerExecFlags(cmd)
	return cmd
}

func exitExecCommandError(cmd *cobra.Command, err error) {
	emitExecCommandError(cmd, err)
	runExitCleanup()
	os.Exit(execExitCode(err))
}

func emitExecCommandError(cmd *cobra.Command, err error) {
	if err == nil {
		return
	}
	code := "EXECUTION_FAILED"
	var execErr *execExitError
	if strings.TrimSpace(err.Error()) != "" && asExecExitError(err, &execErr) && execErr.Code != "" {
		code = execErr.Code
	}
	jsonMode := false
	outputFormat := "text"
	envelope := false
	if cmd != nil {
		if cmd.Flags().Lookup("json") != nil {
			jsonMode, _ = cmd.Flags().GetBool("json")
		}
		if cmd.Flags().Lookup("output") != nil {
			outputFormat, _ = cmd.Flags().GetString("output")
		}
		envelope = useJSONEnvelope(cmd)
	}
	if jsonMode {
		processor := NewExecEventProcessor(true, nil, "")
		processor.OnError(ErrorEvent{Message: err.Error(), Code: code})
		return
	}
	if strings.EqualFold(strings.TrimSpace(outputFormat), "json") {
		emitCommandError("exec", "json", err, map[string]interface{}{"code": code})
		return
	}
	_ = envelope
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func asExecExitError(err error, target **execExitError) bool {
	for err != nil {
		if typed, ok := err.(*execExitError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = next.Unwrap()
	}
	return false
}
