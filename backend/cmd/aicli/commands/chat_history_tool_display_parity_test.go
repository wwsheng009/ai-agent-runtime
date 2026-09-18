package commands

import (
	"strings"
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// realPersistedToolMessage 复刻真实库中该会话 seq=4 的持久化消息。
func realPersistedToolMessage() runtimetypes.Message {
	metadata := runtimetypes.NewMetadata()
	metadata["tool_name"] = "shell"
	metadata["tool_source"] = "toolkit"
	metadata["outcome"] = "success"
	metadata["ok"] = true
	metadata["tool_invocation"] = map[string]interface{}{
		"attempted_args": map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}
	metadata["tool_metadata"] = map[string]interface{}{
		"capture_limit_reached":   false,
		"captured_output_bytes":   17,
		"command":                 "echo E2E_SNAPSHOT_OK",
		"duration_ms":             602,
		"exit_code":               0,
		"output_capture_complete": true,
		"output_kind":             "text",
		"shell_display":           `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		"shell_path":              `C:\Program Files\PowerShell\7\pwsh.exe`,
		"shell_type":              "pwsh",
	}
	return runtimetypes.Message{
		Role: "tool",
		Content: "Exit code: 0\nShell: pwsh\nWorkdir: E:\\projects\\ai\\ai-agent-runtime\\backend\n" +
			"Wall time: 602ms\nTimed out: false\nOutput:\nE2E_SNAPSHOT_OK\n\n" +
			"Full raw output artifact_id: art_d00815f1e8534555acdfd64dca1a812c size=133 kind=text; " +
			"read the full raw output via the artifact read tool; never pass this id to task_output",
		ToolCallID: "call_00_FkGDA5eq5Wz3jmrsnec76430",
		Metadata:   metadata,
	}
}

// TestChatHistoryToolDisplayUsesCompactSummaryLikeLive 用真实持久化数据锁定
// 重放显示形态：必须是与实时同款的 compact 摘要（标题 + command + 摘要行），
// 而不是模型面向原文（含 artifact 指针）。
//
// 已知残差（来源为不可持久化的实时事件载荷，不属本回归范围）：
//   - 标题耗时用持久化的 execution duration（602ms），live 捕获为事件口径
//     的 608ms；
//   - live 捕获中的 "stdout" 行来自 tool.progress 流标签（仅实时事件载荷，
//     不落库）；重放不伪造流式段标签。
func TestChatHistoryToolDisplayUsesCompactSummaryLikeLive(t *testing.T) {
	message := realPersistedToolMessage()
	got := chatHistoryToolDisplay(message, "shell", nil)
	t.Logf("reconstructed=%q", got)

	for _, want := range []string{
		"• Completed echo E2E_SNAPSHOT_OK", // 命令进入标题（而非 tool 名）
		"Exit code: 0",
		"Shell: pwsh",
		"Workdir: E:\\projects\\ai\\ai-agent-runtime\\backend",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact display missing %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "Full raw output artifact_id") {
		t.Fatalf("raw artifact pointer leaked into replay display:\n%q", got)
	}
	if strings.Contains(got, "pwsh.exe") {
		t.Fatalf("shell executable path leaked into replay display:\n%q", got)
	}
	if strings.Count(got, "\n") != 3 {
		t.Fatalf("expected title + 3 tree lines (summary preview), got:\n%q", got)
	}
}
