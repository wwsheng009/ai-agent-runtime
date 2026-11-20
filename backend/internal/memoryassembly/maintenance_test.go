package memoryassembly

import (
	"context"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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

func TestMaintenanceRecoveryAndCaptureSchedulePolicy(t *testing.T) {
	tests := []struct {
		name                  string
		trigger               string
		generationEnabled     bool
		captureEnabled        bool
		recoveryOnStartup     bool
		wantMaintenance       bool
		wantStartupRecovery   bool
		wantStartupExtraction bool
		wantIdle              bool
	}{
		{
			name:    "startup recovery enabled without idle trigger",
			trigger: "session_end", generationEnabled: true, captureEnabled: true,
			recoveryOnStartup: true, wantMaintenance: true,
			wantStartupRecovery: true, wantStartupExtraction: true,
		},
		{
			name:    "manual trigger does not become automatic startup recovery",
			trigger: "manual", generationEnabled: true, captureEnabled: true,
			recoveryOnStartup: true, wantMaintenance: true,
		},
		{
			name:    "disabled generation keeps durable capture materializer but no worker",
			trigger: "manual", generationEnabled: false, captureEnabled: true,
			recoveryOnStartup: true, wantMaintenance: true,
		},
		{
			name:    "idle trigger enables retry workers",
			trigger: "idle", generationEnabled: true, captureEnabled: true,
			recoveryOnStartup: false, wantMaintenance: true, wantIdle: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := runtimecfg.DefaultRuntimeConfig()
			config.Memory.Generation.Trigger = test.trigger
			config.Memory.Generation.Enabled = test.generationEnabled
			config.Memory.Generation.TurnDigest.Enabled = test.captureEnabled
			config.Memory.Generation.TurnDigest.RecoveryOnStartup = test.recoveryOnStartup
			runtime := &memoryruntime.Runtime{Config: memoryruntime.Config{
				TurnDigestIdleExtractMinutes: 1,
			}}
			options := MaintenanceOptions{Runtime: runtime, Config: config}
			schedule := deriveMaintenanceSchedule(options)
			if enabled := MaintenanceEnabled(options); enabled != test.wantMaintenance {
				t.Fatalf("MaintenanceEnabled = %t, want %t", enabled, test.wantMaintenance)
			}
			if schedule.startupRecovery != test.wantStartupRecovery {
				t.Fatalf("startupRecovery = %t, want %t", schedule.startupRecovery, test.wantStartupRecovery)
			}
			if schedule.captureStartupWorker != test.wantStartupExtraction {
				t.Fatalf("captureStartupWorker = %t, want %t", schedule.captureStartupWorker, test.wantStartupExtraction)
			}
			if schedule.idleEnabled != test.wantIdle {
				t.Fatalf("idleEnabled = %t, want %t", schedule.idleEnabled, test.wantIdle)
			}
		})
	}
}

func TestMaintenanceStartStopIsSafeWhenDisabled(t *testing.T) {
	stop := StartMaintenance(context.Background(), MaintenanceOptions{})
	stop()
}

func TestMaintenanceOwnerRenewsAndReleasesLease(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "maintenance-loop-test",
		Retention: memorystore.RetentionPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	defer store.Close()

	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Retention.Enabled = true
	runtime := &memoryruntime.Runtime{Store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartMaintenance(ctx, MaintenanceOptions{
		Runtime:            runtime,
		Config:             config,
		OwnerID:            "maintenance-loop",
		OwnerLeaseTTL:      250 * time.Millisecond,
		OwnerRetryInterval: 10 * time.Millisecond,
	})

	competitor := "maintenance-contender"
	var observedOwner bool
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		lease, acquired, acquireErr := store.AcquireMaintenanceLease(
			context.Background(), competitor, "test", time.Second,
		)
		if acquireErr != nil {
			t.Fatalf("observe maintenance owner: %v", acquireErr)
		}
		if !acquired {
			if lease.OwnerID != "maintenance-loop" {
				t.Fatalf("active owner = %q, want maintenance-loop", lease.OwnerID)
			}
			observedOwner = true
			break
		}
		if err := store.ReleaseMaintenanceLease(context.Background(), lease); err != nil {
			t.Fatalf("release probe lease: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observedOwner {
		stop()
		t.Fatal("maintenance loop did not acquire its owner lease")
	}

	time.Sleep(600 * time.Millisecond)
	if _, acquired, err := store.AcquireMaintenanceLease(
		context.Background(), competitor, "test", time.Second,
	); err != nil {
		t.Fatalf("check renewed lease: %v", err)
	} else if acquired {
		t.Fatal("maintenance owner lease expired instead of being renewed")
	}

	stop()
	replacement, acquired, err := store.AcquireMaintenanceLease(
		context.Background(), competitor, "test", time.Second,
	)
	if err != nil || !acquired {
		t.Fatalf("acquire after stop = %+v, acquired=%v, err=%v", replacement, acquired, err)
	}
}
