package usageanalytics

import (
	"encoding/json"
	"fmt"
	"strings"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// LoadSessionRequests replays terminal request rows from usage_requests for
// cache analytics. cache_requests is a mirror of this table; the local cache
// service can therefore read the canonical analytics store instead of keeping a
// second copy of every request. Rows that are not valid CacheRequestRecord JSON
// are skipped, matching the old mirror's per-row degradation.
func (s *Store) LoadSessionRequests(sessionID string) ([]cacheanalytics.CacheRequestRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("usage analytics store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []cacheanalytics.CacheRequestRecord{}, nil
	}
	rows, ok, err := s.query(`
		SELECT record_json FROM usage_requests
		WHERE session_id = ? OR root_session_id = ?
		ORDER BY started_at_unix_nano ASC, llm_request_id ASC
	`, sessionID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load usage requests for session %s: %w", sessionID, err)
	}
	if !ok {
		return []cacheanalytics.CacheRequestRecord{}, nil
	}
	defer rows.Close()

	records := []cacheanalytics.CacheRequestRecord{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan usage request: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		var record cacheanalytics.CacheRequestRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage requests: %w", err)
	}
	return records, nil
}
