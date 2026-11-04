package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRunRetentionSweepUpdatesObservableStatus(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour).UnixNano()
	current := now.Add(-time.Hour).UnixNano()
	if _, err := store.db.Exec(`INSERT INTO usage_sessions (session_id, status) VALUES ('s-retention', 'completed')`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO usage_requests
		(llm_request_id, session_id, status, success, started_at_unix_nano)
		VALUES ('old', 's-retention', 'success', 1, ?), ('new', 's-retention', 'success', 1, ?)`,
		old, current); err != nil {
		t.Fatalf("seed requests: %v", err)
	}

	service := &Service{store: store, now: func() time.Time { return now }}
	opts := normalizeRetentionOptions(RetentionOptions{
		Enabled:   true,
		Days:      30,
		Interval:  time.Hour,
		BatchSize: 100,
	})
	service.runRetentionSweep(context.Background(), opts)

	status := service.RetentionStatus()
	if !status.Enabled || status.Days != 30 || status.Interval != "1h0m0s" || status.BatchSize != 100 {
		t.Fatalf("retention policy status = %+v", status)
	}
	if status.LastRunAt == nil || !status.LastRunAt.Equal(now) {
		t.Fatalf("retention last run = %v, want %v", status.LastRunAt, now)
	}
	if status.Deleted.Requests != 1 || !status.Deleted.StatsRebuilt {
		t.Fatalf("retention delete stats = %+v", status.Deleted)
	}
	if status.Error != "" {
		t.Fatalf("retention error = %q", status.Error)
	}
}
