package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestChatDebugHistoryEffectSummaryKeepsLegacyCounterAliases 钉住 P1-1/P2 计数
// 改名后 resume 性能门禁（scripts/test-aicli-resume-startup-perf-e2e.ps1）与
// E2E-RESUME-01 依赖的旧字段仍可解析，且映射口径正确：
//
//	pending   ← queued（claimed 已折叠进 queued）
//	in-flight ← claimed-token 非零（单飞 claim 未结算）
//	acked     ← delivered + compacted（累计已交付 token/行，单调）
//
// 回归背景：别名缺失时门禁的收敛判据（pending=0 且 in-flight=0 且
// acked>=cells）永远不满足，会把健康的 resume 误判为「未收敛」并挂到超时。
func TestChatDebugHistoryEffectSummaryKeepsLegacyCounterAliases(t *testing.T) {
	effects := ui.HistoryEffectDiagnostics{
		Summary: ui.HistoryEffectQueueSummary{
			Queued:          3,
			Delivered:       5,
			LedgerCompacted: 7,
		},
		NextToken: 20,
	}
	summary := chatDebugHistoryEffectSummary(effects)
	for _, want := range []string{"pending=3", "in-flight=0", "acked=12"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing legacy alias %q:\n%s", want, summary)
		}
	}

	effects.Summary.ClaimedToken = 42
	summary = chatDebugHistoryEffectSummary(effects)
	if !strings.Contains(summary, "in-flight=1") {
		t.Fatalf("claimed token must report in-flight=1:\n%s", summary)
	}
}
