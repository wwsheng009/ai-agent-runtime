package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// exploration_sessions / exploration_nodes 的读路径（06 §4 Phase 2 W1 / S1）。
//
// 读方法只使用 s.db，不触碰 execWrite：reader 角色的只读句柄同样可用
// （ADR-0001 单写者 / 多读者）。workspace 隔离通过 join exploration_sessions
// 实现——exploration_nodes 自身没有 workspace_id 列。

// LookupExplorationNodes 返回工作区内的探索节点。
//
// 三条查询路径共用本方法：
//   - 任务工作集：给出 TaskID（精确匹配 sessions.task_id）；
//   - 会话级工作集：给出 SessionID（限定该会话下 task_id 为空的节点，与 W2
//     写入侧"宿主无任务语义"的回退口径一致）；
//   - 跨任务复用：TaskID / SessionID 都为空，按 Target（精确匹配）+ Type
//     过滤，按"最近使用优先"返回。
//
// 排序稳定：last_used_at 非空的在前、按时间倒序；未使用过的按 created_at
// 倒序；同刻用 id 兜底。Limit <= 0 时使用 defaultQueryLimit。
func (s *sqliteStore) LookupExplorationNodes(ctx context.Context, q ExplorationNodeQuery) ([]ExplorationNode, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("knowledge: lookup exploration nodes: store is not open")
	}
	if strings.TrimSpace(q.WorkspaceID) == "" {
		return nil, errors.New("knowledge: lookup exploration nodes: workspace_id is required")
	}
	if q.Type != "" && !q.Type.Valid() {
		return nil, fmt.Errorf("knowledge: lookup exploration nodes: invalid node_type %q", q.Type)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}

	where := []string{"s.workspace_id = ?"}
	args := []any{q.WorkspaceID}
	if taskID := strings.TrimSpace(q.TaskID); taskID != "" {
		where = append(where, "s.task_id = ?")
		args = append(args, taskID)
	}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		// 会话级工作集只覆盖无任务锚点的行：任务行必须走 TaskID 路径，
		// 避免会话级检索把任务作用域的数据带进更宽松的阈值一侧。
		where = append(where, "s.session_id = ?", "COALESCE(s.task_id, '') = ''")
		args = append(args, sessionID)
	}
	if target := strings.TrimSpace(q.Target); target != "" {
		where = append(where, "n.target = ?")
		args = append(args, target)
	}
	if q.Type != "" {
		where = append(where, "n.node_type = ?")
		args = append(args, string(q.Type))
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.exploration_id, n.node_type, n.target,
		       COALESCE(n.file_id, ''), COALESCE(n.symbol_id, ''), COALESCE(n.summary, ''),
		       n.confidence, n.knowledge_version, n.created_at,
		       COALESCE(n.last_used_at, 0), n.use_count
		FROM exploration_nodes n
		JOIN exploration_sessions s ON s.id = n.exploration_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY (n.last_used_at IS NULL) ASC, n.last_used_at DESC, n.created_at DESC, n.id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: lookup exploration nodes: %w", err)
	}
	defer rows.Close()

	var out []ExplorationNode
	for rows.Next() {
		node, err := scanExplorationNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// LatestExplorationSession 返回 (workspace_id, session_id) 下最近更新的会话；
// ok=false 表示该会话尚无登记。纯读，reader 可用。
func (s *sqliteStore) LatestExplorationSession(ctx context.Context, workspaceID, sessionID string) (ExplorationSession, bool, error) {
	if s == nil || s.db == nil {
		return ExplorationSession{}, false, errors.New("knowledge: latest exploration session: store is not open")
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(sessionID) == "" {
		return ExplorationSession{}, false, errors.New("knowledge: latest exploration session: workspace_id and session_id are required")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, session_id, COALESCE(task_id, ''), created_at, updated_at
		FROM exploration_sessions
		WHERE workspace_id = ? AND session_id = ?
		ORDER BY updated_at DESC, rowid DESC
		LIMIT 1`, workspaceID, sessionID)

	var sess ExplorationSession
	var createdMS, updatedMS int64
	if err := row.Scan(&sess.ID, &sess.WorkspaceID, &sess.SessionID, &sess.TaskID, &createdMS, &updatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExplorationSession{}, false, nil
		}
		return ExplorationSession{}, false, fmt.Errorf("knowledge: latest exploration session: %w", err)
	}
	sess.CreatedAt = timeFromUnixMillis(createdMS)
	sess.UpdatedAt = timeFromUnixMillis(updatedMS)
	return sess, true, nil
}

// scanExplorationNode 扫描一行 exploration_nodes（与 LookupExplorationNodes 的
// SELECT 列序一致）。
func scanExplorationNode(row rowScanner) (ExplorationNode, error) {
	var (
		node      ExplorationNode
		nodeType  string
		createdMS int64
		usedMS    int64
	)
	if err := row.Scan(&node.ID, &node.ExplorationID, &nodeType, &node.Target,
		&node.FileID, &node.SymbolID, &node.Summary, &node.Confidence,
		&node.KnowledgeVersion, &createdMS, &usedMS, &node.UseCount); err != nil {
		return ExplorationNode{}, fmt.Errorf("knowledge: scan exploration node: %w", err)
	}
	node.NodeType = NodeType(nodeType)
	node.CreatedAt = timeFromUnixMillis(createdMS)
	node.LastUsedAt = timeFromUnixMillis(usedMS)
	return node, nil
}
