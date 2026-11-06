package sessionpurge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const runtimeBusyProtectionWindow = 24 * time.Hour

type historySession struct {
	ID        string
	UpdatedAt time.Time
	HasTime   bool
	ExpiresAt time.Time
}

func loadHistorySessions(ctx context.Context, path string) ([]historySession, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	ok, err := tableExists(ctx, db, "sessions")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id, updated_at, COALESCE(expires_at, '') FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("load history sessions: %w", err)
	}
	defer rows.Close()
	var sessions []historySession
	for rows.Next() {
		var session historySession
		var updatedRaw, expiresRaw string
		if err := rows.Scan(&session.ID, &updatedRaw, &expiresRaw); err != nil {
			return nil, err
		}
		session.ID = strings.TrimSpace(session.ID)
		if session.ID == "" {
			continue
		}
		if ts, ok := parseRuntimeTime(updatedRaw); ok {
			session.UpdatedAt = ts
			session.HasTime = true
		}
		if ts, ok := parseRuntimeTime(expiresRaw); ok {
			session.ExpiresAt = ts
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func loadLastSeen(ctx context.Context, paths Paths, opts Options) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	merge := func(values map[string]time.Time) {
		for sessionID, ts := range values {
			if previous, exists := result[sessionID]; !exists || ts.After(previous) {
				result[sessionID] = ts
			}
		}
	}

	runtimeSeen, err := loadRuntimeLastSeen(ctx, paths.Runtime)
	if err != nil {
		return nil, err
	}
	merge(runtimeSeen)

	cellSeen, err := loadMergedUnixTimes(ctx, paths.Cells, []string{
		`SELECT session_id, MAX(created_at) FROM session_cells GROUP BY session_id`,
	})
	if err != nil {
		return nil, err
	}
	merge(cellSeen)

	usageSeen, err := loadUsageLastSeen(ctx, paths.UsageAnalytics)
	if err != nil {
		return nil, err
	}
	merge(usageSeen)

	artifactSeen, err := loadArtifactLastSeen(ctx, paths.Artifacts)
	if err != nil {
		return nil, err
	}
	merge(artifactSeen)

	agentSeen, err := loadAgentLastSeen(ctx, paths.AgentControl)
	if err != nil {
		return nil, err
	}
	merge(agentSeen)

	backgroundSeen, err := loadBackgroundLastSeen(ctx, paths.Background)
	if err != nil {
		return nil, err
	}
	merge(backgroundSeen)

	scheduleSeen, err := loadScheduleLastSeen(ctx, paths.Schedule)
	if err != nil {
		return nil, err
	}
	merge(scheduleSeen)

	if opts.IncludeTeam {
		teamSeen, err := loadTeamLastSeen(ctx, paths.Team)
		if err != nil {
			return nil, err
		}
		merge(teamSeen)
	}
	return result, nil
}

func loadRuntimeLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	queries := []string{
		`SELECT session_id, MAX(updated_at) FROM session_runtime_state GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM session_events GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM session_tool_receipts GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM cache_requests GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM agent_control_mailbox_records WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
		`SELECT session_id, MAX(heartbeat_at) FROM session_actor_leases GROUP BY session_id`,
	}
	return loadMergedStringTimes(ctx, path, queries)
}

func loadUsageLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedUnixTimes(ctx, path, []string{
		`SELECT session_id, MAX(COALESCE(NULLIF(updated_at_unix_nano, 0), NULLIF(ended_at_unix_nano, 0), NULLIF(started_at_unix_nano, 0), 0)) FROM usage_sessions GROUP BY session_id`,
		`SELECT session_id, MAX(started_at_unix_nano) FROM usage_requests GROUP BY session_id`,
		`SELECT session_id, MAX(ended_at_unix_nano) FROM usage_turns GROUP BY session_id`,
		`SELECT session_id, MAX(started_at_unix_nano) FROM usage_tool_calls GROUP BY session_id`,
		`SELECT session_id, MAX(started_unix_nano) FROM usage_lsp_requests GROUP BY session_id`,
		`SELECT session_id, MAX(observed_unix_nano) FROM usage_lsp_first_publish GROUP BY session_id`,
		`SELECT session_id, MAX(last_at_unix_nano) FROM usage_render_fence_drops GROUP BY session_id`,
		`SELECT session_id, MAX(recorded_at_unix_nano) FROM usage_routes GROUP BY session_id`,
		`SELECT parent_session_id AS session_id, MAX(started_at_unix_nano) FROM usage_subagents GROUP BY parent_session_id`,
	})
}

func loadArtifactLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedStringTimes(ctx, path, []string{
		`SELECT session_id, MAX(created_at) FROM artifacts GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM checkpoints GROUP BY session_id`,
		`SELECT session_id, MAX(created_at) FROM memory_entries GROUP BY session_id`,
	})
}

func loadAgentLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedStringTimes(ctx, path, []string{
		`SELECT session_id, MAX(updated_at) FROM agent_control_agents GROUP BY session_id`,
	})
}

func loadBackgroundLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedStringTimes(ctx, path, []string{
		`SELECT session_id, MAX(COALESCE(NULLIF(finished_at, ''), NULLIF(started_at, ''), created_at)) FROM background_jobs WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
	})
}

func loadScheduleLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedStringTimes(ctx, path, []string{
		`SELECT session_id, MAX(updated_at) FROM schedule_tasks WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
	})
}

func loadTeamLastSeen(ctx context.Context, path string) (map[string]time.Time, error) {
	return loadMergedStringTimes(ctx, path, []string{
		`SELECT session_id, MAX(updated_at) FROM teammates WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
		`SELECT lead_session_id AS session_id, MAX(updated_at) FROM teams WHERE lead_session_id IS NOT NULL AND lead_session_id <> '' GROUP BY lead_session_id`,
	})
}

func loadMergedStringTimes(ctx context.Context, path string, queries []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	if strings.TrimSpace(path) == "" {
		return result, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	for _, query := range queries {
		ok, err := queryReferencesExistingTable(ctx, db, query)
		if err != nil {
			return nil, fmt.Errorf("check table for query %q: %w", query, err)
		}
		if !ok {
			continue
		}
		values, err := querySessionTimes(ctx, db, query)
		if err != nil {
			return nil, err
		}
		for sessionID, ts := range values {
			if previous, exists := result[sessionID]; !exists || ts.After(previous) {
				result[sessionID] = ts
			}
		}
	}
	return result, nil
}

func loadMergedUnixTimes(ctx context.Context, path string, queries []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	if strings.TrimSpace(path) == "" {
		return result, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	for _, query := range queries {
		ok, err := queryReferencesExistingTable(ctx, db, query)
		if err != nil {
			return nil, fmt.Errorf("check table for query %q: %w", query, err)
		}
		if !ok {
			continue
		}
		values, err := querySessionUnixTimes(ctx, db, query)
		if err != nil {
			return nil, err
		}
		for sessionID, ts := range values {
			if previous, exists := result[sessionID]; !exists || ts.After(previous) {
				result[sessionID] = ts
			}
		}
	}
	return result, nil
}

// queryReferencesExistingTable 从固定查询里取 `FROM <table>` 的表名做存在性门；
// 查询串都是包内常量，不接受外部输入。
func queryReferencesExistingTable(ctx context.Context, db *sql.DB, query string) (bool, error) {
	lower := strings.ToLower(query)
	index := strings.Index(lower, " from ")
	if index < 0 {
		return false, nil
	}
	rest := strings.TrimSpace(query[index+6:])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false, nil
	}
	table := strings.Trim(fields[0], `"`)
	ok, err := tableExists(ctx, db, table)
	if err != nil {
		return false, fmt.Errorf("table %s: %w", table, err)
	}
	if !ok {
		return false, nil
	}
	if _, err := db.ExecContext(ctx, "EXPLAIN "+query, explainArgs(query)...); err != nil {
		if isMissingColumnError(err) {
			return false, nil
		}
		return false, fmt.Errorf("check query %q: %w", query, err)
	}
	return ok, nil
}

func explainArgs(query string) []interface{} {
	args := make([]interface{}, strings.Count(query, "?"))
	for index := range args {
		args[index] = ""
	}
	return args
}

func isMissingColumnError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no such column") || strings.Contains(text, "has no column named")
}

