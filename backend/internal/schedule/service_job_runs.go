package schedule

import "strings"

// ClearJobRuns removes the run history for one owned schedule without
// deleting or changing the schedule itself.
func (s *Service) ClearJobRuns(sessionID, id string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}

	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok, err := s.store.Get(id)
	if err != nil {
		return 0, err
	}
	if !ok || !taskOwnedBy(task, sessionID) {
		return 0, inputError(CodeNotFound, "schedule_not_found")
	}

	sqlStore, isSQL := s.store.(*SQLiteStore)
	if !isSQL {
		return 0, nil
	}
	return sqlStore.ClearJobRuns(id)
}

// ClearAllJobRuns removes execution history for every schedule without
// deleting or changing schedules, delivery history, pending messages, or
// delivery intents. The caller must opt into this operation explicitly at the
// API boundary; an empty task id is never interpreted as "all".
func (s *Service) ClearAllJobRuns() (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sqlStore, isSQL := s.store.(*SQLiteStore)
	if !isSQL {
		return 0, nil
	}
	return sqlStore.ClearAllJobRuns()
}
