package usageanalytics

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultRetentionDays     = 90
	defaultRetentionInterval = 6 * time.Hour
	defaultRetentionBatch    = 2000
	defaultRetentionStartup  = 30 * time.Second
)

// RetentionOptions controls the asynchronous raw-detail cleanup loop.
type RetentionOptions struct {
	Enabled    bool
	Days       int
	Interval   time.Duration
	BatchSize  int
	StartDelay time.Duration
}

// RetentionStatus is the latest observable result of automatic cleanup.
type RetentionStatus struct {
	Enabled   bool       `json:"enabled"`
	Days      int        `json:"days"`
	Interval  string     `json:"interval,omitempty"`
	BatchSize int        `json:"batch_size,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	Deleted   PruneStats `json:"deleted"`
	Error     string     `json:"error,omitempty"`
}

func normalizeRetentionOptions(opts RetentionOptions) RetentionOptions {
	if opts.Days <= 0 {
		opts.Days = defaultRetentionDays
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultRetentionInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultRetentionBatch
	}
	if opts.StartDelay < 0 {
		opts.StartDelay = 0
	}
	return opts
}

func (s *Service) startRetentionWorker(opts RetentionOptions) {
	if s == nil || s.store == nil || s.store.ReadOnly() {
		return
	}
	opts = normalizeRetentionOptions(opts)
	if !opts.Enabled {
		s.setRetentionStatus(RetentionStatus{
			Enabled:   false,
			Days:      opts.Days,
			Interval:  opts.Interval.String(),
			BatchSize: opts.BatchSize,
		})
		return
	}
	if opts.StartDelay == 0 {
		opts.StartDelay = defaultRetentionStartup
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.maintenanceCancel = cancel
	s.maintenanceDone = done
	s.setRetentionStatus(RetentionStatus{
		Enabled:   true,
		Days:      opts.Days,
		Interval:  opts.Interval.String(),
		BatchSize: opts.BatchSize,
	})

	go func() {
		defer close(done)
		timer := time.NewTimer(opts.StartDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runRetentionSweep(ctx, opts)
		}
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runRetentionSweep(ctx, opts)
			}
		}
	}()
}

func (s *Service) runRetentionSweep(ctx context.Context, opts RetentionOptions) {
	if s == nil || s.store == nil {
		return
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	cutoff := startedAt.Add(-time.Duration(opts.Days) * 24 * time.Hour)
	stats, err := s.store.PruneDetailBefore(ctx, PruneOptions{
		Cutoff:    cutoff,
		BatchSize: opts.BatchSize,
	})
	status := RetentionStatus{
		Enabled:   true,
		Days:      opts.Days,
		Interval:  opts.Interval.String(),
		BatchSize: opts.BatchSize,
		LastRunAt: &startedAt,
		Deleted:   stats,
	}
	if err != nil {
		if ctx.Err() == nil {
			status.Error = err.Error()
			degradeWarn("分析库自动清理失败（保留 %d 天）：%v", opts.Days, err)
		}
	}
	s.setRetentionStatus(status)
}

func (s *Service) setRetentionStatus(status RetentionStatus) {
	if s == nil {
		return
	}
	s.retentionMu.Lock()
	s.retentionStatus = status
	s.retentionMu.Unlock()
}

// RetentionStatus returns the effective policy and latest sweep result.
func (s *Service) RetentionStatus() RetentionStatus {
	if s == nil {
		return RetentionStatus{}
	}
	s.retentionMu.RLock()
	defer s.retentionMu.RUnlock()
	return s.retentionStatus
}

// PruneNow executes one retention sweep synchronously (maintenance/tests).
func (s *Service) PruneNow(ctx context.Context, days int) (PruneStats, error) {
	if s == nil || s.store == nil {
		return PruneStats{}, fmt.Errorf("usage analytics service is unavailable")
	}
	if days <= 0 {
		days = defaultRetentionDays
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	return s.store.PruneDetailBefore(ctx, PruneOptions{
		Cutoff:    now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
		BatchSize: defaultRetentionBatch,
	})
}
