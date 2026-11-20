package tools

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
)

func bashSearchNoMatchNextAction() string {
	return "Treat this as no-match / empty evidence, not a hard crash. Prefer toolkit `grep` (literal/paths/glob) for code search; change pattern or broaden path/workdir. Do not retry the identical shell query unchanged."
}

func buildBashBatchFailureError(failed int, items []map[string]interface{}) error {
	parts := []string{fmt.Sprintf("bash command batch completed with %d failure(s)", failed)}
	summaries := make([]string, 0, 3)
	for _, item := range items {
		if success, _ := item["success"].(bool); success {
			continue
		}
		index, _ := item["index"].(int)
		command := mutatecore.TruncateDiagnosticText(extractString(item["command"]), 80)
		errText := mutatecore.TruncateDiagnosticText(extractString(item["error"]), 120)
		if errText == "" {
			errText = "unknown error"
		}
		summaries = append(summaries, fmt.Sprintf("#%d %s => %s", index+1, command, errText))
		if len(summaries) >= 3 {
			break
		}
	}
	if len(summaries) > 0 {
		parts = append(parts, "失败摘要: "+strings.Join(summaries, "; "))
	}
	parts = append(parts, "请查看 Content 中对应 command 段落输出后修复失败命令，或用 stop_on_error=true 在首个失败后停止")
	return fmt.Errorf("%s", strings.Join(parts, "。"))
}

func isShellTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) || runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded)
}

func normalizeRipgrepOutput(output []byte) []string {
	lines, _ := normalizeRipgrepOutputWithStats(output)
	return lines
}

func writeContentRevision(exists bool, content string) string {
	return mutatecore.Revision(exists, content)
}
