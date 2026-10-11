package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// executeDetachedManaged routes detach=true through the injected background
// submitter so the launch becomes a supervised job: output is captured under
// the job's log/buffer, the lifecycle is CAS-guarded, the process tree is
// tracked, and terminal transitions reach the supervision wake path
// (2026-10-11). The owning session comes from the ambient context recorded by
// the executor path (WithBackgroundSubmitterSession).
func (b *BashTool) executeDetachedManaged(ctx context.Context, command, resolvedWorkdir string) (*toolkit.ToolResult, error) {
	submitter := b.bgSubmitter
	if submitter == nil {
		return nil, fmt.Errorf("background submitter is not wired")
	}
	sessionID := toolctx.SessionID(ctx)

	var (
		jobID  string
		status string
		err    error
	)
	if managedDetachPrefersInteractive(command) {
		// Long-lived programs (dev servers, REPLs, TUIs) become interactive
		// sessions so the model can drive them with write_stdin instead of
		// relaunching (2026-10-11).
		jobID, status, err = submitter.OpenInteractiveSession(ctx, sessionID, command, resolvedWorkdir, false, 0, 0)
	} else {
		jobID, status, err = submitter.SubmitBackgroundShell(ctx, sessionID, command, resolvedWorkdir)
	}
	if err != nil {
		return toolResultFailureWithCode(
			fmt.Errorf("submit background job: %w", err),
			"PROCESS_START_FAILED",
			"Retry the command, or run it in the foreground to surface its output.",
			map[string]interface{}{"detach_managed_failed": true},
		), nil
	}

	kind := "作业"
	if managedDetachPrefersInteractive(command) {
		kind = "交互会话"
	}
	content := fmt.Sprintf(
		"已在后台托管启动（detach=true → 受管%s）。\nJob ID: %s\nStatus: %s\n命令: %s\n说明: 输出由后台管理器捕获；用 task_output 按 job_id 读取（支持 wait=output/exit），终态会触发会话唤醒。%s",
		kind, jobID, status, command,
		managedDetachNextActionHint(command),
	)
	metadata := map[string]interface{}{
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"detached":                    true,
		"detached_managed":            true,
		"job_id":                      jobID,
		"job_status":                  status,
		"executed":                    true,
		toolresult.MetadataNextActionKey: fmt.Sprintf(
			"Managed background %s started (job_id=%s, status=%s); read its output with task_output (wait=output|exit)%s instead of relaunching the command.",
			kind, jobID, status, managedDetachInteractiveHint(command),
		),
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata:   metadata,
	}, nil
}

func managedDetachInteractiveHint(command string) string {
	if managedDetachPrefersInteractive(command) {
		return " and drive it with write_stdin"
	}
	return ""
}

func managedDetachNextActionHint(command string) string {
	if managedDetachPrefersInteractive(command) {
		return "交互会话可用 write_stdin 继续驱动（发送命令行时以 \\r 结尾）。"
	}
	return ""
}

// managedDetachPrefersInteractive reports whether a detach call should open an
// interactive session instead of a one-shot job. Heuristic (2026-10-11):
// commands that clearly start a long-lived program (dev servers, REPLs, TUIs)
// benefit from a write_stdin-driven session; everything else stays one-shot.
// Conservative on purpose — misclassifying a one-shot as a session costs an
// extra close, while the reverse would hang a job that never exits.
func managedDetachPrefersInteractive(command string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(command))
	if trimmed == "" {
		return false
	}
	markers := []string{
		"npm run dev", "npm start", "pnpm dev", "pnpm start", "yarn dev",
		"uvicorn", "flask run", "vite", "next dev", "air",
	}
	if runtime.GOOS != "windows" {
		markers = append(markers, "python -m http.server", "python3 -m http.server")
	}
	for _, marker := range markers {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	return false
}
