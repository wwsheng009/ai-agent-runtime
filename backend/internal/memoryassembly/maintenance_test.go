package memoryassembly

import (
	"context"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
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
	if derived.coreRebuildEnabled {
		t.Fatal("Core rebuild scheduler must remain opt-in by default")
	}
}

func TestMaintenanceCoreRebuildScheduleIsOptInAndUsesConfiguredInterval(t *testing.T) {
	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Generation.Enabled = false
	config.Memory.Generation.TurnDigest.Enabled = false
	config.Memory.Consolidation.Enabled = false
	config.Memory.Retention.Enabled = false
	config.Memory.Freshness.Enabled = false
	config.Memory.Injection.RecallTrace.RetentionDays = 0
	config.Memory.Feedback.ExposureRetentionDays = 0
	config.Memory.Core.Rebuild.Enabled = true
	config.Memory.Core.Rebuild.Interval = 3 * time.Minute

	runtime := &memoryruntime.Runtime{}
	options := MaintenanceOptions{Runtime: runtime, Config: config}
	schedule := deriveMaintenanceSchedule(options)
	if !MaintenanceEnabled(options) {
		t.Fatal("enabled Core rebuild must start maintenance")
	}
	if !schedule.coreRebuildEnabled {
		t.Fatal("Core rebuild scheduler was not derived")
	}
	if schedule.coreRebuildInterval != 3*time.Minute {
		t.Fatalf("core rebuild interval = %s, want 3m", schedule.coreRebuildInterval)
	}
}

func TestMaintenanceCoreRebuildSchedulerRunsBoundedWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "maintenance-core-rebuild",
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	defer store.Close()

	pinned := true
	if _, err := store.Append(memorystore.AppendNoteOptions{
		Text:   "The maintenance scheduler may only propose a reviewed Core candidate.",
		Pinned: &pinned,
	}); err != nil {
		t.Fatalf("append pinned source: %v", err)
	}

	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Mode = "on"
	config.Memory.Generation.Enabled = false
	config.Memory.Generation.TurnDigest.Enabled = false
	config.Memory.Consolidation.Enabled = false
	config.Memory.Retention.Enabled = false
	config.Memory.Freshness.Enabled = false
	config.Memory.Injection.RecallTrace.RetentionDays = 0
	config.Memory.Feedback.ExposureRetentionDays = 0
	config.Memory.Core.Rebuild.Enabled = true
	config.Memory.Core.Rebuild.Interval = 10 * time.Millisecond
	config.Memory.Core.Rebuild.MaxRecords = 4
	config.Memory.Core.Rebuild.MaxUsageRows = 4
	config.Memory.Core.Rebuild.TokenBudget = 240

	stageEvents := make(chan string, 4)
	runtime := &memoryruntime.Runtime{
		Store: store,
		Config: memoryruntime.Config{
			Mode:                    "on",
			WorkerID:                "maintenance-core-worker",
			CoreRebuildMaxRecords:   4,
			CoreRebuildMaxUsageRows: 4,
			CoreRebuildTokenBudget:  240,
		},
	}
	stop := StartMaintenance(ctx, MaintenanceOptions{
		Runtime:            runtime,
		Config:             config,
		OwnerID:            "maintenance-core-owner",
		OwnerRetryInterval: 5 * time.Millisecond,
		OnStage: func(stage, result string) {
			if stage == "core_rebuild" {
				stageEvents <- result
			}
		},
	})
	defer stop()

	select {
	case result := <-stageEvents:
		if result != "ok" {
			t.Fatalf("core rebuild stage result = %q", result)
		}
		stop()
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for automatic Core rebuild")
	}

	candidates, err := store.ListCore(ctx, memorystore.CoreMemoryFilter{
		Status: memorystore.CoreStatusCandidate, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list Core candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("automatic Core rebuild candidates = %d, want 1", len(candidates))
	}
	if candidates[0].Status != memorystore.CoreStatusCandidate {
		t.Fatalf("automatic Core rebuild status = %s, want candidate", candidates[0].Status)
	}
	if _, err := store.GetCore(ctx); err == nil {
		t.Fatal("automatic Core rebuild must not activate a candidate")
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
		wantTurnThreshold     bool
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
		{
			name:    "turn threshold trigger uses the threshold worker",
			trigger: "turn_threshold", generationEnabled: true, captureEnabled: true,
			recoveryOnStartup: false, wantMaintenance: true, wantTurnThreshold: true,
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
			if schedule.turnThresholdEnabled != test.wantTurnThreshold {
				t.Fatalf("turnThresholdEnabled = %t, want %t", schedule.turnThresholdEnabled, test.wantTurnThreshold)
			}
			if test.trigger == "turn_threshold" && schedule.periodicCaptureExtraction {
				t.Fatal("turn_threshold must not be bypassed by the generic periodic capture drain")
			}
		})
	}
}

func TestMaintenanceCaptureExtractionScheduleIndependentOfIdle(t *testing.T) {
	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Generation.Trigger = "session_end"
	config.Memory.Generation.TurnDigest.RecoveryOnStartup = false
	config.Memory.Maintenance.CaptureExtraction.Interval = 2 * time.Minute
	runtime := &memoryruntime.Runtime{}
	options := MaintenanceOptions{Runtime: runtime, Config: config}
	schedule := deriveMaintenanceSchedule(options)
	if !MaintenanceEnabled(options) || !schedule.periodicCaptureExtraction || schedule.idleEnabled {
		t.Fatalf("session_end maintenance schedule = %+v", schedule)
	}
	if schedule.captureExtractionInterval != 2*time.Minute {
		t.Fatalf("capture extraction interval = %s", schedule.captureExtractionInterval)
	}
	config.Memory.Maintenance.CaptureExtraction.Enabled = false
	options.Config = config
	schedule = deriveMaintenanceSchedule(options)
	if MaintenanceEnabled(options) == false || schedule.periodicCaptureExtraction {
		t.Fatalf("disabled extraction schedule should not enable its drain: %+v", schedule)
	}
}