func loadProtection(ctx context.Context, paths Paths, candidates map[string]string, opts Options, cutoff time.Time, timer *purgeTimer) (map[string][]string, error) {
	reasons := make(map[string][]string)
	protect := func(name string, fn func() error) error {
		return timer.stage(name, fn)
	}
	add := func(sessionID, reason string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		if _, isCandidate := candidates[sessionID]; !isCandidate {
			return
		}
		for _, existing := range reasons[sessionID] {
			if existing == reason {
				return
			}
		}
		reasons[sessionID] = append(reasons[sessionID], reason)
	}

	if err := protect("protection_runtime", func() error {
		return protectRuntimeSessions(ctx, paths.Runtime, opts.Now, add)
	}); err != nil {
		return nil, err
	}
	if err := protect("protection_background", func() error {
		return protectBackgroundSessions(ctx, paths.Background, candidates, add)
	}); err != nil {
		return nil, err
	}
	if err := protect("protection_agent_control", func() error {
		return protectAgentControlSessions(ctx, paths.AgentControl, add)
	}); err != nil {
		return nil, err
	}
	if err := protect("protection_schedule", func() error {
		return protectScheduleSessions(ctx, paths.Schedule, opts.Now, add)
	}); err != nil {
		return nil, err
	}
	if opts.IncludeTeam {
		if err := protect("protection_team", func() error {
			return protectTeamSessions(ctx, paths.Team, opts.Now, add)
		}); err != nil {
			return nil, err
		}
	}
	if opts.FastCandidates {
		if err := protect("protection_recent", func() error {
			return protectRecentActivity(ctx, paths, candidates, opts, cutoff, add)
		}); err != nil {
			return nil, err
		}
	}
	return reasons, nil
}

