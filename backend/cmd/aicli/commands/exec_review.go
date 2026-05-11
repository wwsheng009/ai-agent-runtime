package commands

import (
	"bytes"
	"fmt"
	"os"
	osExec "os/exec"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const maxReviewDiffBytes = 512 * 1024

func newExecReviewCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review [指令]",
		Short: "运行代码审查",
		Long: `对当前 Git 仓库运行代码审查。

默认审查未提交更改。positional args 会作为审查指令。`,
		Example: `  aicli exec review --uncommitted
  aicli exec review --base main
  aicli exec review --commit abc1234
  aicli exec review "检查安全漏洞"`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := runExecReview(cmd, getCfg(), args); err != nil {
				exitExecCommandError(cmd, err)
			}
		},
	}
	cmd.Flags().Bool("uncommitted", false, "审查未提交的更改")
	cmd.Flags().String("base", "", "审查相对于基础分支的更改")
	cmd.Flags().String("commit", "", "审查特定提交")
	cmd.Flags().String("commit-title", "", "提交标题（用于 --commit）")
	registerExecSharedFlags(cmd, map[string]bool{"prompt": true, "title": true})
	return cmd
}

func runExecReview(cmd *cobra.Command, cfg *config.Config, args []string) error {
	uncommitted, _ := cmd.Flags().GetBool("uncommitted")
	baseBranch, _ := cmd.Flags().GetString("base")
	commitSHA, _ := cmd.Flags().GetString("commit")
	commitTitle, _ := cmd.Flags().GetString("commit-title")
	customPrompt := strings.TrimSpace(strings.Join(args, " "))

	targetCount := 0
	if uncommitted {
		targetCount++
	}
	if strings.TrimSpace(baseBranch) != "" {
		targetCount++
	}
	if strings.TrimSpace(commitSHA) != "" {
		targetCount++
	}
	if targetCount == 0 {
		uncommitted = true
	}
	if targetCount > 1 {
		return newExecExitError(execExitUsage, "REVIEW_TARGET_CONFLICT", fmt.Errorf("--uncommitted, --base 和 --commit 不能同时使用"))
	}
	if err := ensureGitRepository(); err != nil {
		return newExecExitError(execExitUsage, "NOT_GIT_REPOSITORY", err)
	}
	diff, truncated, err := getReviewDiff(uncommitted, baseBranch, commitSHA)
	if err != nil {
		return newExecExitError(execExitExecutionFailed, "REVIEW_DIFF_FAILED", err)
	}
	if strings.TrimSpace(diff) == "" {
		return newExecExitError(execExitUsage, "NO_REVIEW_CHANGES", fmt.Errorf("没有可审查的更改"))
	}
	reviewPrompt := buildReviewPrompt(diff, customPrompt, commitTitle, uncommitted, baseBranch, commitSHA, truncated)

	opts, err := parseExecOptionsNoPrompt(cmd)
	if err != nil {
		return err
	}
	opts.Prompt = reviewPrompt
	opts.PermissionMode = runtimepolicy.ModePlan
	opts.Ephemeral = true
	opts.Command = "review"
	opts.ReviewArgs = &ExecReviewArgs{
		Uncommitted: uncommitted,
		BaseBranch:  baseBranch,
		CommitSHA:   commitSHA,
		CommitTitle: commitTitle,
		Prompt:      customPrompt,
	}
	processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)
	if truncated {
		processor.OnWarning("diff 已截断，审查结果可能不完整")
	}
	session, cleanup, err := buildExecSession(cfg, opts, processor)
	if err != nil {
		return err
	}
	defer cleanup()
	return executeExecWithSignals(session)
}

func ensureGitRepository() error {
	cmd := osExec.Command("git", "rev-parse", "--is-inside-work-tree")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("当前目录不是 Git 仓库")
	}
	return nil
}

func getReviewDiff(uncommitted bool, baseBranch, commitSHA string) (string, bool, error) {
	var diff string
	var err error
	switch {
	case uncommitted:
		diff, err = getUncommittedReviewDiff()
	case strings.TrimSpace(baseBranch) != "":
		output, cmdErr := osExec.Command("git", "diff", strings.TrimSpace(baseBranch)+"...HEAD").Output()
		diff, err = string(output), cmdErr
	case strings.TrimSpace(commitSHA) != "":
		output, cmdErr := osExec.Command("git", "show", "--format=", strings.TrimSpace(commitSHA)).Output()
		diff, err = string(output), cmdErr
	default:
		err = fmt.Errorf("未指定审查目标")
	}
	if err != nil {
		return "", false, fmt.Errorf("git 命令执行失败: %w", err)
	}
	return truncateReviewDiff(diff)
}

func getUncommittedReviewDiff() (string, error) {
	output, err := osExec.Command("git", "diff", "HEAD", "--").Output()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.Write(output)
	untracked, err := osExec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return "", err
	}
	for _, file := range strings.Fields(string(untracked)) {
		content, readErr := os.ReadFile(file)
		if readErr != nil {
			fmt.Fprintf(&buf, "\n# untracked: %s（读取失败: %v）\n", file, readErr)
			continue
		}
		if bytes.IndexByte(content, 0) >= 0 {
			fmt.Fprintf(&buf, "\n# untracked binary file: %s\n", file)
			continue
		}
		fmt.Fprintf(&buf, "\ndiff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n", file, file, file)
		for _, line := range strings.Split(string(content), "\n") {
			if line == "" {
				fmt.Fprintln(&buf, "+")
				continue
			}
			fmt.Fprintf(&buf, "+%s\n", line)
		}
	}
	return buf.String(), nil
}

func truncateReviewDiff(diff string) (string, bool, error) {
	if len(diff) <= maxReviewDiffBytes {
		return diff, false, nil
	}
	return diff[:maxReviewDiffBytes] + "\n\n[warning] diff 已截断，审查结果可能不完整。\n", true, nil
}

func buildReviewPrompt(diff, customPrompt, commitTitle string, uncommitted bool, baseBranch, commitSHA string, truncated bool) string {
	var sb strings.Builder
	if customPrompt != "" {
		sb.WriteString(customPrompt)
	} else {
		sb.WriteString("请对以下代码变更进行审查。优先指出 bug、风险、行为回归和缺失测试。\n")
	}
	sb.WriteString("\n\n")
	switch {
	case uncommitted:
		sb.WriteString("## 审查目标：未提交的更改\n\n")
	case strings.TrimSpace(baseBranch) != "":
		sb.WriteString(fmt.Sprintf("## 审查目标：相对于 %s 分支的更改\n\n", strings.TrimSpace(baseBranch)))
	case strings.TrimSpace(commitSHA) != "":
		title := strings.TrimSpace(commitSHA)
		if strings.TrimSpace(commitTitle) != "" {
			title = fmt.Sprintf("%s (%s)", strings.TrimSpace(commitTitle), shortCommitSHA(commitSHA))
		}
		sb.WriteString(fmt.Sprintf("## 审查目标：提交 %s\n\n", title))
	}
	if truncated {
		sb.WriteString("> diff 已截断，审查结果可能不完整。\n\n")
	}
	sb.WriteString("```diff\n")
	sb.WriteString(diff)
	sb.WriteString("\n```\n")
	return sb.String()
}

func shortCommitSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}
