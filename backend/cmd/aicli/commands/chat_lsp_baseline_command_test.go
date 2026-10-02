package commands

import (
	"strings"
	"testing"
)

// TestChatLSPBaselineText 覆盖 /lsp baseline 的参数解析与报告渲染：
// 数据源是分析库（与池是否启用无关），未采集项必须 n/a。
func TestChatLSPBaselineText(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"lsp.request.finished","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"trigger":"inline","outcome":"injected","duration_ms":10,"diag_count":2,"appended_bytes":100,"server":"gopls"}}`,
		`{"type":"tool.completed","session_id":"s1","timestamp":"2026-09-29T10:00:00Z","payload":{"logical_tool":"apply_patch","output_model_visible_bytes":1000}}`,
		"",
	}, "\n")
	seedLSPBaselineTestDB(t, body)

	// --days 0 = 全窗口：不依赖运行日期，样例恒被计入。
	report := chatLSPBaselineText([]string{"--days", "0"})
	// "扫描根" 已随事实源迁移移出报告——扫描量是日志健康度，不是数据。
	for _, want := range []string{"§4.3 基线登记表", "lsp_edit_coverage_ratio", "1.0000", "数据源: 分析库"} {
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

	// 空库：所有读数未采集（n/a），不得渲染成 0。
	seedLSPBaselineTestDB(t, "")
	empty := chatLSPBaselineText([]string{"--days", "0"})
	if !strings.Contains(empty, "n/a（窗口内无 LSP 活跃会话）") {
		t.Fatalf("empty store must report n/a coverage:\n%s", empty)
	}
}
