package knowledge

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// W7 测量装置的 live 入口（仓库既有惯例：env 门控、默认跳过；见
// live_ledger_verify_test.go / live_sampling_test.go）。
//
// 两个入口分工：
//   - TestLivePhase2LedgerG2：直读真实 usageledger DB，复算 G2 硬门槛
//     （unsafe_reuse_count / knowledge_version_mismatch_count 必须为 0）。
//   - TestLivePhase2Report：从任务级样本 JSON（A/B 两臂 + G4 证据）复算
//     G1–G4 全量报告。样本提取口径见
//     docs/knowledge_Layer/reports/phase2_exploration_report.md §实测步骤。
//
// 两个入口都不编造数据：未设置 env 时跳过；设置后只复算输入中的真实记录。

// phase2LedgerLimit 是 live 入口一次读取的 ledger 行数上限（防误拉整表）。
const phase2LedgerLimit = 100000

// TestLivePhase2LedgerG2 从真实 ledger 复算 G2 硬门槛。
//
//	KNOWLEDGE_PHASE2_LEDGER=<usage ledger sqlite 路径>
//	KNOWLEDGE_PHASE2_SINCE=<RFC3339，可选；缺省读全表窗口>
func TestLivePhase2LedgerG2(t *testing.T) {
	path := os.Getenv("KNOWLEDGE_PHASE2_LEDGER")
	if path == "" {
		t.Skip("KNOWLEDGE_PHASE2_LEDGER not set")
	}
	since := time.Time{}
	if raw := os.Getenv("KNOWLEDGE_PHASE2_SINCE"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		require.NoError(t, err, "KNOWLEDGE_PHASE2_SINCE must be RFC3339")
		since = parsed
	}
	store, err := usageledger.NewSQLiteStore(&usageledger.Config{Driver: "sqlite", DSN: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	records, err := store.GetSince(since, phase2LedgerLimit)
	require.NoError(t, err)

	// G2 硬门槛直读：unsafe_reuse_count / knowledge_version_mismatch_count。
	g2 := G2Result{}
	for _, record := range records {
		if record == nil {
			continue
		}
		g2.UnsafeReuseCount += record.UnsafeReuseCount
		g2.VersionMismatches += record.KnowledgeVersionMismatchCount
	}
	g2.Pass = g2.UnsafeReuseCount == 0 && g2.VersionMismatches == 0
	if g2.UnsafeReuseCount != 0 {
		g2.Violations = append(g2.Violations, "unsafe_reuse_count != 0")
	}
	if g2.VersionMismatches != 0 {
		g2.Violations = append(g2.Violations, "knowledge_version_mismatch_count != 0")
	}

	baseline := Summarize(records)
	t.Logf("ledger: records=%d since=%s", len(records), since.Format(time.RFC3339))
	t.Logf("g2: unsafe_reuse_count=%d knowledge_version_mismatch_count=%d pass=%t violations=%v",
		g2.UnsafeReuseCount, g2.VersionMismatches, g2.Pass, g2.Violations)
	t.Logf("baseline(ledger): tasks=%d total_tokens=%d exploration_tokens=%d reuse_tokens=%d tool_calls_per_task=%.3f repeated_reads=%d",
		baseline.Tasks, baseline.TotalTokens, baseline.ExplorationTokens, baseline.ReuseTokens,
		baseline.ToolCallsPerTask, baseline.RepeatedReads)
	t.Logf("note: stale_item_injected 不在 ledger（contextmgr metadata，当前恒 0）；G1/G3 需任务级两臂样本，见 TestLivePhase2Report")

	// G2 是硬门槛：真实数据出现一次即失败（04 §7.3）。
	require.True(t, g2.Pass, "G2 hard gate violated on real ledger: %v", g2.Violations)
}

// phase2SamplesFile 是 TestLivePhase2Report 的输入格式。
type phase2SamplesFile struct {
	Baseline []TaskSample `json:"baseline"`
	Current  []TaskSample `json:"current"`
	G4       G4Evidence   `json:"g4"`
}

// TestLivePhase2Report 从任务级两臂样本复算 G1–G4（真实 A/B 的复算入口）。
//
//	KNOWLEDGE_PHASE2_SAMPLES_JSON=<样本 JSON 路径>
//
// 样本 JSON 由真实 ledger + 会话埋点导出（口径见报告 §实测步骤）；本测试
// 只复算、不生成，避免把装置自检数字混入实测报告。
func TestLivePhase2Report(t *testing.T) {
	path := os.Getenv("KNOWLEDGE_PHASE2_SAMPLES_JSON")
	if path == "" {
		t.Skip("KNOWLEDGE_PHASE2_SAMPLES_JSON not set")
	}
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	var samples phase2SamplesFile
	require.NoError(t, json.Unmarshal(payload, &samples), "样本 JSON 解析失败")

	report := SummarizePhase2(samples.Baseline, samples.Current, samples.G4)
	rendered, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	t.Logf("phase2 report (%s):\n%s", path, rendered)

	// 同一批输入两次复算必须逐位一致（06 §4 W7「复算一致性」）。
	again := SummarizePhase2(samples.Baseline, samples.Current, samples.G4)
	require.Equal(t, report, again, "复算必须逐位一致")

	// G2 硬门槛在真实样本上必须为 0（04 §7.3）。
	require.True(t, report.G2.Pass, "G2 hard gate violated: %v", report.G2.Violations)
}
