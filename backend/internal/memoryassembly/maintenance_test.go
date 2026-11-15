package memoryassembly

import (
	"context"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
)

func TestMaintenanceEnabledAndScheduleDerivation(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Generation.Enabled = true
	cfg.Memory.Generation.Trigger = "idle"
	cfg.Memory.Generation.TurnDigest.Enabled = true
	cfg.Memory.Generation.TurnDigest.IdleExtractMinutes = 7
	cfg.Memory.Freshness.Enabled = true
	cfg.Memory.Freshness.RevalidateIntervalHours = 3
	rt := &memoryruntime.Runtime{Config: memoryruntime.Config{TurnDigestIdleExtractMinutes: 7}}

	enabled := MaintenanceEnabled(MaintenanceOptions{Runtime: rt, Config: cfg})
	if !enabled {
		t.Fatal("idle/turn-digest/freshness config must enable maintenance")
	}
	derived := deriveMaintenanceSchedule(MaintenanceOptions{Runtime: rt, Config: cfg})
	if !derived.idleEnabled || !derived.turnDigestEnabled || !derived.freshnessEnabled {
		t.Fatalf("unexpected derived flags: %+v", derived)
	}
	if derived.idleExtractMinutes != 7 {
		t.Fatalf("idle extract minutes = %d, want 7", derived.idleExtractMinutes)
	}
	if derived.freshnessInterval.Hours() != 3 {
		t.Fatalf("freshness interval = %s, want 3h", derived.freshnessInterval)
	}
}

func TestMaintenanceStartStopIsSafeWhenDisabled(t *testing.T) {
	stop := StartMaintenance(context.Background(), MaintenanceOptions{})
	stop()
}
