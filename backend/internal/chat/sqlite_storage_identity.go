package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// CanonicalRebuildStats 汇总 checkpoint 投影重建路径的归因（P0-3 步骤 0 观测）：
//   - FullCanonicalLoads：default 分支回退到全量 canonical 解码的次数；
//   - FastIdentityProofs：用 identity_hash 证明「入参已到最新」而跳过全量解码的次数；
//   - BackfilledRows：identity_hash 惰性回填的行数。
//
// 目标是让「UI 为什么慢 / 落库为什么慢」在复测时可直接读到分支占比：
// 迁移后的旧会话第一次 default 回退会回填全部历史行，此后 FastIdentityProofs
// 应持续增长而 FullCanonicalLoads 保持不变。
type CanonicalRebuildStats struct {
	FullCanonicalLoads int64
	FastIdentityProofs int64
	BackfilledRows     int64
}

// CanonicalRebuildStats 返回该存储实例的投影重建归因计数快照。
func (s *SQLiteSessionStorage) CanonicalRebuildStats() CanonicalRebuildStats {
	if s == nil {
		return CanonicalRebuildStats{}
	}
	return CanonicalRebuildStats{
		FullCanonicalLoads: s.canonicalFullLoads.Load(),
		FastIdentityProofs: s.canonicalFastProofs.Load(),
		BackfilledRows:     s.canonicalBackfilledRows.Load(),
	}
}

// canonicalMessageIdentityHash 计算消息的身份指纹，语义与
// incomingHistoryReachesNewest 使用的 messageIdentityEqual 完全一致：
// 只比较 role 与 content（长度前缀拼接，避免字段边界歧义）。
//
// 为什么不能复用 payload 的 sha256：payload 包含 id/timestamp/metadata，重建
// 后的内存副本会携带全新的 id 与时间戳，按 payload 比较会把「同一条消息」判成
// 不同，快速路径随即失效。
func canonicalMessageIdentityHash(message types.Message) []byte {
	hasher := sha256.New()
	var length [4]byte
	writeField := func(value string) {
		binary.LittleEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	writeField(message.Role)
	writeField(message.Content)
	return hasher.Sum(nil)
}

// canonicalIdentityHashesCompleteTx 报告该会话的 canonical 行是否全部具备
// identity_hash。判断「入参最新消息不存在于 canonical」必须以此为前提：NULL
// 行对等值查询不可见，部分回填会让「新消息」与「已存在但未回填」无法区分。
func (s *SQLiteSessionStorage) canonicalIdentityHashesCompleteTx(ctx context.Context, tx *sql.Tx, sessionID string) (bool, error) {
	var missing int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM session_messages
		WHERE session_id = ? AND identity_hash IS NULL
		LIMIT 1
	`, sessionID).Scan(&missing)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe canonical identity hashes: %w", err)
	}
	return false, nil
}

// incomingHistoryReachesNewestFastTx 用 identity_hash 以 O(1)（索引查询）证明
// incomingHistoryReachesNewest 的结论，避免为回答这一问题全量解码 canonical
// 转录（生产 6472 条会话实测 4s/次 checkpoint，占 profile 14%）。
//
// 返回 proven=true 当且仅当旧路径「加载全量 canonical 后调用
// incomingHistoryReachesNewest」必然为 true，三种情形：
//  1. canonical 为空：旧路径把 source 落回 session.History，等价于已到最新；
//  2. 入参最新消息与 canonical 最新行身份一致（必然命中最后的相等项）；
//  3. 入参最新消息在 canonical 中不存在（例如 compaction summary 这类新消息），
//     旧路径的语义是「honor caller replacement」。
//
// 其余情况（stale 窗口：消息存在于更早位置；或 identity_hash 尚未回填完）
// 返回 false，调用方保留全量路径，并在同一事务里惰性回填。
func (s *SQLiteSessionStorage) incomingHistoryReachesNewestFastTx(ctx context.Context, tx *sql.Tx, sessionID string, history []types.Message) (bool, error) {
	if len(history) == 0 {
		return false, nil
	}
	var newest []byte
	err := tx.QueryRowContext(ctx, `
		SELECT identity_hash FROM session_messages
		WHERE session_id = ?
		ORDER BY seq DESC
		LIMIT 1
	`, sessionID).Scan(&newest)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read newest canonical identity: %w", err)
	}
	want := canonicalMessageIdentityHash(history[len(history)-1])
	if len(newest) > 0 && bytes.Equal(newest, want) {
		return true, nil
	}
	if len(newest) == 0 {
		// 最新行尚未回填（迁移后的旧库）：无法证明，保守走全量路径。
		return false, nil
	}
	complete, err := s.canonicalIdentityHashesCompleteTx(ctx, tx, sessionID)
	if err != nil || !complete {
		return false, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM session_messages
		WHERE session_id = ? AND identity_hash = ?
		LIMIT 1
	`, sessionID, want).Scan(&exists)
	if err == sql.ErrNoRows {
		// 不存在于 canonical：新消息（compaction summary / 全新 turn）。
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe canonical identity: %w", err)
	}
	// 存在但不在最新位置：入参是过期窗口，必须回退全量路径由
	// incomingHistoryReachesNewest 决定 source（结果会用 canonical）。
	return false, nil
}

