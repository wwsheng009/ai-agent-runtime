package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// index_jobs 的读写（06 §4 Phase 1 交付 5；DDL 事实源见 supplement/15 §15.3）。
//
// 不变量（ADR-0007 §4.3 / 04 §4.1 Incremental）：
//   - 任何索引变更路径都必须**先写 index_jobs 再执行**，不得绕过本表直接写 symbols；
//   - 本表由 owner 独占写入；reader 调用写方法会拿到 ErrReadOnlyStore（硬失败，
//     不静默丢弃）；LatestIndexJob 是纯读，reader 可用。

// StartIndexJob 落一条运行记录并返回其 id；status 缺省为 running，kind 缺省为 light。
func (s *sqliteStore) StartIndexJob(ctx context.Context, job IndexJob) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("knowledge: start index job: store is not open")
	}
	if strings.TrimSpace(job.WorkspaceID) == "" {
		return "", errors.New("knowledge: start index job: workspace id is required")
	}
	if strings.TrimSpace(job.ID) == "" {
		job.ID = "job_" + digest(job.WorkspaceID, string(job.Kind), strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	if strings.TrimSpace(job.Kind) == "" {
		job.Kind = IndexJobKindLight
	}
	if strings.TrimSpace(job.Status) == "" {
		job.Status = IndexJobStatusRunning
	}
	if job.StartedAt == 0 {
		job.StartedAt = time.Now().UnixMilli()
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO index_jobs (
				id, workspace_id, kind, status, files_total, files_done, error, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, NULL)`,
			job.ID, job.WorkspaceID, job.Kind, job.Status, job.FilesTotal, job.FilesDone, job.StartedAt)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("knowledge: start index job: %w", err)
	}
	return job.ID, nil
}

// UpdateIndexJob 刷新运行进度；用于长索引的中途上报（尽力而为）。
func (s *sqliteStore) UpdateIndexJob(ctx context.Context, jobID string, filesDone int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge: update index job: store is not open")
	}
	if strings.TrimSpace(jobID) == "" {
		return errors.New("knowledge: update index job: job id is required")
	}
	return s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE index_jobs SET files_done = ? WHERE id = ?`, filesDone, jobID)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err == nil && affected == 0 {
			return fmt.Errorf("knowledge: update index job: %s not found", jobID)
		}
		return nil
	})
}

// FinishIndexJob 终结一条运行记录：写 status / 进度 / 错误摘要 / finished_at。
//
// errMsg 会被截断到 maxIndexJobErrorBytes——状态面只需要能解释失败的摘要，
// 不需要完整堆栈（后者只会把行撑大并泄漏路径细节）。
func (s *sqliteStore) FinishIndexJob(ctx context.Context, jobID, status string, filesTotal, filesDone int, errMsg string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge: finish index job: store is not open")
	}
	if strings.TrimSpace(jobID) == "" {
		return errors.New("knowledge: finish index job: job id is required")
	}
	if strings.TrimSpace(status) == "" {
		status = IndexJobStatusDone
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE index_jobs
			SET status = ?, files_total = ?, files_done = ?, error = ?, finished_at = ?
			WHERE id = ?`,
			status, filesTotal, filesDone, nullableText(truncateError(errMsg)), time.Now().UnixMilli(), jobID)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err == nil && affected == 0 {
			return fmt.Errorf("knowledge: finish index job: %s not found", jobID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("knowledge: finish index job: %w", err)
	}
	return nil
}

// LatestIndexJob 返回该 workspace 最近一次运行记录（按 started_at 倒序）；
// nil 表示从未跑过。纯读，reader 可用。
func (s *sqliteStore) LatestIndexJob(ctx context.Context, workspaceID string) (*IndexJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("knowledge: read latest index job: store is not open")
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, kind, status, files_total, files_done,
		       COALESCE(error, ''), COALESCE(started_at, 0), COALESCE(finished_at, 0)
		FROM index_jobs
		WHERE workspace_id = ?
		ORDER BY COALESCE(started_at, 0) DESC, rowid DESC
		LIMIT 1`, workspaceID)
	var job IndexJob
	if err := row.Scan(&job.ID, &job.WorkspaceID, &job.Kind, &job.Status,
		&job.FilesTotal, &job.FilesDone, &job.Error, &job.StartedAt, &job.FinishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("knowledge: read latest index job: %w", err)
	}
	if job.StartedAt > 0 && job.FinishedAt > 0 {
		job.DurationMS = job.FinishedAt - job.StartedAt
	}
	return &job, nil
}

// nullableText 把空串写成 NULL：index_jobs.error 的 NULL 语义是"无错误"，
// 空串与 NULL 混用会让"有错误但摘要为空"变得不可区分。
func nullableText(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// truncateError 截断错误摘要并标注截断。
func truncateError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxIndexJobErrorBytes {
		return message
	}
	return message[:maxIndexJobErrorBytes] + "…(truncated)"
}
