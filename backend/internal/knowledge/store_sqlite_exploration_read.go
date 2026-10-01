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
//   - 任务工作集：给出 TaskID（精确匹配 sessions.task_id）；Target 非空时按
//     n.target 精确过滤；
//   - 会话级工作集：给出 SessionID（限定该会话下 task_id 为空的节点，与 W2
//     写入侧"宿主无任务语义"的回退口径一致）；Target 非空时同样精确过滤；
//   - 跨任务复用：TaskID / SessionID 都为空，Target 作为加权检索键参与匹配
//     （exact > prefix > contains，支持限定名 / 路径符号归一化，规则见
//     explorationLookupKeys），不再要求精确相等；Target 为空时不按目标过滤。
//
// 排序稳定：跨任务加权路径先按匹配权重升序（exact=0 / prefix=1 / contains=2），
// 其后沿用其余路径的口径——last_used_at 非空的在前、按时间倒序；未使用过的按
// created_at 倒序；同刻用 id 兜底。Limit <= 0 时使用 defaultQueryLimit，
// 加权路径同样受 Limit 约束（SQL LIMIT 在 ORDER BY 之后生效）。
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

	taskID := strings.TrimSpace(q.TaskID)
	sessionID := strings.TrimSpace(q.SessionID)
	crossTask := taskID == "" && sessionID == ""

	where := []string{"s.workspace_id = ?"}
	args := []any{q.WorkspaceID}
	if taskID != "" {
		where = append(where, "s.task_id = ?")
		args = append(args, taskID)
	}
	if sessionID != "" {
		// 会话级工作集只覆盖无任务锚点的行：任务行必须走 TaskID 路径，
		// 避免会话级检索把任务作用域的数据带进更宽松的阈值一侧。
		where = append(where, "s.session_id = ?", "COALESCE(s.task_id, '') = ''")
		args = append(args, sessionID)
	}

	orderBy := "(n.last_used_at IS NULL) ASC, n.last_used_at DESC, n.created_at DESC, n.id ASC"
	var rankArgs []any
	if target := strings.TrimSpace(q.Target); target != "" {
		if !crossTask {
			// 任务/会话路径语义不变：Target 仍是精确过滤（保持既有索引形状）。
			where = append(where, "n.target = ?")
			args = append(args, target)
		} else {
			keys := explorationLookupKeys(target)
			if len(keys) == 0 {
				// 归一化后没有可匹配的键：显式无候选，不放大成全量返回。
				return nil, nil
			}
			matchExpr := `LOWER(REPLACE(n.target, '\', '/'))`
			contains := make([]string, 0, len(keys))
			for _, key := range keys {
				contains = append(contains, matchExpr+` LIKE LOWER(?) ESCAPE '\'`)
				args = append(args, "%"+escapeLike(key)+"%")
			}
			where = append(where, "("+strings.Join(contains, " OR ")+")")

			rank := strings.Builder{}
			rank.WriteString("CASE")
			for _, key := range keys {
				rank.WriteString(" WHEN " + matchExpr + ` = LOWER(?) THEN 0`)
				rankArgs = append(rankArgs, key)
			}
			for _, key := range keys {
				rank.WriteString(" WHEN " + matchExpr + ` LIKE LOWER(?) ESCAPE '\' THEN 1`)
				rankArgs = append(rankArgs, escapeLike(key)+"%")
			}
			rank.WriteString(" ELSE 2 END ASC")
			orderBy = rank.String() + ", " + orderBy
		}
	}
	if q.Type != "" {
		where = append(where, "n.node_type = ?")
		args = append(args, string(q.Type))
	}
	// 占位符顺序必须与 SQL 文本一致：WHERE 参数 → ORDER BY CASE 参数 → LIMIT。
	args = append(args, rankArgs...)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.exploration_id, n.node_type, n.target,
		       COALESCE(n.file_id, ''), COALESCE(n.symbol_id, ''), COALESCE(n.summary, ''),
		       n.confidence, n.knowledge_version, n.created_at,
		       COALESCE(n.last_used_at, 0), n.use_count
		FROM exploration_nodes n
		JOIN exploration_sessions s ON s.id = n.exploration_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY `+orderBy+`
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

