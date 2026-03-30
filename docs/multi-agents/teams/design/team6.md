继续。下面直接落你上一轮最需要的三块：

1. `sqlite_repo.go` 的关键事务实现骨架
2. migration SQL 草案
3. `SendTeamMessage` 工具骨架

我先修一个上一轮接口里的小漏洞：`blocked` 不应该复用 `FailTask()`。
否则 `replanIfStuck()` 看不出“失败”和“阻塞”的区别。这里建议把 Repo 补成：

```go
BlockTask(ctx context.Context, teamID, taskID string, reason string) error
FailTask(ctx context.Context, teamID, taskID string, reason string, retryable bool) error
```

下面的代码都按这个修正来写。

---

# 1. `internal/team/sqlite_repo.go`

这版只把**最关键事务**写出来。普通 `Get/List/Scan` 按同样模式补齐就行。

有一个实现前提很重要：

**Team 的 claim/lease/path-claim 事务建议用 SQLite 的“立即写事务”语义。**
不然 `CanClaim -> ClaimTask -> AcquirePathClaims` 在高并发下会有竞争窗口。工程上就是确保这类事务一开始就拿写锁，而不是等到第一条写语句才升级。

## 1.1 文件骨架

```go
package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrVersionConflict = errors.New("version conflict")
)

type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type sqliteCore struct {
	q dbtx
}

type SQLiteRepo struct {
	db *sql.DB
	*sqliteCore
}

type txRepo struct {
	tx *sql.Tx
	*sqliteCore
}

func NewSQLiteRepo(db *sql.DB) *SQLiteRepo {
	return &SQLiteRepo{
		db:         db,
		sqliteCore: &sqliteCore{q: db},
	}
}

func (r *SQLiteRepo) WithTx(ctx context.Context, fn func(TxRepo) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tr := &txRepo{
		tx:         tx,
		sqliteCore: &sqliteCore{q: tx},
	}
	if err := fn(tr); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *txRepo) WithTx(ctx context.Context, fn func(TxRepo) error) error {
	return fn(r)
}
```

## 1.2 JSON / 时间 / 扫描 helper

```go
func mustJSON(v any) []byte {
	if v == nil {
		return []byte("null")
	}
	b, _ := json.Marshal(v)
	return b
}

func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

func decodeStringSlice(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	_ = json.Unmarshal(b, &out)
	return out
}

func decodeStringMapAny(b []byte) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
```

## 1.3 Team / Teammate 基础方法

```go
func (c *sqliteCore) CreateTeam(ctx context.Context, t Team) error {
	_, err := c.q.ExecContext(ctx, `
		INSERT INTO teams (
			id, workspace_id, lead_session_id, status, strategy,
			max_teammates, max_writers, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.WorkspaceID, t.LeadSessionID, t.Status, t.Strategy,
		t.MaxTeammates, t.MaxWriters, t.CreatedAt, t.UpdatedAt,
	)
	return err
}

