package schedule

import "fmt"

// ClearJobRuns removes only the execution history for one schedule. The
// schedule, delivery history, pending messages, and delivery intents remain
// untouched.
func (s *SQLiteStore) ClearJobRuns(scheduleID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("schedule: store is not open")
	}
	result, err := s.db.Exec(
		`DELETE FROM schedule_job_runs WHERE schedule_id = ?`,
		scheduleID,
	)
	if err != nil {
		return 0, fmt.Errorf("clear job runs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("clear job runs: %w", err)
	}
	return deleted, nil
}

// ClearAllJobRuns removes every execution-history row, including rows whose
// schedule was already deleted. It does not change schedules or delivery state.
func (s *SQLiteStore) ClearAllJobRuns() (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("schedule: store is not open")
	}
	result, err := s.db.Exec(`DELETE FROM schedule_job_runs`)
	if err != nil {
		return 0, fmt.Errorf("clear all job runs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("clear all job runs: %w", err)
	}
	return deleted, nil
}