func TestMaintenanceRepairsHistoricalJobDeadlinesOnlyWithOwnerLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "maintenance-deadline-repair",
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	defer store.Close()

	oldest := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	jobs := make([]memorystore.MemoryJob, 0, 2)
	for i, createdAt := range []time.Time{oldest, oldest.Add(time.Hour)} {
		job, ensureErr := store.EnsureJob(context.Background(), memorystore.MemoryJob{
			Kind: "phase1_extract", JobKey: []string{"historical-old", "historical-new"}[i],
			Status: memorystore.JobStatusPending, CreatedAt: createdAt,
		})
		if ensureErr != nil {
			t.Fatalf("EnsureJob(%d): %v", i, ensureErr)
		}
		jobs = append(jobs, job)
	}
	db, err := sqliteutil.OpenFile(store.DBPath(), true)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	for _, job := range jobs {
		if _, err := db.ExecContext(context.Background(),
			`UPDATE memory_jobs SET deadline_at = NULL WHERE id = ?`, job.ID); err != nil {
			_ = db.Close()
			t.Fatalf("clear fixture deadline: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	blocker, acquired, err := store.AcquireMaintenanceLease(
		context.Background(), "operator-hold", "test", time.Minute,
	)
	if err != nil || !acquired {
		t.Fatalf("acquire blocking lease = acquired:%t err:%v", acquired, err)
	}
	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Generation.Enabled = false
	config.Memory.Generation.TurnDigest.Enabled = false
	config.Memory.Consolidation.Enabled = false
	config.Memory.Freshness.Enabled = false
	config.Memory.Retention.Enabled = false
	config.Memory.Maintenance.JobDeadlineRepair = runtimecfg.MemoryJobDeadlineRepairConfig{
		Enabled: true, BatchSize: 1, Interval: 100 * time.Millisecond,
	}
	runtime := &memoryruntime.Runtime{Store: store}
	stageEvents := make(chan string, 4)
	stop := StartMaintenance(ctx, MaintenanceOptions{
		Runtime: runtime, Config: config,
		OwnerID: "deadline-repair-loop", OwnerRetryInterval: 10 * time.Millisecond,
		OnStage: func(stage, result string) {
			if stage == "job_deadline_repair" {
				stageEvents <- result
			}
		},
	})
	defer stop()

	time.Sleep(50 * time.Millisecond)
	summary, err := store.SummarizeJobs(context.Background())
	if err != nil {
		t.Fatalf("summarize while lease is blocked: %v", err)
	}
	if summary.MissingDeadlineJobs != len(jobs) {
		t.Fatalf("blocked repair changed jobs: missing deadlines = %d, want %d",
			summary.MissingDeadlineJobs, len(jobs))
	}
	if err := store.ReleaseMaintenanceLease(context.Background(), blocker); err != nil {
		t.Fatalf("release blocking lease: %v", err)
	}

	for remaining := len(jobs) - 1; remaining >= 0; remaining-- {
		select {
		case result := <-stageEvents:
			if result != "ok" {
				t.Fatalf("deadline repair stage result = %q", result)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for deadline repair pass; %d jobs should remain", remaining)
		}
		summary, err = store.SummarizeJobs(context.Background())
		if err != nil {
			t.Fatalf("summarize after repair pass: %v", err)
		}
		if summary.MissingDeadlineJobs != remaining {
			t.Fatalf("missing deadlines after pass = %d, want %d",
				summary.MissingDeadlineJobs, remaining)
		}
	}
}

func TestMaintenanceTurnThresholdHonorsCaptureExtractionGate(t *testing.T) {
	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Generation.Enabled = true
	config.Memory.Generation.Trigger = "turn_threshold"
	config.Memory.Generation.TurnDigest.Enabled = true
	config.Memory.Maintenance.CaptureExtraction.Enabled = false
	options := MaintenanceOptions{Runtime: &memoryruntime.Runtime{}, Config: config}

	schedule := deriveMaintenanceSchedule(options)
	if schedule.turnThresholdEnabled || schedule.periodicCaptureExtraction {
		t.Fatalf("disabled capture extraction must disable both workers: %+v", schedule)
	}
	if !schedule.captureEnabled {
		t.Fatal("turn receipt materialization remains independent from extraction")
	}
}

func TestMaintenanceStartStopIsSafeWhenDisabled(t *testing.T) {
	stop := StartMaintenance(context.Background(), MaintenanceOptions{})
	stop()
}

func TestMaintenanceModeOffRecordsSkipAndDoesNotAcquireLease(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "off", ProjectKey: "maintenance-off-test",
		Retention: memorystore.RetentionPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	defer store.Close()
	runtime := &memoryruntime.Runtime{Store: store}
	config := runtimecfg.DefaultRuntimeConfig()
	config.Memory.Retention.Enabled = true
	stop := StartMaintenance(context.Background(), MaintenanceOptions{
		Runtime: runtime, Config: config,
	})
	stop()
	stats := runtime.DigestStats()
	if stats.LastSkipReason != "memory_mode_off" {
		t.Fatalf("LastSkipReason = %q, want memory_mode_off", stats.LastSkipReason)
	}
	if !stats.LastMaintenanceAt.IsZero() {
		t.Fatalf("mode-off maintenance must not report a stage: %+v", stats)
	}
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
