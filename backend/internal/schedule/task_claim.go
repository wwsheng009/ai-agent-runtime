package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// TaskClaimer 是任务级 claim 的存储能力（S4，§4.3，D3：并发控制在 store CAS
// 内完成——SQLite 单写域 + 现成索引最可靠，mesh 租约只负责领导权）。
//
// 语义：driver 处理任务前先原子抢占（owner + 短租约）；抢失败 = 另一节点正在
// 处理 → 跳过。租约过期（崩溃/失联）后由新 leader 接管重试；"至多一次"最终
// 仍由 occurrence 账本 + 接收侧去重收敛（§12.1），claim 是冗余防线。
type TaskClaimer interface {
	// ClaimTasks 以 CAS 方式把 ids 抢占给 owner（租约至 leaseUntil）：仅当行
	// 未被持有、或持有者是 owner 本人、或旧租约已过期（< now）时成功。返回
	// 实际抢到的 id（一次事务内的多行 CAS；部分成功是正常结果）。
	ClaimTasks(ids []string, owner string, leaseUntil, now time.Time) ([]string, error)
	// RenewTaskClaims 续期 owner 仍持有的任务租约；返回续期行数。
	RenewTaskClaims(ids []string, owner string, leaseUntil time.Time) (int, error)
	// ReleaseTaskClaims 释放 owner 持有的任务租约（非持有者 no-op）。
	ReleaseTaskClaims(ids []string, owner string) error
}

// TaskAttemptStore 是 attempts 持久化与死信状态显式化的存储能力（S4，§4.3 ③）。
// 计数供 leader 易主后的退避延续（避免接管即重试风暴）；死信时间戳供查询。
// 这些写是投递簿记：不推进 state_version，也不重写任务内容。
type TaskAttemptStore interface {
	// RecordTaskAttempt 记一次失败尝试：attempt_count+1、last_attempt_at=now；
	// deadLettered=true 时记录 dead_lettered_at（已有值不覆盖）。返回最新计数。
	RecordTaskAttempt(id string, now time.Time, deadLettered bool) (int, error)
	// ClearTaskAttempts 清空尝试计数与死信标记（投递成功后调用）。
	ClearTaskAttempts(id string) error
}

const (
	// claimLease 是任务 claim 的默认租约：覆盖一次投递 + 余量；长执行
	// （内嵌/headless）在启动前显式续期到执行上限之外（Runtime.renewClaimsForLongWork）。
	claimLease = 60 * time.Second
	// claimRetryDelay 是抢不到 claim 时的本地延后（等持有者完成或租约过期）。
	claimRetryDelay = 15 * time.Second
)

// ClaimTasks 实现 TaskClaimer（§4.3 组级 claim：同一批到期任务一次事务内多行
// CAS；部分成功是正常结果，调用方对未抢到的成员延后重扫）。
func (s *SQLiteStore) ClaimTasks(ids []string, owner string, leaseUntil, now time.Time) ([]string, error) {
	owner = strings.TrimSpace(owner)
	cleaned := dedupeIDs(ids)
	if len(cleaned) == 0 || owner == "" {
		return nil, nil
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	claimed := make([]string, 0, len(cleaned))
	for _, id := range cleaned {
		res, err := tx.ExecContext(ctx, `
UPDATE schedule_tasks SET owner_instance_id = ?, lease_expires_at = ?
WHERE id = ? AND (owner_instance_id IS NULL OR owner_instance_id = '' OR owner_instance_id = ? OR lease_expires_at IS NULL OR lease_expires_at < ?)`,
			owner, formatInstant(leaseUntil), id, owner, formatInstant(now))
		if err != nil {
			return nil, fmt.Errorf("claim task %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected > 0 {
			claimed = append(claimed, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// RenewTaskClaims 实现 TaskClaimer：续期 owner 仍持有的任务租约。
func (s *SQLiteStore) RenewTaskClaims(ids []string, owner string, leaseUntil time.Time) (int, error) {
	cleaned := dedupeIDs(ids)
	owner = strings.TrimSpace(owner)
	if len(cleaned) == 0 || owner == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE schedule_tasks SET lease_expires_at = ? WHERE owner_instance_id = ? AND id IN (`+idPlaceholders(len(cleaned))+`)`,
		append([]any{formatInstant(leaseUntil), owner}, stringArgs(cleaned)...)...)
	if err != nil {
		return 0, fmt.Errorf("renew task claims: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ReleaseTaskClaims 实现 TaskClaimer：释放 owner 持有的任务租约。
func (s *SQLiteStore) ReleaseTaskClaims(ids []string, owner string) error {
	cleaned := dedupeIDs(ids)
	owner = strings.TrimSpace(owner)
	if len(cleaned) == 0 || owner == "" {
		return nil
	}
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE schedule_tasks SET owner_instance_id = NULL, lease_expires_at = NULL WHERE owner_instance_id = ? AND id IN (`+idPlaceholders(len(cleaned))+`)`,
		append([]any{owner}, stringArgs(cleaned)...)...); err != nil {
		return fmt.Errorf("release task claims: %w", err)
	}
	return nil
}

// RecordTaskAttempt 实现 TaskAttemptStore：attempt_count+1、last_attempt_at=now；
// deadLettered 时补记 dead_lettered_at（保留首次留痕）。
func (s *SQLiteStore) RecordTaskAttempt(id string, now time.Time, deadLettered bool) (int, error) {
	stamp := formatInstant(now)
	var dead any
	if deadLettered {
		dead = stamp
	}
	if _, err := s.db.ExecContext(context.Background(), `
UPDATE schedule_tasks SET attempt_count = attempt_count + 1, last_attempt_at = ?,
    dead_lettered_at = COALESCE(dead_lettered_at, ?)
WHERE id = ?`, stamp, dead, id); err != nil {
		return 0, fmt.Errorf("record attempt for %s: %w", id, err)
	}
	var count int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT attempt_count FROM schedule_tasks WHERE id = ?`, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("read attempt count for %s: %w", id, err)
	}
	return count, nil
}

// ClearTaskAttempts 实现 TaskAttemptStore：投递成功后清空计数与死信留痕。
func (s *SQLiteStore) ClearTaskAttempts(id string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE schedule_tasks SET attempt_count = 0, last_attempt_at = NULL, dead_lettered_at = NULL WHERE id = ?`,
		id); err != nil {
		return fmt.Errorf("clear attempts for %s: %w", id, err)
	}
	return nil
}

// dedupeIDs 去重并剔除空 id（保持输入顺序）。
func dedupeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// idPlaceholders 生成 n 个 `?` 的占位串（n > 0；调用方保证非空）。
func idPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// stringArgs 把 []string 转为 []any（append 展开用）。
func stringArgs(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
