package memoryassembly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// MaintenanceHost is the host-owned half of the memory maintenance loop.
// Runtime methods stay on the shared object; only host-specific logging and
// consolidation construction are callbacks.
type MaintenanceHost interface {
	RunConsolidation(context.Context) error
}

type MaintenanceOptions struct {
	Runtime *memoryruntime.Runtime
	Config  *runtimecfg.RuntimeConfig
	Host    MaintenanceHost
	OnError func(stage string, err error)

	// OwnerID is normally generated once per maintenance-loop incarnation.
	// The other lease settings are injectable for bounded tests/embedders.
	OwnerID            string
	OwnerKind          string
	OwnerLeaseTTL      time.Duration
	OwnerRetryInterval time.Duration
}

const (
	defaultMaintenanceOwnerKind  = "runtime"
	defaultMaintenanceLeaseTTL   = time.Minute
	defaultMaintenanceLeaseRetry = 5 * time.Second
)

// MaintenanceEnabled reports whether any ticker/maintenance feature is on.
func MaintenanceEnabled(opts MaintenanceOptions) bool {
	cfg := deriveMaintenanceSchedule(opts)
	return cfg.idleEnabled || cfg.consolidationEnabled || cfg.retentionEnabled ||
		cfg.lifecycleEnabled || cfg.turnDigestEnabled || cfg.captureEnabled ||
		cfg.startupRecovery || cfg.freshnessEnabled
}

// StartMaintenance starts the shared ticker loop and returns a stop function
// that cancels the loop and waits for its goroutine. The returned stop is
// always non-nil.
func StartMaintenance(ctx context.Context, opts MaintenanceOptions) func() {
	if !MaintenanceEnabled(opts) {
		return func() {}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMaintenance(loopCtx, opts)
	}()
	return func() {
		cancel()
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			if opts.OnError != nil {
				opts.OnError("maintenance_stop_timeout", context.DeadlineExceeded)
			}
		}
	}
}

type maintenanceSchedule struct {
	idleEnabled          bool
	consolidationEnabled bool
	retentionEnabled     bool
	lifecycleEnabled     bool
	freshnessEnabled     bool
	turnDigestEnabled    bool
	captureEnabled       bool
	startupRecovery      bool
	captureStartupWorker bool
	idleExtractMinutes   int
	freshnessInterval    time.Duration
}

func deriveMaintenanceSchedule(opts MaintenanceOptions) maintenanceSchedule {
	cfg := opts.Config
	rt := opts.Runtime
	result := maintenanceSchedule{}
	if cfg == nil || rt == nil {
		return result
	}
	generation := cfg.Memory.Generation
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	result.idleEnabled = generation.Enabled && (trigger == "idle" || trigger == "both")
	result.consolidationEnabled = cfg.Memory.Consolidation.Enabled
	result.retentionEnabled = cfg.Memory.Injection.RecallTrace.RetentionDays > 0 ||
		cfg.Memory.Feedback.ExposureRetentionDays > 0
	result.lifecycleEnabled = cfg.Memory.Retention.Enabled
	result.freshnessEnabled = cfg.Memory.Freshness.Enabled
	result.freshnessInterval = time.Duration(cfg.Memory.Freshness.RevalidateIntervalHours) * time.Hour
	if result.freshnessInterval <= 0 {
		result.freshnessInterval = 24 * time.Hour
	}
	result.turnDigestEnabled = generation.Enabled && generation.TurnDigest.Enabled
	// Capture/materialization is independent from the LLM generation gate. A
	// disabled generator must not strand canonical receipts; they remain
	// materializable and visible as queued/deferred work.
	result.captureEnabled = generation.TurnDigest.Enabled
	result.startupRecovery = generation.Enabled &&
		generation.TurnDigest.RecoveryOnStartup && trigger != "manual"
	result.captureStartupWorker = result.captureEnabled && result.startupRecovery
	result.idleExtractMinutes = rt.Config.TurnDigestIdleExtractMinutes
	if result.idleExtractMinutes <= 0 {
		result.idleExtractMinutes = 30
	}
	return result
}

func runMaintenance(ctx context.Context, opts MaintenanceOptions) {
	rt := opts.Runtime
	if rt == nil || rt.Store == nil {
		return
	}
	storeMode := strings.ToLower(strings.TrimSpace(rt.Store.Mode()))
	if storeMode != "on" && storeMode != "shadow" {
		return
	}
	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		ownerID = uuid.NewString()
	}
	ownerKind := strings.TrimSpace(opts.OwnerKind)
	if ownerKind == "" {
		ownerKind = defaultMaintenanceOwnerKind
	}
	leaseTTL := opts.OwnerLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultMaintenanceLeaseTTL
	}
	retryInterval := opts.OwnerRetryInterval
	if retryInterval <= 0 {
		retryInterval = defaultMaintenanceLeaseRetry
	}
	report := func(stage string, err error) {
		if err != nil && opts.OnError != nil {
			opts.OnError(stage, err)
		}
	}
	for ctx.Err() == nil {
		lease, acquired, err := rt.Store.AcquireMaintenanceLease(ctx, ownerID, ownerKind, leaseTTL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			report("maintenance_owner_acquire", err)
			if !waitMaintenanceRetry(ctx, retryInterval) {
				return
			}
			continue
		}
		if !acquired {
			if !waitMaintenanceRetry(ctx, retryInterval) {
				return
			}
			continue
		}
		ownedCtx, cancel := context.WithCancel(ctx)
		leaseCtx := memorystore.WithMaintenanceLease(ownedCtx, lease)
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			interval := leaseTTL / 3
			if interval <= 0 {
				interval = time.Second
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ownedCtx.Done():
					return
				case <-ticker.C:
					if _, renewErr := rt.Store.RenewMaintenanceLease(ownedCtx, lease, leaseTTL); renewErr != nil {
						report("maintenance_owner_renew", renewErr)
						cancel()
						return
					}
				}
			}
		}()
		runMaintenanceOwned(leaseCtx, opts)
		cancel()
		<-heartbeatDone
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := rt.Store.ReleaseMaintenanceLease(releaseCtx, lease); err != nil &&
			!errors.Is(err, memorystore.ErrMaintenanceLeaseLost) {
			report("maintenance_owner_release", err)
		}
		releaseCancel()
		if ctx.Err() == nil && !waitMaintenanceRetry(ctx, retryInterval) {
			return
		}
	}
}

