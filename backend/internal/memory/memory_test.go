package memory

import (
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

func TestMemory_GetStatsInitializesToolMapAndUsage(t *testing.T) {
	mem := NewMemory(4)
	mem.CreateSession("session-1")
	mem.Add(types.Observation{Step: "s1", Tool: "search_docs", Success: true, Output: "ok"})
	mem.Add(types.Observation{Step: "s2", Tool: "search_docs", Success: false, Error: "failed"})
	mem.Add(types.Observation{Step: "s3", Tool: "run_tests", Success: true, Output: "pass"})

	stats := mem.GetStats()
	if stats == nil {
		t.Fatal("expected stats")
	}
	if stats.ObservationsByTool == nil {
		t.Fatal("expected ObservationsByTool map to be initialized")
	}
	if got := stats.ObservationsByTool["search_docs"]; got != 2 {
		t.Fatalf("expected search_docs count=2, got %d", got)
	}
	if got := stats.ObservationsByTool["run_tests"]; got != 1 {
		t.Fatalf("expected run_tests count=1, got %d", got)
	}
	if stats.TotalSessions != 1 {
		t.Fatalf("expected total sessions=1, got %d", stats.TotalSessions)
	}
	if stats.Usage <= 0.74 || stats.Usage >= 0.76 {
		t.Fatalf("expected usage about 0.75, got %f", stats.Usage)
	}
}

func TestMemory_GetStatsHandlesZeroMaxSize(t *testing.T) {
	mem := NewMemory(2)
	mem.Add(types.Observation{Step: "s1", Tool: "search_docs", Success: true, Output: "ok"})
	mem.SetMaxSize(0)

	stats := mem.GetStats()
	if stats == nil {
		t.Fatal("expected stats")
	}
	if stats.Usage != 0 {
		t.Fatalf("expected usage=0 when max size <= 0, got %f", stats.Usage)
	}
}