func (c *sqliteCore) GetTeam(ctx context.Context, teamID string) (*Team, error) {
	var t Team
	err := c.q.QueryRowContext(ctx, `
		SELECT id, workspace_id, lead_session_id, status, strategy,
		       max_teammates, max_writers, created_at, updated_at
		FROM teams
		WHERE id=?`, teamID).
		Scan(&t.ID, &t.WorkspaceID, &t.LeadSessionID, &t.Status, &t.Strategy,
			&t.MaxTeammates, &t.MaxWriters, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *sqliteCore) UpdateTeamStatus(ctx context.Context, teamID string, status TeamStatus) error {
	_, err := c.q.ExecContext(ctx, `
		UPDATE teams
		SET status=?, updated_at=?
		WHERE id=?`, status, time.Now(), teamID)
	return err
}

func (c *sqliteCore) UpsertTeammate(ctx context.Context, mate Teammate) error {
	_, err := c.q.ExecContext(ctx, `
		INSERT INTO teammates (
			id, team_id, name, profile, session_id, state,
			last_heartbeat, capabilities_json, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			profile=excluded.profile,
			session_id=excluded.session_id,
			state=excluded.state,
			last_heartbeat=excluded.last_heartbeat,
			capabilities_json=excluded.capabilities_json,
			metadata_json=excluded.metadata_json`,
		mate.ID, mate.TeamID, mate.Name, mate.Profile, mate.SessionID,
		mate.State, mate.LastHeartbeat, mustJSON(mate.Capabilities), mustJSON(mate.Metadata),
	)
	return err
}

func (c *sqliteCore) ListIdleTeammates(ctx context.Context, teamID string) ([]Teammate, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, name, profile, session_id, state,
		       last_heartbeat, capabilities_json, metadata_json
		FROM teammates
		WHERE team_id=? AND state='idle'
		ORDER BY last_heartbeat DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Teammate
	for rows.Next() {
		var m Teammate
		var caps, meta []byte
		if err := rows.Scan(&m.ID, &m.TeamID, &m.Name, &m.Profile, &m.SessionID,
			&m.State, &m.LastHeartbeat, &caps, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(caps, &m.Capabilities)
		_ = json.Unmarshal(meta, &m.Metadata)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (c *sqliteCore) UpdateTeammateState(ctx context.Context, teamID, teammateID string, state TeammateState) error {
	_, err := c.q.ExecContext(ctx, `
		UPDATE teammates
		SET state=?, last_heartbeat=?
		WHERE team_id=? AND id=?`,
		state, time.Now(), teamID, teammateID)
	return err
}

func (c *sqliteCore) HeartbeatTeammate(ctx context.Context, teamID, teammateID string, at time.Time) error {
	_, err := c.q.ExecContext(ctx, `
		UPDATE teammates
		SET last_heartbeat=?
		WHERE team_id=? AND id=?`,
		at, teamID, teammateID)
	return err
}
```

## 1.4 插入 task + dependency

这里有个细节：如果 task 没有依赖，默认直接进 `ready`；有依赖就进 `pending`。

```go
func (r *SQLiteRepo) InsertTasks(ctx context.Context, tasks []Task, deps []TaskDependency) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		depCount := make(map[string]int, len(tasks))
		for _, d := range deps {
			depCount[d.TaskID]++
		}

		core := tx.(*txRepo).sqliteCore
		for _, t := range tasks {
			status := t.Status
			if status == "" {
				if depCount[t.ID] == 0 {
					status = TaskReady
				} else {
					status = TaskPending
				}
			}

			_, err := core.q.ExecContext(ctx, `
				INSERT INTO team_tasks (
					id, team_id, parent_task_id, title, goal, inputs_json,
					status, priority, assignee, lease_until, retry_count,
					read_paths_json, write_paths_json, deliverables_json,
					summary, result_ref, version, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				t.ID, t.TeamID, t.ParentTaskID, t.Title, t.Goal, mustJSON(t.Inputs),
				status, t.Priority, t.Assignee, t.LeaseUntil, t.RetryCount,
				mustJSON(t.ReadPaths), mustJSON(t.WritePaths), mustJSON(t.Deliverables),
				t.Summary, t.ResultRef, max64(t.Version, 1), t.CreatedAt, t.UpdatedAt,
			)
			if err != nil {
				return err
			}
		}

		for _, d := range deps {
			_, err := core.q.ExecContext(ctx, `
				INSERT INTO team_task_dependencies (task_id, depends_on_task_id)
				VALUES (?, ?)`, d.TaskID, d.DependsOnTaskID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}
```

## 1.5 ListReadyTasks / ListRunningTasks / GetTask

```go
func (c *sqliteCore) GetTask(ctx context.Context, teamID, taskID string) (*Task, error) {
	row := c.q.QueryRowContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, inputs_json,
		       status, priority, assignee, lease_until, retry_count,
		       read_paths_json, write_paths_json, deliverables_json,
		       summary, result_ref, version, created_at, updated_at
		FROM team_tasks
		WHERE team_id=? AND id=?`, teamID, taskID)

	var t Task
	var parent, assignee, resultRef sql.NullString
	var leaseUntil sql.NullTime
	var inputs, readPaths, writePaths, deliverables []byte

	err := row.Scan(
		&t.ID, &t.TeamID, &parent, &t.Title, &t.Goal, &inputs,
		&t.Status, &t.Priority, &assignee, &leaseUntil, &t.RetryCount,
		&readPaths, &writePaths, &deliverables,
		&t.Summary, &resultRef, &t.Version, &t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	t.ParentTaskID = nullStringPtr(parent)
	t.Assignee = nullStringPtr(assignee)
	t.LeaseUntil = nullTimePtr(leaseUntil)
	t.ResultRef = nullStringPtr(resultRef)
	_ = json.Unmarshal(inputs, &t.Inputs)
	_ = json.Unmarshal(readPaths, &t.ReadPaths)
	_ = json.Unmarshal(writePaths, &t.WritePaths)
	_ = json.Unmarshal(deliverables, &t.Deliverables)
	return &t, nil
}

func (c *sqliteCore) ListReadyTasks(ctx context.Context, teamID string, limit int) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, inputs_json,
		       status, priority, assignee, lease_until, retry_count,
		       read_paths_json, write_paths_json, deliverables_json,
		       summary, result_ref, version, created_at, updated_at
		FROM team_tasks
		WHERE team_id=? AND status='ready'
		ORDER BY priority DESC, created_at ASC
		LIMIT ?`, teamID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (c *sqliteCore) ListRunningTasks(ctx context.Context, teamID string) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, inputs_json,
		       status, priority, assignee, lease_until, retry_count,
		       read_paths_json, write_paths_json, deliverables_json,
		       summary, result_ref, version, created_at, updated_at
		FROM team_tasks
		WHERE team_id=? AND status='running'
		ORDER BY updated_at ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}
```

`scanTasks()` 跟 `GetTask()` 一样解一遍字段，我就不重复展开了。

## 1.6 ClaimTask

这是真正的第一个高风险事务。

```go
func (c *sqliteCore) ClaimTask(
	ctx context.Context,
	teamID, taskID, assignee string,
	expectedVersion int64,
	leaseUntil time.Time,
) (bool, error) {
	res, err := c.q.ExecContext(ctx, `
		UPDATE team_tasks
		SET status='running',
		    assignee=?,
		    lease_until=?,
		    version=version+1,
		    updated_at=?
		WHERE team_id=?
		  AND id=?
		  AND status='ready'
		  AND version=?`,
		assignee, leaseUntil, time.Now(),
		teamID, taskID, expectedVersion,
	)
	if err != nil {
		return false, err
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}

	_, err = c.q.ExecContext(ctx, `
		UPDATE teammates
		SET state='busy', last_heartbeat=?
		WHERE team_id=? AND id=?`,
		time.Now(), teamID, assignee,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}
```

## 1.7 RenewLease

这里有个容易漏掉的点：**不仅要续 task lease，还要续 path claim lease。**

```go
func (r *SQLiteRepo) RenewLease(ctx context.Context, teamID, taskID string, leaseUntil time.Time) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		var assignee sql.NullString
		err := core.q.QueryRowContext(ctx, `
			SELECT assignee
			FROM team_tasks
			WHERE team_id=? AND id=? AND status='running'`,
			teamID, taskID,
		).Scan(&assignee)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		if _, err := core.q.ExecContext(ctx, `
			UPDATE team_tasks
			SET lease_until=?, updated_at=?
			WHERE team_id=? AND id=? AND status='running'`,
			leaseUntil, time.Now(), teamID, taskID,
		); err != nil {
			return err
		}

		if _, err := core.q.ExecContext(ctx, `
			UPDATE team_path_claims
			SET lease_until=?
			WHERE team_id=? AND task_id=?`,
			leaseUntil, teamID, taskID,
		); err != nil {
			return err
		}

		if assignee.Valid {
			if _, err := core.q.ExecContext(ctx, `
				UPDATE teammates
				SET last_heartbeat=?
				WHERE team_id=? AND id=?`,
				time.Now(), teamID, assignee.String,
			); err != nil {
				return err
			}
		}

		return nil
	})
}
```

## 1.8 CompleteTask

完成任务时，一定要：

* task -> done
* 清 lease
* 释放 path claims
* teammate -> idle

```go
func (r *SQLiteRepo) CompleteTask(ctx context.Context, teamID, taskID string, summary string, resultRef *string) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		task, err := core.GetTask(ctx, teamID, taskID)
		if err != nil {
			return err
		}

		_, err = core.q.ExecContext(ctx, `
			UPDATE team_tasks
			SET status='done',
			    summary=?,
			    result_ref=?,
			    lease_until=NULL,
			    updated_at=?
			WHERE team_id=? AND id=?`,
			summary, resultRef, time.Now(), teamID, taskID,
		)
		if err != nil {
			return err
		}

		if _, err := core.q.ExecContext(ctx, `
			DELETE FROM team_path_claims
			WHERE team_id=? AND task_id=?`,
			teamID, taskID,
		); err != nil {
			return err
		}

		if task.Assignee != nil {
			if _, err := core.q.ExecContext(ctx, `
				UPDATE teammates
				SET state='idle', last_heartbeat=?
				WHERE team_id=? AND id=?`,
				time.Now(), teamID, *task.Assignee,
			); err != nil {
				return err
			}
		}

		return nil
	})
}
```

## 1.9 BlockTask / FailTask

```go
func (r *SQLiteRepo) BlockTask(ctx context.Context, teamID, taskID string, reason string) error {
	return r.setTerminalLikeTaskState(ctx, teamID, taskID, TaskBlocked, reason, nil, false)
}

func (r *SQLiteRepo) FailTask(ctx context.Context, teamID, taskID string, reason string, retryable bool) error {
	return r.setTerminalLikeTaskState(ctx, teamID, taskID, TaskFailed, reason, nil, retryable)
}

func (r *SQLiteRepo) setTerminalLikeTaskState(
	ctx context.Context,
	teamID, taskID string,
	status TaskStatus,
	reason string,
	resultRef *string,
	retryable bool,
) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		task, err := core.GetTask(ctx, teamID, taskID)
		if err != nil {
			return err
		}

		_, err = core.q.ExecContext(ctx, `
			UPDATE team_tasks
			SET status=?,
			    summary=?,
			    result_ref=?,
			    lease_until=NULL,
			    updated_at=?
			WHERE team_id=? AND id=?`,
			status, reason, resultRef, time.Now(), teamID, taskID,
		)
		if err != nil {
			return err
		}

		if _, err := core.q.ExecContext(ctx, `
			DELETE FROM team_path_claims
			WHERE team_id=? AND task_id=?`,
			teamID, taskID,
		); err != nil {
			return err
		}

		if task.Assignee != nil {
			nextState := TeammateIdle
			if retryable && status == TaskFailed {
				nextState = TeammateIdle
			}
			if _, err := core.q.ExecContext(ctx, `
				UPDATE teammates
				SET state=?, last_heartbeat=?
				WHERE team_id=? AND id=?`,
				nextState, time.Now(), teamID, *task.Assignee,
			); err != nil {
				return err
			}
		}

		return nil
	})
}
```

## 1.10 RequeueExpiredTasks

这个事务要同时做三件事：重排 task、释放 claims、更新 teammate。

```go
func (r *SQLiteRepo) RequeueExpiredTasks(ctx context.Context, teamID string, now time.Time) ([]Task, error) {
	var expired []Task

	err := r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		rows, err := core.q.QueryContext(ctx, `
			SELECT id, team_id, parent_task_id, title, goal, inputs_json,
			       status, priority, assignee, lease_until, retry_count,
			       read_paths_json, write_paths_json, deliverables_json,
			       summary, result_ref, version, created_at, updated_at
			FROM team_tasks
			WHERE team_id=?
			  AND status='running'
			  AND lease_until IS NOT NULL
			  AND lease_until < ?`,
			teamID, now,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		expired, err = scanTasks(rows)
		if err != nil {
			return err
		}
		if len(expired) == 0 {
			return nil
		}

		for _, t := range expired {
			if _, err := core.q.ExecContext(ctx, `
				UPDATE team_tasks
				SET status='ready',
				    assignee=NULL,
				    lease_until=NULL,
				    retry_count=retry_count+1,
				    updated_at=?
				WHERE team_id=? AND id=?`,
				now, teamID, t.ID,
			); err != nil {
				return err
			}

			if _, err := core.q.ExecContext(ctx, `
				DELETE FROM team_path_claims
				WHERE team_id=? AND task_id=?`,
				teamID, t.ID,
			); err != nil {
				return err
			}

			if t.Assignee != nil {
				if _, err := core.q.ExecContext(ctx, `
					UPDATE teammates
					SET state='offline', last_heartbeat=?
					WHERE team_id=? AND id=?`,
					now, teamID, *t.Assignee,
				); err != nil {
					return err
				}
			}
		}
		return nil
	})

	return expired, err
}
```

## 1.11 UnblockReadyTasks

这个 SQL 很重要，直接用“无未完成依赖”作为 ready 条件。

```go
func (c *sqliteCore) UnblockReadyTasks(ctx context.Context, teamID string) ([]Task, error) {
	_, err := c.q.ExecContext(ctx, `
		UPDATE team_tasks
		SET status='ready',
		    updated_at=?
		WHERE team_id=?
		  AND status IN ('pending', 'blocked')
		  AND NOT EXISTS (
			  SELECT 1
			  FROM team_task_dependencies d
			  JOIN team_tasks dep ON dep.id = d.depends_on_task_id
			  WHERE d.task_id = team_tasks.id
			    AND dep.status != 'done'
		  )`,
		time.Now(), teamID,
	)
	if err != nil {
		return nil, err
	}

	return c.ListReadyTasks(ctx, teamID, 1000)
}
```

## 1.12 Path claims

```go
func (c *sqliteCore) ListPathClaims(ctx context.Context, teamID string) ([]PathClaim, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, task_id, owner_agent_id, path, mode, lease_until
		FROM team_path_claims
		WHERE team_id=?
		ORDER BY lease_until ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PathClaim
	for rows.Next() {
		var p PathClaim
		if err := rows.Scan(&p.ID, &p.TeamID, &p.TaskID, &p.OwnerAgentID, &p.Path, &p.Mode, &p.LeaseUntil); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (c *sqliteCore) AcquirePathClaims(ctx context.Context, claims []PathClaim) error {
	for _, cl := range claims {
		if _, err := c.q.ExecContext(ctx, `
			INSERT INTO team_path_claims (
				id, team_id, task_id, owner_agent_id, path, mode, lease_until
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			cl.ID, cl.TeamID, cl.TaskID, cl.OwnerAgentID, cl.Path, cl.Mode, cl.LeaseUntil,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *sqliteCore) ReleasePathClaimsByTask(ctx context.Context, teamID, taskID string) error {
	_, err := c.q.ExecContext(ctx, `
		DELETE FROM team_path_claims
		WHERE team_id=? AND task_id=?`,
		teamID, taskID)
	return err
}
```

## 1.13 Mailbox

```go
func (c *sqliteCore) InsertMail(ctx context.Context, msg MailMessage) error {
	_, err := c.q.ExecContext(ctx, `
		INSERT INTO team_mailbox_messages (
			id, team_id, from_agent, to_agent, task_id,
			kind, body, metadata_json, created_at, acked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.TeamID, msg.FromAgent, msg.ToAgent, msg.TaskID,
		msg.Kind, msg.Body, mustJSON(msg.Metadata), msg.CreatedAt, msg.AckedAt,
	)
	return err
}

func (c *sqliteCore) ListUnreadMail(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, from_agent, to_agent, task_id,
		       kind, body, metadata_json, created_at, acked_at
		FROM team_mailbox_messages
		WHERE team_id=?
		  AND acked_at IS NULL
		  AND (to_agent=? OR to_agent='*')
		ORDER BY created_at ASC
		LIMIT ?`, teamID, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MailMessage
	for rows.Next() {
		var m MailMessage
		var taskID sql.NullString
		var ackedAt sql.NullTime
		var meta []byte
		if err := rows.Scan(
			&m.ID, &m.TeamID, &m.FromAgent, &m.ToAgent, &taskID,
			&m.Kind, &m.Body, &meta, &m.CreatedAt, &ackedAt,
		); err != nil {
			return nil, err
		}
		m.TaskID = nullStringPtr(taskID)
		m.AckedAt = nullTimePtr(ackedAt)
		_ = json.Unmarshal(meta, &m.Metadata)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (c *sqliteCore) AckMail(ctx context.Context, teamID, agentID string, msgIDs []string, at time.Time) error {
	if len(msgIDs) == 0 {
		return nil
	}
	q, args := buildIn(`
		UPDATE team_mailbox_messages
		SET acked_at=?
		WHERE team_id=?
		  AND (to_agent=? OR to_agent='*')
		  AND id IN (%s)`,
		append([]any{at, teamID, agentID}, stringsToAny(msgIDs)...)...,
	)
	_, err := c.q.ExecContext(ctx, q, args...)
	return err
}
```

`buildIn()` / `stringsToAny()` 这种小 helper 你项目里应该已经有类似实现，没有的话我下条可以直接补。

## 1.14 Team events

```go
func (r *SQLiteRepo) AppendEvent(ctx context.Context, evt TeamEvent) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		var nextSeq int64
		if err := core.q.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1
			FROM team_events
			WHERE team_id=?`, evt.TeamID,
		).Scan(&nextSeq); err != nil {
			return err
		}

		_, err := core.q.ExecContext(ctx, `
			INSERT INTO team_events (
				team_id, seq, type, payload_json, created_at
			) VALUES (?, ?, ?, ?, ?)`,
			evt.TeamID, nextSeq, evt.Type, evt.Payload, evt.CreatedAt,
		)
		return err
	})
}
```

---

# 2. migration SQL 草案

建议分 3 个 migration。

## 2.1 `0005_team_core.sql`

```sql
CREATE TABLE teams (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  lead_session_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'paused', 'done', 'failed')),
  strategy TEXT NOT NULL,
  max_teammates INTEGER NOT NULL,
  max_writers INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);

CREATE TABLE teammates (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  name TEXT NOT NULL,
  profile TEXT NOT NULL,
  session_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('idle', 'busy', 'blocked', 'offline')),
  last_heartbeat DATETIME NOT NULL,
  capabilities_json BLOB NOT NULL,
  metadata_json BLOB,
  FOREIGN KEY(team_id) REFERENCES teams(id)
);

CREATE TABLE team_tasks (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  parent_task_id TEXT,
  title TEXT NOT NULL,
  goal TEXT NOT NULL,
  inputs_json BLOB,
  status TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'running', 'blocked', 'done', 'failed', 'cancelled')),
  priority INTEGER NOT NULL DEFAULT 0,
  assignee TEXT,
  lease_until DATETIME,
  retry_count INTEGER NOT NULL DEFAULT 0,
  read_paths_json BLOB,
  write_paths_json BLOB,
  deliverables_json BLOB,
  summary TEXT,
  result_ref TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL,
  FOREIGN KEY(team_id) REFERENCES teams(id)
);

CREATE TABLE team_task_dependencies (
  task_id TEXT NOT NULL,
  depends_on_task_id TEXT NOT NULL,
  PRIMARY KEY(task_id, depends_on_task_id)
);

CREATE INDEX idx_team_tasks_ready
ON team_tasks(team_id, status, priority DESC, created_at ASC);

CREATE INDEX idx_team_tasks_running
ON team_tasks(team_id, status, lease_until);

CREATE INDEX idx_team_deps_task
ON team_task_dependencies(task_id);

CREATE INDEX idx_team_deps_depends_on
ON team_task_dependencies(depends_on_task_id);

CREATE UNIQUE INDEX uq_team_running_assignee
ON team_tasks(team_id, assignee)
WHERE status='running' AND assignee IS NOT NULL;
```

## 2.2 `0006_team_mailbox_and_claims.sql`

```sql
CREATE TABLE team_mailbox_messages (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  from_agent TEXT NOT NULL,
  to_agent TEXT NOT NULL,
  task_id TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('info', 'question', 'challenge', 'handoff', 'done', 'blocked', 'failed', 'warning')),
  body TEXT NOT NULL,
  metadata_json BLOB,
  created_at DATETIME NOT NULL,
  acked_at DATETIME
);

CREATE TABLE team_path_claims (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  path TEXT NOT NULL,
  mode TEXT NOT NULL CHECK (mode IN ('read', 'write')),
  lease_until DATETIME NOT NULL
);

CREATE INDEX idx_team_mail_unread
ON team_mailbox_messages(team_id, to_agent, created_at)
WHERE acked_at IS NULL;

CREATE INDEX idx_team_claims_lookup
ON team_path_claims(team_id, lease_until);

CREATE INDEX idx_team_claims_path
ON team_path_claims(team_id, path);
```

## 2.3 `0007_team_events.sql`

```sql
CREATE TABLE team_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  team_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  created_at DATETIME NOT NULL,
  UNIQUE(team_id, seq)
);

CREATE INDEX idx_team_events_team_seq
ON team_events(team_id, seq);
```

---

# 3. `internal/runtime/tools/send_team_message.go`

这个工具不要允许 agent 自己伪造 `from_agent` 和 `team_id`。
正确做法是：**这两个身份字段从受信运行时上下文注入，不从 tool args 里读。**

也就是说，tool args 只允许写：

* 发给谁
* 消息类型
* task_id（可选）
* body
* metadata

## 3.1 上下文接口

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"your/module/internal/team"
)

var (
	ErrNoTeamRuntimeContext = errors.New("no team runtime context")
	ErrInvalidMessageKind   = errors.New("invalid message kind")
)

type TeamRuntimeContext struct {
	TeamID        string
	AgentID       string
	CurrentTaskID string
}

type TeamContextProvider interface {
	FromContext(ctx context.Context) (TeamRuntimeContext, bool)
}
```

## 3.2 tool args / result

```go
type SendTeamMessageArgs struct {
	ToAgent  string         `json:"to_agent"`
	Kind     string         `json:"kind"`
	TaskID   string         `json:"task_id,omitempty"`
	Body     string         `json:"body"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SendTeamMessageResult struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}
```

## 3.3 工具骨架

这里我写成通用形态，你把 `Descriptor()` 对齐到你现在的 toolkit 接口就行。

```go
type TeamMailWriter interface {
	InsertMail(ctx context.Context, msg team.MailMessage) error
}

type SendTeamMessageTool struct {
	Repo        TeamMailWriter
	TeamContext TeamContextProvider
	Now         func() time.Time
}

func NewSendTeamMessageTool(repo TeamMailWriter, teamCtx TeamContextProvider, now func() time.Time) *SendTeamMessageTool {
	if now == nil {
		now = time.Now
	}
	return &SendTeamMessageTool{
		Repo:        repo,
		TeamContext: teamCtx,
		Now:         now,
	}
}

func (t *SendTeamMessageTool) Name() string {
	return "SendTeamMessage"
}

func (t *SendTeamMessageTool) Execute(ctx context.Context, raw json.RawMessage) (*SendTeamMessageResult, error) {
	var args SendTeamMessageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}

	rt, ok := t.TeamContext.FromContext(ctx)
	if !ok {
		return nil, ErrNoTeamRuntimeContext
	}

	if !isValidMessageKind(args.Kind) {
		return nil, ErrInvalidMessageKind
	}
	if args.ToAgent == "" {
		return nil, errors.New("to_agent is required")
	}
	if args.Body == "" && len(args.Metadata) == 0 {
		return nil, errors.New("body or metadata is required")
	}

	taskID := args.TaskID
	if taskID == "" {
		taskID = rt.CurrentTaskID
	}
	if taskID == "" && (args.Kind == "done" || args.Kind == "blocked" || args.Kind == "failed") {
		return nil, errors.New("task_id is required for done/blocked/failed")
	}

	// done / blocked / failed 的最小字段约束
	switch args.Kind {
	case "done":
		if _, ok := args.Metadata["summary"]; !ok {
			return nil, errors.New("done message requires metadata.summary")
		}
	case "blocked", "failed":
		if _, ok := args.Metadata["reason"]; !ok && args.Body == "" {
			return nil, errors.New("blocked/failed message requires reason")
		}
	}

	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}

	msg := team.MailMessage{
		ID:        newID(),
		TeamID:    rt.TeamID,
		FromAgent: rt.AgentID,
		ToAgent:   args.ToAgent,
		TaskID:    taskIDPtr,
		Kind:      args.Kind,
		Body:      args.Body,
		Metadata:  args.Metadata,
		CreatedAt: t.Now(),
	}

	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	// 强制覆盖可信字段，避免 agent 伪造
	msg.Metadata["task_id"] = taskID
	msg.Metadata["from_agent"] = rt.AgentID

	if err := t.Repo.InsertMail(ctx, msg); err != nil {
		return nil, err
	}

	return &SendTeamMessageResult{
		MessageID: msg.ID,
		Status:    "queued",
	}, nil
}

func isValidMessageKind(kind string) bool {
	switch kind {
	case "info", "question", "challenge", "handoff", "done", "blocked", "failed", "warning":
		return true
	default:
		return false
	}
}

func newID() string {
	return fmt.Sprintf("id_%d", time.Now().UnixNano())
}
```

---

# 4. 这个工具怎么和 orchestrator 闭环

你上一轮的 `orchestrator.go` 里已经有：

* `collectMailboxReports()`
* `handleDoneMail()`
* `handleBlockedMail()`
* `handleFailedMail()`

所以现在 teammate 只要在 prompt 里明确被要求：

```text
完成任务后调用 SendTeamMessage:
{
  "to_agent": "__orchestrator__",
  "kind": "done",
  "body": "task completed",
  "metadata": {
    "summary": "...",
    "result_ref": "optional artifact ref"
  }
}
```

阻塞时：

```text
{
  "to_agent": "__orchestrator__",
  "kind": "blocked",
  "body": "waiting for clarification",
  "metadata": {
    "reason": "...具体原因..."
  }
}
```

失败时：

```text
{
  "to_agent": "__orchestrator__",
  "kind": "failed",
  "body": "task failed",
  "metadata": {
    "reason": "...",
    "retryable": true
  }
}
```

这样最小闭环就成了：

```text
orchestrator dispatch task
 -> teammate session 执行
 -> teammate 调 SendTeamMessage
 -> mailbox 落库
 -> orchestrator tick 消费 mailbox
 -> task 状态更新
 -> dependents 解锁 / team 收敛
```

---

# 5. 两个很关键的实现补丁

这两个不改，第一版很容易埋坑。

## 5.1 `dispatchOne()` 里的事务边界要保持

`CanClaim()` 和 `AcquirePathClaims()` 一定要在 `WithTx()` 里面做。
否则多个 orchestrator tick 或多个进程同时跑时，会出现“双重 claim”。

你上一轮那版 `dispatchOne()` 思路是对的，保留就行。

## 5.2 `RenewLease()` 要同时续 path claims

这个上面已经补了。
如果只续 task，不续 path claim，另一个 agent 会在老 claim 过期后误拿到同一写路径。

---

# 6. 你现在还差一个很小但很值钱的工具：`ReadMailboxDigest`

严格说，最小闭环有 `SendTeamMessage` 就够了。
但如果你想让 teammate 在长任务中真正利用别人的结果，建议下一步补一个只读工具：

```go
ReadMailboxDigest(limit=10, kinds=["info","question","handoff"])
```

这样 teammate 不只是被动接 prompt 里的 digest，也能主动拉一下最新 team 状态。

不过这个不是第一优先级。第一优先级还是先把 `SendTeamMessage` 打通。

---

# 7. 第一轮测试建议

这几条建议你直接写成 integration test。

## 7.1 `TestClaimTaskAndAcquirePathClaims_Atomic`

场景：

* 两个 goroutine 同时对同一 ready task 调 `dispatchOne`
* 最终只能有一个成功
* `team_path_claims` 只能出现一组 claim
* task 只能有一个 assignee

## 7.2 `TestRenewLease_AlsoRenewsClaims`

场景：

* claim 一个写路径
* 调 `RenewLease`
* 检查 `team_tasks.lease_until` 和 `team_path_claims.lease_until` 同时更新

## 7.3 `TestCompleteTask_ReleasesClaimsAndIdlesTeammate`

场景：

* running task + busy teammate + 一组 path claim
* `CompleteTask()`
* task -> done
* claims 被删
* teammate -> idle

## 7.4 `TestRequeueExpiredTasks_ReleasesClaimsAndMarksOffline`

场景：

* running task 过期
* `RequeueExpiredTasks()`
* task -> ready
* assignee 清空
* retry_count + 1
* claims 被删
* teammate -> offline

## 7.5 `TestSendTeamMessage_UsesTrustedRuntimeIdentity`

场景：

* agent 在 args 里伪造 team_id/from_agent
* tool 忽略这些伪造字段
* 实际落库仍使用运行时注入的 `TeamID/AgentID`

---

# 8. 你下一步最该补哪两个文件

按最省返工的顺序，我建议现在直接写：

1. `internal/team/sqlite_repo.go`
2. `internal/runtime/tools/send_team_message.go`

这两个文件一接上，你前面那版 `orchestrator.go` 就能真正跑一个“最小 Team 闭环”。

下一条我可以继续直接给你：

`buildIn()` / `scanTasks()` / `sqlite_repo.go` 剩余 CRUD 补全骨架，以及 `ReadMailboxDigest` 工具实现。