// explorationLookupKeys 把跨任务检索键归一化为参与匹配的键集合（去重、保序）。
//
// 归一化规则是跨任务加权检索的稳定契约（与 code_search 的限定名口径同族）：
//
//  1. 基础归一化：去首尾空白、'\' → '/'、去掉前导 "./" 与 "/"
//     （复用 normalizeRelPath 的路径口径）；
//  2. 形式识别（决定"末段"）：
//     - 路径符号 path#symbol（含 '#'）：键 = 全名 + '#' 前路径 + '#' 后符号段
//     （pkg/a.go#Foo → pkg/a.go#Foo / pkg/a.go / Foo）；
//     - 路径（不含 '#'、含 '/'）：键 = 全名 + 末段 basename
//     （internal/knowledge/planner.go → 全名 / planner.go）；
//     - 限定名（不含 '#' 与 '/'、含 '.'）：键 = 全名 + 末段点分名
//     （knowledge.Plan → knowledge.Plan / Plan；pkg.Type.Method → 全名 / Method）；
//     末段命中常见代码文件扩展名时不提取（planner.go 不产生 "go" 键）；
//     - 其余（裸名）：键 = 全名（PlanInput）。
//     末段一律在首个空白处截断：自然语言查询把符号名嵌在句中时
//     （"请检查 knowledge.Plan 的复用" → 末段 Plan）仍能命中。
//
// 匹配时每个键独立判定 exact（目标与键相等）> prefix（目标以键为前缀）>
// contains（目标包含键），节点取所有键中的最小权重；未命中任何键的节点不返回。
// 大小写两侧统一 LOWER()（SQLite ASCII 折叠；CJK 无大小写不受影响）。
func explorationLookupKeys(raw string) []string {
	normalized := normalizeRelPath(raw)
	if normalized == "" {
		return nil
	}
	keys := []string{normalized}
	seen := map[string]bool{normalized: true}
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	if hash := strings.LastIndex(normalized, "#"); hash >= 0 && hash < len(normalized)-1 {
		// 路径符号：路径部分命中文件节点，符号段命中符号节点。
		add(normalized[:hash])
		add(lookupTailField(normalized[hash+1:]))
		return keys
	}
	if slash := strings.LastIndex(normalized, "/"); slash >= 0 && slash < len(normalized)-1 {
		add(lookupTailField(normalized[slash+1:]))
		return keys
	}
	if dot := strings.LastIndex(normalized, "."); dot > 0 && dot < len(normalized)-1 {
		tail := lookupTailField(normalized[dot+1:])
		if tail != "" && !explorationFileExtensionTail(tail) {
			add(tail)
		}
	}
	return keys
}

// lookupTailField 取末段的首个空白分隔片段：自然语言查询常把符号名嵌在句中
// （如 "请检查 knowledge.Plan 的复用"），截断后再做加权匹配。
func lookupTailField(tail string) string {
	if fields := strings.Fields(tail); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// explorationFileExtensionTail 报告点分末段是否更像代码文件扩展名（如
// planner.go 的 "go"）：是则不做限定名尾段提取，避免把扩展名当符号名放大噪声。
// 集合故意保守，只覆盖常见代码文件；未列出的扩展名只影响尾段召回，不影响全名匹配。
func explorationFileExtensionTail(tail string) bool {
	switch strings.ToLower(tail) {
	case "go", "ts", "tsx", "js", "jsx", "mjs", "cjs",
		"py", "java", "kt", "rs", "c", "h", "cc", "cpp", "hpp", "cs",
		"rb", "php", "swift", "scala", "sh", "ps1", "sql",
		"md", "json", "yaml", "yml", "toml", "xml", "html", "css",
		"vue", "svelte":
		return true
	default:
		return false
	}
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
