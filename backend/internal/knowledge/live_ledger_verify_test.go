package knowledge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// TestLiveLedgerVerify 是 live 接入验证的复算入口（临时脚本，验证后删除）：
// 读取真实会话写入的账本 DB，走生产 reader + SummarizeAttribution 复算 M1–M4。
func TestLiveLedgerVerify(t *testing.T) {
	path := os.Getenv("KNOWLEDGE_LIVE_LEDGER")
	if path == "" {
		t.Skip("KNOWLEDGE_LIVE_LEDGER not set")
	}
	store, err := usageledger.NewSQLiteStore(&usageledger.Config{Driver: "sqlite", DSN: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.ListExplorationAttribution(context.Background(), time.Time{}, 1000)
	require.NoError(t, err)

	report := SummarizeAttribution(rows, DefaultShadowAlpha)
	t.Logf("calls=%d denominator=%d zero=%d usable=%d M1=%.4f M2=%.4f M3=%.4f M4=%.4f",
		report.Calls, report.Denominator, report.ZeroBaseline, report.Usable,
		report.M1, report.M2, report.M3, report.M4)
	for tool, b := range report.ByTool {
		t.Logf("tool=%s calls=%d denom=%d usable=%d M1=%.4f M2=%.4f M3=%.4f coverage_p50=%.4f",
			tool, b.Calls, b.Denominator, b.Usable, b.M1, b.M2, b.M3, b.CoverageP50)
	}
	t.Logf("calibrated_alpha=%.2f", CalibrateShadowAlpha(rows))
}