func protectRecentActivity(ctx context.Context, paths Paths, candidates map[string]string, opts Options, cutoff time.Time, add func(string, string)) error {
	cutoffText := cutoff.UTC().Format(time.RFC3339Nano)
	cutoffNano := cutoff.UnixNano()
	if err := protectRecentSummaryIDs(ctx, paths, cutoff, add); err != nil {
		return err
	}
	candidateIDs := make([]string, 0, len(candidates))
	for sessionID := range candidates {
		candidateIDs = append(candidateIDs, sessionID)
	}
	stores := []struct {
		path    string
		queries []string
		unix    bool
	}{
		{paths.Runtime, []string{
			`SELECT session_id FROM session_events WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM session_tool_receipts WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM cache_requests WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM agent_control_mailbox_records WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
		}, false},
		{paths.UsageAnalytics, []string{
			`SELECT session_id FROM usage_requests WHERE started_at_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_turns WHERE ended_at_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_tool_calls WHERE started_at_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_lsp_requests WHERE started_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_lsp_first_publish WHERE observed_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_render_fence_drops WHERE last_at_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM usage_routes WHERE recorded_at_unix_nano >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT child_session_id FROM usage_routes WHERE recorded_at_unix_nano >= ? AND child_session_id IN (SELECT value FROM json_each(?))`,
			`SELECT parent_session_id FROM usage_subagents WHERE COALESCE(NULLIF(completed_at_unix_nano, 0), started_at_unix_nano, 0) >= ? AND parent_session_id IN (SELECT value FROM json_each(?))`,
			`SELECT child_session_id FROM usage_subagents WHERE COALESCE(NULLIF(completed_at_unix_nano, 0), started_at_unix_nano, 0) >= ? AND child_session_id IN (SELECT value FROM json_each(?))`,
			`SELECT child_session_id FROM usage_subagents WHERE COALESCE(NULLIF(completed_at_unix_nano, 0), started_at_unix_nano, 0) >= ? AND child_session_id IN (SELECT value FROM json_each(?))`,
		}, true},
		{paths.Artifacts, []string{
			`SELECT session_id FROM artifacts WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM checkpoints WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM memory_entries WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
		}, false},
		{paths.AgentControl, []string{
			`SELECT session_id FROM agent_control_agent_wake_events WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT session_id FROM agent_control_global_mailbox_records WHERE created_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
		}, false},
		{paths.Background, []string{
			`SELECT session_id FROM background_jobs WHERE COALESCE(NULLIF(finished_at, ''), NULLIF(started_at, ''), created_at) >= ? AND session_id IN (SELECT value FROM json_each(?))`,
		}, false},
		{paths.Schedule, []string{
			`SELECT session_id FROM schedule_tasks WHERE updated_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT created_by_session_id FROM schedule_tasks WHERE updated_at >= ? AND created_by_session_id IN (SELECT value FROM json_each(?))`,
		}, false},
	}
	if opts.IncludeTeam {
		stores = append(stores, struct {
			path    string
			queries []string
			unix    bool
		}{paths.Team, []string{
			`SELECT session_id FROM teammates WHERE updated_at >= ? AND session_id IN (SELECT value FROM json_each(?))`,
			`SELECT lead_session_id FROM teams WHERE updated_at >= ? AND lead_session_id IN (SELECT value FROM json_each(?))`,
		}, false})
	}
	for _, store := range stores {
		if err := protectRecentIDsInStore(ctx, store.path, store.queries, store.unix, candidateIDs, cutoffText, cutoffNano, add); err != nil {
			return err
		}
	}
	return nil
}

func protectRecentSummaryIDs(ctx context.Context, paths Paths, cutoff time.Time, add func(string, string)) error {
	stores := []struct {
		path    string
		queries []string
		unix    bool
	}{
		{paths.UsageAnalytics, []string{
			`SELECT session_id, MAX(COALESCE(NULLIF(updated_at_unix_nano, 0), NULLIF(ended_at_unix_nano, 0), NULLIF(started_at_unix_nano, 0), 0)) FROM usage_sessions GROUP BY session_id`,
			`SELECT session_id, MAX(started_at_unix_nano) FROM usage_requests GROUP BY session_id`,
			`SELECT session_id, MAX(ended_at_unix_nano) FROM usage_turns GROUP BY session_id`,
			`SELECT session_id, MAX(started_at_unix_nano) FROM usage_tool_calls GROUP BY session_id`,
			`SELECT session_id, MAX(started_unix_nano) FROM usage_lsp_requests GROUP BY session_id`,
			`SELECT session_id, MAX(observed_unix_nano) FROM usage_lsp_first_publish GROUP BY session_id`,
			`SELECT session_id, MAX(last_at_unix_nano) FROM usage_render_fence_drops GROUP BY session_id`,
			`SELECT session_id, MAX(recorded_at_unix_nano) FROM usage_routes GROUP BY session_id`,
			`SELECT parent_session_id AS session_id, MAX(started_at_unix_nano) FROM usage_subagents GROUP BY parent_session_id`,
		}, true},
		{paths.Runtime, []string{
			`SELECT session_id, MAX(updated_at) FROM session_runtime_state GROUP BY session_id`,
			`SELECT session_id, MAX(heartbeat_at) FROM session_actor_leases GROUP BY session_id`,
		}, false},
		{paths.Artifacts, []string{
			`SELECT session_id, MAX(created_at) FROM artifacts GROUP BY session_id`,
			`SELECT session_id, MAX(created_at) FROM checkpoints GROUP BY session_id`,
			`SELECT session_id, MAX(created_at) FROM memory_entries GROUP BY session_id`,
		}, false},
		{paths.AgentControl, []string{
			`SELECT session_id, MAX(updated_at) FROM agent_control_agents GROUP BY session_id`,
		}, false},
		{paths.Background, []string{
			`SELECT session_id, MAX(COALESCE(NULLIF(finished_at, ''), NULLIF(started_at, ''), created_at)) FROM background_jobs WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
		}, false},
		{paths.Schedule, []string{
			`SELECT session_id, MAX(updated_at) FROM schedule_tasks WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
		}, false},
	}
	if _, err := os.Stat(paths.Team); err == nil {
		stores = append(stores, struct {
			path    string
			queries []string
			unix    bool
		}{paths.Team, []string{
			`SELECT session_id, MAX(updated_at) FROM teammates WHERE session_id IS NOT NULL AND session_id <> '' GROUP BY session_id`,
			`SELECT lead_session_id AS session_id, MAX(updated_at) FROM teams WHERE lead_session_id IS NOT NULL AND lead_session_id <> '' GROUP BY lead_session_id`,
		}, false})
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, store := range stores {
		if err := protectRecentSummaryIDsInStore(ctx, store.path, store.queries, store.unix, cutoff, add); err != nil {
			return err
		}
	}
	return nil
}

func protectRecentSummaryIDsInStore(ctx context.Context, path string, queries []string, unix bool, cutoff time.Time, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	for _, query := range queries {
		ok, err := queryReferencesExistingTable(ctx, db, query)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var values map[string]time.Time
		if unix {
			values, err = querySessionUnixTimes(ctx, db, query)
		} else {
			values, err = querySessionTimes(ctx, db, query)
		}
		if err != nil {
			return err
		}
		for sessionID, seen := range values {
			if !seen.Before(cutoff) {
				add(sessionID, "recent_activity")
			}
		}
	}
	return nil
}

func protectRecentIDsInStore(ctx context.Context, path string, queries []string, unix bool, candidateIDs []string, cutoffText string, cutoffNano int64, add func(string, string)) error {
	if len(candidateIDs) == 0 {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	encodedIDs, err := json.Marshal(candidateIDs)
	if err != nil {
		return fmt.Errorf("encode purge candidates: %w", err)
	}
	for _, query := range queries {
		ok, err := queryReferencesExistingTable(ctx, db, query)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		cutoffValue := interface{}(cutoffText)
		if unix {
			cutoffValue = cutoffNano
		}
		if err := protectRecentIDsWithQuery(ctx, db, query, cutoffValue, string(encodedIDs), add); err != nil {
			return err
		}
	}
	return nil
}

func protectRecentIDsWithQuery(ctx context.Context, db *sql.DB, query string, cutoff interface{}, encodedIDs string, add func(string, string)) error {
	rows, err := db.QueryContext(ctx, query, cutoff, encodedIDs)
	if err != nil {
		if isMissingColumnError(err) {
			return nil
		}
		return fmt.Errorf("recent activity query %q: %w", query, err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID sql.NullString
		if err := rows.Scan(&sessionID); err != nil {
			return err
		}
		add(strings.TrimSpace(sessionID.String), "recent_activity")
	}
	return rows.Err()
}

func protectRuntimeSessions(ctx context.Context, path string, now time.Time, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if ok, _ := tableExists(ctx, db, "session_runtime_state"); ok {
		rows, err := db.QueryContext(ctx, `SELECT session_id, status, updated_at FROM session_runtime_state`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sessionID, status, updatedRaw string
			if err := rows.Scan(&sessionID, &status, &updatedRaw); err != nil {
				_ = rows.Close()
				return err
			}
			switch strings.ToLower(strings.TrimSpace(status)) {
			case "running", "waiting_approval", "waiting_input", "rewinding":
				// 旧库里有大量崩溃/强退遗留的 running 状态。只有最近仍在
				// 活动窗口内的 busy 状态才作为活跃会话保护；否则它们会
				// 永久阻断按龄清理。
				if updatedAt, ok := parseRuntimeTime(updatedRaw); ok && updatedAt.After(now.Add(-runtimeBusyProtectionWindow)) {
					add(sessionID, "runtime_busy_recent")
				}
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if ok, _ := tableExists(ctx, db, "session_actor_leases"); ok {
		rows, err := db.QueryContext(ctx, `SELECT session_id, expires_at FROM session_actor_leases`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sessionID, expiresRaw string
			if err := rows.Scan(&sessionID, &expiresRaw); err != nil {
				_ = rows.Close()
				return err
			}
			if expires, ok := parseRuntimeTime(expiresRaw); ok && expires.After(now) {
				add(sessionID, "runtime_lease")
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func protectBackgroundSessions(ctx context.Context, path string, candidates map[string]string, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if ok, _ := tableExists(ctx, db, "background_jobs"); !ok {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT session_id, status FROM background_jobs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, status string
		if err := rows.Scan(&sessionID, &status); err != nil {
			return err
		}
		if _, isCandidate := candidates[strings.TrimSpace(sessionID)]; !isCandidate {
			continue
		}
		if !isTerminalBackgroundStatus(status) {
			add(sessionID, "background_active")
		}
	}
	return rows.Err()
}

func isTerminalBackgroundStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "timed_out", "interrupted",
		"expired", "abandoned", "orphaned", "skipped":
		return true
	default:
		return false
	}
}

func protectAgentControlSessions(ctx context.Context, path string, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if ok, _ := tableExists(ctx, db, "agent_control_agents"); !ok {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, COALESCE(parent_session_id, ''), COALESCE(root_session_id, '')
		FROM agent_control_agents
		WHERE status = 'active' AND closed_at IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, parentSessionID, rootSessionID string
		if err := rows.Scan(&sessionID, &parentSessionID, &rootSessionID); err != nil {
			return err
		}
		add(sessionID, "agent_active")
		add(parentSessionID, "agent_parent_active")
		add(rootSessionID, "agent_parent_active")
	}
	return rows.Err()
}

func protectScheduleSessions(ctx context.Context, path string, now time.Time, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if ok, _ := tableExists(ctx, db, "schedule_tasks"); !ok {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(session_id, ''), COALESCE(created_by_session_id, ''), status,
		       COALESCE(lease_expires_at, '')
		FROM schedule_tasks
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, createdBy, status, leaseRaw string
		if err := rows.Scan(&sessionID, &createdBy, &status, &leaseRaw); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "active", "paused":
			add(sessionID, "schedule_active")
			add(createdBy, "schedule_created_by_active")
		}
		if lease, ok := parseRuntimeTime(leaseRaw); ok && lease.After(now) {
			add(sessionID, "schedule_lease")
			add(createdBy, "schedule_lease")
		}
	}
	return rows.Err()
}

func protectTeamSessions(ctx context.Context, path string, now time.Time, add func(string, string)) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	db, err := openRuntimeDB(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if ok, _ := tableExists(ctx, db, "teams"); ok {
		rows, err := db.QueryContext(ctx, `SELECT lead_session_id, status FROM teams WHERE lead_session_id IS NOT NULL AND lead_session_id <> ''`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sessionID, status string
			if err := rows.Scan(&sessionID, &status); err != nil {
				_ = rows.Close()
				return err
			}
			if strings.EqualFold(strings.TrimSpace(status), "active") {
				add(sessionID, "team_active")
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if ok, _ := tableExists(ctx, db, "orchestrator_owner_leases"); ok {
		rows, err := db.QueryContext(ctx, `
			SELECT t.lead_session_id, l.lease_until
			FROM orchestrator_owner_leases l
			JOIN teams t ON t.id = l.team_id
			WHERE t.lead_session_id IS NOT NULL AND t.lead_session_id <> ''
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sessionID, leaseRaw string
			if err := rows.Scan(&sessionID, &leaseRaw); err != nil {
				return err
			}
			if lease, ok := parseRuntimeTime(leaseRaw); ok && lease.After(now) {
				add(sessionID, "team_orchestrator_lease")
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if ok, _ := tableExists(ctx, db, "teammates"); ok {
		rows, err := db.QueryContext(ctx, `SELECT session_id, state FROM teammates WHERE session_id IS NOT NULL AND session_id <> ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sessionID, state string
			if err := rows.Scan(&sessionID, &state); err != nil {
				return err
			}
			switch strings.ToLower(strings.TrimSpace(state)) {
			case "", "idle", "closed", "stale", "offline":
			default:
				add(sessionID, "team_teammate_active")
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}
