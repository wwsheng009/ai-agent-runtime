package runtimeapi

import (
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestBuildSessionLoopConfigPreservesContextAccountingMode(t *testing.T) {
	loop := buildSessionLoopConfig(&runtimecfg.RuntimeConfig{
		Context: runtimecfg.ContextConfig{AccountingMode: "estimate_only"},
	})
	if loop == nil {
		t.Fatal("loop config is nil")
	}
	if loop.ContextAccountingMode != "estimate_only" {
		t.Fatalf("ContextAccountingMode = %q, want estimate_only", loop.ContextAccountingMode)
	}
}

func TestBuildSessionLoopConfigLeavesAccountingModeUnsetByDefault(t *testing.T) {
	loop := buildSessionLoopConfig(nil)
	if loop == nil {
		t.Fatal("loop config is nil")
	}
	if loop.ContextAccountingMode != "" {
		t.Fatalf("ContextAccountingMode = %q, want empty", loop.ContextAccountingMode)
	}
}
