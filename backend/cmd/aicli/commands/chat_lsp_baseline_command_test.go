package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChatLSPBaselineText 覆盖 /lsp baseline 的参数解析与报告渲染：
// 数据源是会话 runtime 事件日志（与池是否启用无关），未采集项必须 n/a。
func TestChatLSPBaselineText(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "2026", "09", "29", "sess_a", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"appended_bytes":100,"server":"gopls"}}`,
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(eventsDir, "runtime-events.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// --days 0 = 全窗口：不依赖运行日期，样例恒被计入。
	report := chatLSPBaselineText([]string{"--root", root, "--days", "0"})
	for _, want := range []string{"§4.3 基线登记表", "lsp_edit_coverage_ratio", "1.0000", "扫描根: " + root} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}

	if usage := chatLSPBaselineText([]string{"--help"}); !strings.Contains(usage, "用法") {
		t.Fatalf("help = %q", usage)
	}
	for _, bad := range [][]string{{"--days", "abc"}, {"--since", "2026/09/29"}, {"--unknown"}} {
		if out := chatLSPBaselineText(bad); !strings.Contains(out, "错误") {
			t.Fatalf("args %v should error, got %q", bad, out)
		}
	}

	// 无 LSP 活跃会话的窗口：覆盖率必须 n/a（§4.3 反模式），不得渲染成 0。
	empty := chatLSPBaselineText([]string{"--root", filepath.Join(root, "missing"), "--days", "0"})
	if !strings.Contains(empty, "n/a（窗口内无 LSP 活跃会话）") {
		t.Fatalf("empty window must report n/a coverage:\n%s", empty)
	}
}
