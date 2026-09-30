package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// exploration_sessions / exploration_nodes / exploration_edges 的写路径
// （06 §4 Phase 2 W1 / S1）。
//
// 不变量：
//   - 全部经 execWrite（IMMEDIATE 事务 + 锁重试）；reader 角色硬失败
//     ErrReadOnlyStore，不静默丢弃（沿用 Phase 1 的单写者语义）。
//   - 主键由 exploration.go 的稳定派生函数给出：同 target 的重复写入命中同一行，
//     由主键与 ON CONFLICT 保证幂等，不依赖"先查后写"。
//   - 节点必须携带非空 knowledge_version；空版本在参数校验阶段拒绝。
//   - use_count / last_used_at 由 store 维护：首次写入 use_count=1 且
//     last_used_at=NULL，同 target 重复写入 use_count+1 并刷新 last_used_at。

// 与 0001_init.sql 的 DDL 默认值对齐：
// confidence REAL NOT NULL DEFAULT 0.5；weight REAL NOT NULL DEFAULT 1.0。
const (
	defaultExplorationConfidence = 0.5
	defaultExplorationEdgeWeight = 1.0
)

// UpsertExplorationSession 以 upsert 语义登记一次探索会话，返回其稳定 id。
//
// 身份 = (workspace_id, session_id, task_id)：重复登记只刷新 updated_at，
// 不改写身份字段与 created_at。reader 调用返回 ErrReadOnlyStore。
func (s *sqliteStore) UpsertExplorationSession(ctx context.Context, sess ExplorationSession) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("knowledge: upsert exploration session: store is not open")
	}
	if err := sess.Validate(); err != nil {
		return "", fmt.Errorf("knowledge: upsert exploration session: %w", err)
	}
	if strings.TrimSpace(sess.ID) == "" {
		sess.ID = ExplorationSessionID(sess.WorkspaceID, sess.SessionID, sess.TaskID)
	}
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = now
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO exploration_sessions (id, workspace_id, session_id, task_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at
		`,
			sess.ID, sess.WorkspaceID, sess.SessionID, nullIfEmpty(sess.TaskID),
			unixMillis(sess.CreatedAt), unixMillis(sess.UpdatedAt))
		return err
	})
	if err != nil {
		return "", fmt.Errorf("knowledge: upsert exploration session: %w", err)
	}
	return sess.ID, nil
}

// AppendExplorationNode 追加一条探索节点，返回其稳定 id。
//
// 幂等语义（DoD ②）：同一 exploration_id 内同一 target 重复写入命中同一行，
// use_count 累加、last_used_at 刷新；其余可变量（类型、引用、摘要、置信度、
// knowledge_version）取最新一次观察值。首次写入 use_count=1、last_used_at=NULL。
// 调用方传入的 UseCount 被忽略：计数由 store 独占维护。
func (s *sqliteStore) AppendExplorationNode(ctx context.Context, node ExplorationNode) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("knowledge: append exploration node: store is not open")
	}
	if err := node.Validate(); err != nil {
		return "", fmt.Errorf("knowledge: append exploration node: %w", err)
	}
	if strings.TrimSpace(node.ID) == "" {
		node.ID = ExplorationNodeID(node.ExplorationID, node.Target)
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now()
	}
	if node.Confidence <= 0 {
		node.Confidence = defaultExplorationConfidence
	}
	now := time.Now()
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO exploration_nodes (
				id, exploration_id, node_type, target, file_id, symbol_id, summary,
				confidence, knowledge_version, created_at, last_used_at, use_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 1)
			ON CONFLICT(id) DO UPDATE SET
				node_type         = excluded.node_type,
				file_id           = excluded.file_id,
				symbol_id         = excluded.symbol_id,
				summary           = excluded.summary,
				confidence        = excluded.confidence,
				knowledge_version = excluded.knowledge_version,
				last_used_at      = ?,
				use_count         = use_count + 1
		`,
			node.ID, node.ExplorationID, string(node.NodeType), node.Target,
			nullIfEmpty(node.FileID), nullIfEmpty(node.SymbolID), nullIfEmpty(node.Summary),
			node.Confidence, node.KnowledgeVersion, unixMillis(node.CreatedAt),
			unixMillis(now))
		return err
	})
	if err != nil {
		return "", fmt.Errorf("knowledge: append exploration node: %w", err)
	}
	return node.ID, nil
}

// TouchExplorationNode 记录一次对既有节点的复用：use_count+1、last_used_at=at
// （at 为零值时取当前时间）。节点不存在返回错误——调用方必须能区分
// "节点已被级联清理"与"触碰成功"，否则复用统计会静默失真。
func (s *sqliteStore) TouchExplorationNode(ctx context.Context, nodeID string, at time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge: touch exploration node: store is not open")
	}
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("knowledge: touch exploration node: node id is required")
	}
	if at.IsZero() {
		at = time.Now()
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE exploration_nodes
			SET last_used_at = ?, use_count = use_count + 1
			WHERE id = ?`, unixMillis(at), nodeID)
		if err != nil {
			return err
		}
		if affected, err := res.RowsAffected(); err == nil && affected == 0 {
			return fmt.Errorf("%s not found", nodeID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("knowledge: touch exploration node: %w", err)
	}
	return nil
}

// AppendExplorationEdge 追加一条节点关系，返回其稳定 id。
//
// 同一 (exploration_id, from_node_id, to_node_id, edge_type) 重复追加是幂等的，
// 权重取最新观察值。reader 调用返回 ErrReadOnlyStore。
func (s *sqliteStore) AppendExplorationEdge(ctx context.Context, edge ExplorationEdge) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("knowledge: append exploration edge: store is not open")
	}
	if err := edge.Validate(); err != nil {
		return "", fmt.Errorf("knowledge: append exploration edge: %w", err)
	}
	if strings.TrimSpace(edge.ID) == "" {
		edge.ID = ExplorationEdgeID(edge.ExplorationID, edge.FromNodeID, edge.ToNodeID, edge.EdgeType)
	}
	if edge.Weight <= 0 {
		edge.Weight = defaultExplorationEdgeWeight
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO exploration_edges (id, exploration_id, from_node_id, to_node_id, edge_type, weight)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET weight = excluded.weight
		`,
			edge.ID, edge.ExplorationID, edge.FromNodeID, edge.ToNodeID,
			string(edge.EdgeType), edge.Weight)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("knowledge: append exploration edge: %w", err)
	}
	return edge.ID, nil
}