func waitMaintenanceRetry(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func runMaintenanceOwned(ctx context.Context, opts MaintenanceOptions) {
	cfg := deriveMaintenanceSchedule(opts)
	rt := opts.Runtime
	if rt == nil {
		return
	}
	report := func(stage string, err error) {
		if err != nil && opts.OnError != nil {
			opts.OnError(stage, err)
		}
	}
	runStage := func(stage string, fn func(context.Context) error) bool {
		if err := rt.Store.ValidateMaintenanceLeaseContext(ctx); err != nil {
			report(stage+"_lease", err)
			return false
		}
		stageCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := executeMaintenanceStage(stageCtx, fn)
		cancel()
		report(stage, err)
		if err := rt.Store.ValidateMaintenanceLeaseContext(ctx); err != nil {
			report(stage+"_lease", err)
			return false
		}
		return ctx.Err() == nil
	}
	if cfg.captureEnabled {
		if !runStage("capture_materialization", func(stageCtx context.Context) error {
			_, err := rt.MaterializeTurnCaptureReceipts(stageCtx, rt.Config.TurnDigestMaxDigestsPerRun)
			return err
		}) {
			return
		}
	}
	if cfg.captureStartupWorker {
		if !runStage("capture_extraction_recovery", func(stageCtx context.Context) error {
			_, err := rt.RunPendingTurnCaptureReceipts(stageCtx, memoryruntime.TriggerStartup)
			return err
		}) {
			return
		}
	}
	if cfg.startupRecovery {
		// This stage is deliberately legacy-only. Receipt-backed work was
		// materialized and claimed above; RecoverPendingPhase1 skips any session
		// that has a canonical capture receipt.
		if !runStage("legacy_phase1_recovery", func(stageCtx context.Context) error {
			_, err := rt.RecoverPendingPhase1(stageCtx, memoryruntime.TriggerStartup)
			return err
		}) {
			return
		}
	}
	if cfg.freshnessEnabled {
		if !runStage("freshness", func(stageCtx context.Context) error {
			_, err := rt.SweepFreshness(stageCtx)
			return err
		}) {
			return
		}
	}
	digestEnabled := cfg.idleEnabled || cfg.captureEnabled
	maintenanceEnabled := cfg.consolidationEnabled || cfg.retentionEnabled || cfg.lifecycleEnabled || cfg.freshnessEnabled
	lastFreshnessSweep := time.Now()
	var digestTicker, maintenanceTicker *time.Ticker
	var digestTick, maintenanceTick <-chan time.Time
	if digestEnabled {
		digestTicker = time.NewTicker(time.Duration(cfg.idleExtractMinutes) * time.Minute)
		digestTick = digestTicker.C
		defer digestTicker.Stop()
	}
	if maintenanceEnabled {
		maintenanceTicker = time.NewTicker(30 * time.Minute)
		maintenanceTick = maintenanceTicker.C
		defer maintenanceTicker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-digestTick:
			if cfg.captureEnabled {
				if !runStage("capture_materialization", func(stageCtx context.Context) error {
					_, err := rt.MaterializeTurnCaptureReceipts(stageCtx, rt.Config.TurnDigestMaxDigestsPerRun)
					return err
				}) {
					return
				}
			}
			if cfg.idleEnabled {
				if !runStage("capture_extraction", func(stageCtx context.Context) error {
					_, err := rt.RunPendingTurnCaptureReceipts(stageCtx, memoryruntime.TriggerIdle)
					return err
				}) {
					return
				}
			}
			if cfg.turnDigestEnabled {
				if !runStage("turn_digest_retention", func(stageCtx context.Context) error {
					_, err := rt.PruneTurnDigests(stageCtx)
					return err
				}) {
					return
				}
				if !runStage("turn_window_retention", func(stageCtx context.Context) error {
					_, err := rt.PruneTurnWindows(stageCtx)
					return err
				}) {
					return
				}
			}
		case <-maintenanceTick:
			if cfg.consolidationEnabled && opts.Host != nil {
				if !runStage("consolidation", opts.Host.RunConsolidation) {
					return
				}
			}
			if cfg.lifecycleEnabled {
				if !runStage("lifecycle", func(stageCtx context.Context) error {
					_, err := rt.Store.SweepLifecycle(stageCtx, time.Now().UTC())
					return err
				}) {
					return
				}
			}
			if cfg.freshnessEnabled && time.Since(lastFreshnessSweep) >= cfg.freshnessInterval {
				if !runStage("freshness", func(stageCtx context.Context) error {
					_, err := rt.SweepFreshness(stageCtx)
					return err
				}) {
					return
				}
				lastFreshnessSweep = time.Now()
			}
			if cfg.retentionEnabled {
				if !runStage("diagnostics_retention", func(stageCtx context.Context) error {
					_, err := rt.Store.PruneDiagnostics(stageCtx, time.Now().UTC())
					return err
				}) {
					return
				}
			}
		}
	}
}

func executeMaintenanceStage(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("maintenance stage panic: %v", recovered)
		}
	}()
	return fn(ctx)
}
