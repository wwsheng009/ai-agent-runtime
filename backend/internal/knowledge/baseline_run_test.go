package knowledge

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// TestPhase0TaskBaselineSummarize runs the Phase 0 task-side baseline data face
// (04 §7.7) against the 5 real tasks just executed via aicli-ledger (mode=off).
//
// This is a manual baseline-run test (requires gateway.db with real ledger records);
// it skips cleanly when the DB is absent so CI is unaffected.
func TestPhase0TaskBaselineSummarize(t *testing.T) {
	dbPath := "E:/projects/ai/ai-agent-runtime/backend/data/gateway.db"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skipf("gateway.db not found at %s; skipping task-side baseline run", dbPath)
	}
	store, err := usageledger.NewSQLiteStore(&usageledger.Config{
		Driver: "sqlite",
		DSN:    dbPath,
	})
	if err != nil {
		t.Fatalf("open usage ledger store: %v", err)
	}
	defer store.Close()

	// The 5 baseline tasks ran at 2026-09-21 03:37:33–03:38:06 UTC.
	since := time.Date(2026, 9, 21, 3, 37, 0, 0, time.UTC)
	records, err := store.GetSince(since, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	// Filter to llm_runtime (aicli) records — the task-side baseline sample.
	var llmRecords []*entity.TokenUsageHistory
	for _, r := range records {
		if sub, ok := r.Metadata["subsystem"].(string); ok && sub == "llm_runtime" {
			llmRecords = append(llmRecords, r)
		}
	}

	report := Summarize(llmRecords)
	fmt.Println("=== Phase 0 task-side baseline (knowledge.mode=off) ===")
	fmt.Printf("sample: %d LLM requests across 5 real tasks\n", report.Tasks)
	fmt.Printf("TotalTokens: %d\n", report.TotalTokens)
	fmt.Printf("SuccessfulTasks: %d  FailedTasks: %d\n", report.SuccessfulTasks, report.FailedTasks)
	fmt.Printf("ExplorationTokenShare: %.6f (expect 0 in mode=off)\n", report.ExplorationTokenShare())
	fmt.Printf("ReuseTokenShare: %.6f\n", report.ReuseTokenShare())
	fmt.Printf("ToolCallsPerTask: %.2f\n", report.ToolCallsPerTask)
	fmt.Printf("RepeatedReadPerTask: %.2f\n", report.RepeatedReadPerTask())
	fmt.Printf("IndexHitRate: %.4f\n", report.IndexHitRate())
	fmt.Printf("FallbackRate: %.4f\n", report.FallbackRate())
	fmt.Printf("SafetyViolations: %v\n", report.SafetyViolations())

	// Per-task latency (ms) from invoke responses (5 tasks).
	latencies := []float64{12600, 8300, 4400, 5400, 6000}
	p50, p95 := Percentiles(latencies)
	fmt.Printf("Latency p50: %.0fms  p95: %.0fms\n", p50, p95)

	fmt.Println("\nper-record breakdown:")
	for _, r := range llmRecords {
		fmt.Printf("  req=%s in/out/total=%d/%d/%d step=%v model=%s\n",
			r.RequestID, r.InputTokens, r.OutputTokens, r.TotalTokens,
			r.Metadata["step"], r.ModelID)
	}
}
