package sessionpurge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

type purgeRunner struct {
	ctx    context.Context
	db     *sql.DB
	tx     *sql.Tx
	dryRun bool
	result *StoreResult
}

func (r *purgeRunner) queryer() rowQueryer {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *purgeRunner) execer() execer {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *purgeRunner) hasTable(table string) (bool, error) {
	return tableExists(r.ctx, r.queryer(), table)
}

func (r *purgeRunner) delete(table string, columns []string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ok, err := r.hasTable(table)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var rows int64
	if r.dryRun {
		rows, err = countRowsByIDs(r.ctx, r.queryer(), table, columns, ids)
	} else {
		rows, err = deleteRowsByIDs(r.ctx, r.execer(), table, columns, ids)
	}
	if err != nil {
		return err
	}
	r.result.Tables = append(r.result.Tables, TableResult{Table: table, Rows: rows})
	return nil
}

func (r *purgeRunner) deleteByQuery(table, detail, query string, args ...interface{}) error {
	ok, err := r.hasTable(table)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var rows int64
	if r.dryRun {
		rows, err = countWithQuery(r.ctx, r.queryer(), query, args...)
	} else {
		rows, err = execAffected(r.ctx, r.execer(), query, args...)
	}
	if err != nil {
		return err
	}
	r.result.Tables = append(r.result.Tables, TableResult{Table: table, Rows: rows, Detail: detail})
	return nil
}

func (r *purgeRunner) queryIDs(table, idColumn string, filterColumns []string, ids []string) ([]string, error) {
	ok, err := r.hasTable(table)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return queryIDsByColumns(r.ctx, r.queryer(), table, idColumn, filterColumns, ids)
}

func countWithQuery(ctx context.Context, q rowQueryer, query string, args ...interface{}) (int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, rows.Err()
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, rows.Err()
}

func execAffected(ctx context.Context, e execer, query string, args ...interface{}) (int64, error) {
	result, err := e.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

func withStore(
	ctx context.Context,
	path string,
	opts Options,
	name string,
	fn func(*purgeRunner) error,
	afterCommit func() error,
) (StoreResult, error) {
	result := StoreResult{Name: name, Path: path}
	if strings.TrimSpace(path) == "" {
		result.Status = "absent"
		return result, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			result.Status = "absent"
			return result, nil
		}
		result.Status = "failed"
		result.Error = err.Error()
		return result, err
	}
	db, err := openRuntimeDB(path, opts.DryRun)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, err
	}
	defer func() { _ = db.Close() }()

	var tx *sql.Tx
	if !opts.DryRun {
		tx, err = db.BeginTx(ctx, sqliteutil.WriteTxOptions)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result, err
		}
	}
	runner := &purgeRunner{ctx: ctx, db: db, tx: tx, dryRun: opts.DryRun, result: &result}
	if err := fn(runner); err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		result.Status = "failed"
		result.Error = err.Error()
		return result, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result, err
		}
	}
	if afterCommit != nil {
		if err := afterCommit(); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result, err
		}
	}
	if opts.DryRun {
		result.Status = "planned"
	} else {
		result.Status = "pruned"
	}
	return result, nil
}

func purgeHistoryStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	var artifactDirs []string
	return withStore(ctx, path, opts, "history", func(r *purgeRunner) error {
		if err := r.delete("session_messages", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("session_prompt_messages", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("sessions", []string{"id"}, ids); err != nil {
			return err
		}
		root := strings.TrimSpace(opts.ArtifactSessionRoot)
		if root == "" {
			root = filepath.Join(filepath.Dir(path), "session-artifacts")
		}
		for _, sessionID := range ids {
			target := filepath.Join(root, sessionID)
			if !pathWithin(root, target) {
				continue
			}
			if _, err := os.Stat(target); err == nil {
				artifactDirs = append(artifactDirs, target)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if opts.DryRun {
			r.result.ArtifactDirs = int64(len(artifactDirs))
		}
		return nil
	}, func() error {
		if opts.DryRun {
			return nil
		}
		for _, dir := range artifactDirs {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove session artifacts %s: %w", dir, err)
			}
		}
		return nil
	})
}

func purgeRuntimeStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "runtime", func(r *purgeRunner) error {
		tables := []struct {
			name    string
			columns []string
		}{
			{"session_events", []string{"session_id"}},
			{"session_tool_receipts", []string{"session_id"}},
			{"cache_requests", []string{"session_id"}},
			{"session_runtime_state", []string{"session_id"}},
			{"agent_control_mailbox_records", []string{"session_id"}},
			{"session_actor_leases", []string{"session_id"}},
		}
		for _, table := range tables {
			if err := r.delete(table.name, table.columns, ids); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

func purgeAnalyticsStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "analytics", func(r *purgeRunner) error {
		tables := []struct {
			name    string
			columns []string
		}{
			{"usage_requests", []string{"session_id"}},
			{"usage_sessions", []string{"session_id"}},
			{"usage_turns", []string{"session_id"}},
			{"usage_tool_calls", []string{"session_id"}},
			{"usage_session_turn_keys", []string{"session_id"}},
			{"usage_lsp_requests", []string{"session_id"}},
			{"usage_lsp_first_publish", []string{"session_id"}},
			{"usage_render_fence_drops", []string{"session_id"}},
			{"analytics_legacy_llm_requests", []string{"session_id"}},
			{"analytics_legacy_turns", []string{"session_id"}},
			{"analytics_legacy_sessions", []string{"session_id"}},
			{"usage_routes", []string{"session_id", "child_session_id"}},
			{"usage_subagents", []string{"child_session_id"}},
		}
		for _, table := range tables {
			if err := r.delete(table.name, table.columns, ids); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

func purgeArtifactsStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "artifacts", func(r *purgeRunner) error {
		checkpointIDs, err := r.queryIDs("checkpoints", "id", []string{"session_id"}, ids)
		if err != nil {
			return err
		}
		blobIDs, err := queryArtifactBlobIDs(r, ids)
		if err != nil {
			return err
		}
		if err := r.delete("artifacts", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("memory_entries", []string{"session_id"}, ids); err != nil {
			return err
		}
		if len(checkpointIDs) > 0 {
			if err := r.delete("checkpoint_files", []string{"checkpoint_id"}, checkpointIDs); err != nil {
				return err
			}
		}
		if err := r.delete("checkpoints", []string{"id"}, checkpointIDs); err != nil {
			return err
		}
		if len(blobIDs) > 0 {
			if err := deleteUnreferencedBlobs(r, blobIDs, checkpointIDs); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

func queryArtifactBlobIDs(r *purgeRunner, ids []string) ([]string, error) {
	if ok, err := r.hasTable("checkpoint_files"); err != nil || !ok {
		return nil, err
	}
	if ok, err := r.hasTable("checkpoints"); err != nil || !ok {
		return nil, err
	}
	seen := make(map[string]struct{})
	var result []string
	for start := 0; start < len(ids); start += deleteChunkSize {
		end := start + deleteChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		predicate, args := idPredicate([]string{"session_id"}, ids[start:end])
		query := fmt.Sprintf(`
			SELECT DISTINCT before_blob_id FROM checkpoint_files
			WHERE checkpoint_id IN (SELECT id FROM checkpoints WHERE %s)
			  AND before_blob_id IS NOT NULL AND before_blob_id <> ''
			UNION
			SELECT DISTINCT after_blob_id FROM checkpoint_files
			WHERE checkpoint_id IN (SELECT id FROM checkpoints WHERE %s)
			  AND after_blob_id IS NOT NULL AND after_blob_id <> ''
		`, predicate, predicate)
		queryArgs := append(append([]interface{}{}, args...), args...)
		values, err := queryStringIDs(r.ctx, r.queryer(), query, queryArgs...)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func deleteUnreferencedBlobs(r *purgeRunner, blobIDs []string, checkpointIDs []string) error {
	if len(blobIDs) == 0 {
		return nil
	}
	ok, err := r.hasTable("blobs")
	if err != nil || !ok {
		return err
	}
	var total int64
	candidateCheckpoints := make(map[string]struct{}, len(checkpointIDs))
	for _, checkpointID := range checkpointIDs {
		candidateCheckpoints[checkpointID] = struct{}{}
	}
	for start := 0; start < len(blobIDs); start += deleteChunkSize {
		end := start + deleteChunkSize
		if end > len(blobIDs) {
			end = len(blobIDs)
		}
		chunk := blobIDs[start:end]
		predicate, args := idPredicate([]string{"id"}, chunk)
		if r.dryRun {
			refPredicate, refArgs := idPredicate([]string{"before_blob_id", "after_blob_id"}, chunk)
			rows, err := r.queryer().QueryContext(r.ctx, fmt.Sprintf(`
				SELECT before_blob_id, after_blob_id, checkpoint_id
				FROM checkpoint_files
				WHERE %s
			`, refPredicate), refArgs...)
			if err != nil {
				return err
			}
			chunkSet := make(map[string]struct{}, len(chunk))
			for _, id := range chunk {
				chunkSet[id] = struct{}{}
			}
			blocked := make(map[string]struct{})
			for rows.Next() {
				var before, after, checkpointID sql.NullString
				if err := rows.Scan(&before, &after, &checkpointID); err != nil {
					_ = rows.Close()
					return err
				}
				if _, candidateCheckpoint := candidateCheckpoints[checkpointID.String]; candidateCheckpoint {
					continue
				}
				for _, value := range []string{before.String, after.String} {
					if _, inChunk := chunkSet[value]; inChunk {
						blocked[value] = struct{}{}
					}
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, id := range chunk {
				if _, isBlocked := blocked[id]; !isBlocked {
					total++
				}
			}
			continue
		}
		affected, err := execAffected(r.ctx, r.execer(), fmt.Sprintf(`
			DELETE FROM blobs
			WHERE %s
			  AND NOT EXISTS (
				SELECT 1 FROM checkpoint_files cf
				WHERE cf.before_blob_id = blobs.id OR cf.after_blob_id = blobs.id
			  )
		`, predicate), args...)
		if err != nil {
			return err
		}
		total += affected
	}
	r.result.Tables = append(r.result.Tables, TableResult{Table: "blobs", Rows: total, Detail: "unreferenced only"})
	return nil
}

func purgeAgentControlStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "agent-control", func(r *purgeRunner) error {
		agentIDs, err := r.queryIDs("agent_control_agents", "agent_id", []string{"session_id"}, ids)
		if err != nil {
			return err
		}
		if len(agentIDs) > 0 {
			if err := r.delete("agent_control_agent_wake_events", []string{"agent_id"}, agentIDs); err != nil {
				return err
			}
		}
		if err := r.delete("agent_control_agent_wake_events", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("agent_control_agents", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("agent_control_global_mailbox_records", []string{"session_id"}, ids); err != nil {
			return err
		}
		return nil
	}, nil)
}

func purgeBackgroundStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "background", func(r *purgeRunner) error {
		jobIDs, err := r.queryIDs("background_jobs", "id", []string{"session_id"}, ids)
		if err != nil {
			return err
		}
		if len(jobIDs) > 0 {
			if err := r.delete("background_job_events", []string{"job_id"}, jobIDs); err != nil {
				return err
			}
		}
		if err := r.delete("background_jobs", []string{"id"}, jobIDs); err != nil {
			return err
		}
		return nil
	}, nil)
}

func purgeScheduleStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "schedule", func(r *purgeRunner) error {
		taskIDs, err := r.queryIDs("schedule_tasks", "id", []string{"session_id", "created_by_session_id"}, ids)
		if err != nil {
			return err
		}
		if len(taskIDs) > 0 {
			if err := r.delete("schedule_delivery_history", []string{"schedule_id"}, taskIDs); err != nil {
				return err
			}
			if err := r.delete("schedule_delivery_intents", []string{"schedule_id"}, taskIDs); err != nil {
				return err
			}
			if err := r.delete("schedule_pending_messages", []string{"schedule_id"}, taskIDs); err != nil {
				return err
			}
			if err := r.delete("schedule_job_runs", []string{"schedule_id"}, taskIDs); err != nil {
				return err
			}
		}
		if err := r.delete("schedule_pending_messages", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("schedule_job_runs", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("schedule_tasks", []string{"id"}, taskIDs); err != nil {
			return err
		}
		return nil
	}, nil)
}

func purgeTeamStore(ctx context.Context, path string, ids []string, opts Options) (StoreResult, error) {
	return withStore(ctx, path, opts, "team", func(r *purgeRunner) error {
		teamIDs, err := r.queryIDs("teams", "id", []string{"lead_session_id"}, ids)
		if err != nil {
			return err
		}
		if len(teamIDs) > 0 {
			for _, table := range []string{
				"team_events",
				"team_task_signals",
				"agent_control_task_graph_events",
				"agent_control_task_dependencies",
				"team_path_claims",
				"team_mailbox_receipts",
				"agent_control_task_records",
			} {
				if err := r.delete(table, []string{"team_id"}, teamIDs); err != nil {
					return err
				}
			}
			if err := r.delete("agent_control_wake_events", []string{"team_id"}, teamIDs); err != nil {
				return err
			}
		}
		if err := r.delete("teammates", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("agent_control_task_records", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("agent_control_wake_events", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("agent_control_mailbox_records", []string{"session_id"}, ids); err != nil {
			return err
		}
		if err := r.delete("teams", []string{"id"}, teamIDs); err != nil {
			return err
		}
		return nil
	}, nil)
}