// backfillCanonicalIdentityHashesTx 为 identity_hash 为 NULL 的历史行补写指纹。
//
// 迁移只在旧库首次落入全量回退时执行一次：按 seq 升序扫描、只解析 role 与
// content（轻量结构体，不构造完整 Message），再用准备好的语句批量 UPDATE。
// 任何单行解码失败都跳过该行（保持 NULL）：读取与投影路径本就会跳过/回退，
// 回填是加速手段，绝不改变持久化语义。调用方按 best-effort 处理返回的 error。
func (s *SQLiteSessionStorage) backfillCanonicalIdentityHashesTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT seq, payload_json, artifact_path, byte_count, sha256
		FROM session_messages
		WHERE session_id = ? AND identity_hash IS NULL
		ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("scan canonical rows for identity backfill: %w", err)
	}
	type pendingIdentity struct {
		seq  int
		hash []byte
	}
	var pending []pendingIdentity
	for rows.Next() {
		var sequence, byteCount int
		var inline []byte
		var artifact sql.NullString
		var digest string
		if err := rows.Scan(&sequence, &inline, &artifact, &byteCount, &digest); err != nil {
			_ = rows.Close()
			return len(pending), fmt.Errorf("scan canonical row for identity backfill: %w", err)
		}
		payload, err := s.readCanonicalPayload(inline, artifact, byteCount, digest)
		if err != nil {
			// 载荷不可读的行保持 NULL：与全量读取路径对同一行的处理保持一致。
			continue
		}
		var light struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(payload, &light); err != nil {
			var message types.Message
			if fallbackErr := json.Unmarshal(payload, &message); fallbackErr != nil {
				continue
			}
			light.Role, light.Content = message.Role, message.Content
		}
		pending = append(pending, pendingIdentity{
			seq:  sequence,
			hash: canonicalMessageIdentityHash(types.Message{Role: light.Role, Content: light.Content}),
		})
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return len(pending), fmt.Errorf("iterate canonical rows for identity backfill: %w", rowsErr)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE session_messages SET identity_hash = ?
		WHERE session_id = ? AND seq = ?
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare identity backfill: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	written := 0
	for _, item := range pending {
		if _, err := stmt.ExecContext(ctx, item.hash, sessionID, item.seq); err != nil {
			return written, fmt.Errorf("backfill identity hash seq=%d: %w", item.seq, err)
		}
		written++
	}
	return written, nil
}

// backfillCanonicalIdentityHashesBestEffort 在投影重建回退路径中执行回填，
// 失败只记日志：回填是加速手段，不能让一次 checkpoint 因它失败。
func (s *SQLiteSessionStorage) backfillCanonicalIdentityHashesBestEffort(ctx context.Context, tx *sql.Tx, sessionID string) {
	written, err := s.backfillCanonicalIdentityHashesTx(ctx, tx, sessionID)
	if err != nil {
		if logger := logpkg.S(); logger != nil {
			logger.Infof("[session-history] identity hash backfill failed session=%s written=%d error=%v",
				sessionID, written, err)
		}
	}
	if written > 0 {
		s.canonicalBackfilledRows.Add(int64(written))
	}
}
