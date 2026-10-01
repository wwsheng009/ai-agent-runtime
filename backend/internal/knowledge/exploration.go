package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 探索记忆（exploration memory）的 DTO 与枚举（06 §4 Phase 2 W1 / S1）。
//
// 三张表已在 0001_init.sql 中冻结，本文件只把行结构、取值闭集与派生规则
// 变成包内契约：写入路径（store_sqlite_exploration.go）与读取路径
// （store_sqlite_exploration_read.go）都依赖这里，具体 SQL 不出 store。
//
// 隐私边界：Target 允许是文件路径或符号名；调用方不得写入查询明文中的
// 敏感片段，摘要统一走既有 shadow 口径的哈希（见 exploration_recorder.go，W2）。

// NodeType 是 exploration_nodes.node_type 的取值闭集。
type NodeType string

const (
	// NodeTypeFile 指向一个文件（target 为 workspace 相对路径）。
	NodeTypeFile NodeType = "file"
	// NodeTypeSymbol 指向一个符号（target 为限定名或 stable_key）。
	NodeTypeSymbol NodeType = "symbol"
	// NodeTypeQuery 是一次探索查询（target 为查询摘要，不落明文）。
	NodeTypeQuery NodeType = "query"
	// NodeTypeAnswer 是一条可复用结论。
	NodeTypeAnswer NodeType = "answer"
)

// Valid 报告取值是否在闭集内；零值不是合法节点类型。
func (t NodeType) Valid() bool {
	switch t {
	case NodeTypeFile, NodeTypeSymbol, NodeTypeQuery, NodeTypeAnswer:
		return true
	default:
		return false
	}
}

// ParseNodeType 把外部输入解析为合法 NodeType；未知输入显式失败，
// 而不是静默降级成某种"足够好"的类型。
func ParseNodeType(raw string) (NodeType, error) {
	t := NodeType(strings.ToLower(strings.TrimSpace(raw)))
	if !t.Valid() {
		return "", fmt.Errorf("knowledge: unknown node type %q", raw)
	}
	return t, nil
}

// EdgeType 是 exploration_edges.edge_type 的取值闭集。
type EdgeType string

const (
	// EdgeTypeCalls 表示 from 调用了 to。
	EdgeTypeCalls EdgeType = "calls"
	// EdgeTypeReferences 表示 from 引用了 to。
	EdgeTypeReferences EdgeType = "references"
	// EdgeTypeContains 表示 from 包含 to（文件包含符号）。
	EdgeTypeContains EdgeType = "contains"
	// EdgeTypeDerivedFrom 表示 from 由 to 推导而来。
	EdgeTypeDerivedFrom EdgeType = "derived_from"
)

// Valid 报告取值是否在闭集内；零值不是合法边类型。
func (t EdgeType) Valid() bool {
	switch t {
	case EdgeTypeCalls, EdgeTypeReferences, EdgeTypeContains, EdgeTypeDerivedFrom:
		return true
	default:
		return false
	}
}

// ParseEdgeType 把外部输入解析为合法 EdgeType；未知输入显式失败。
func ParseEdgeType(raw string) (EdgeType, error) {
	t := EdgeType(strings.ToLower(strings.TrimSpace(raw)))
	if !t.Valid() {
		return "", fmt.Errorf("knowledge: unknown edge type %q", raw)
	}
	return t, nil
}

// ExplorationSession 是 exploration_sessions 表的一行：一次任务探索的会话锚点。
//
// ID 由 (workspace_id, session_id, task_id) 派生，因此重复 Upsert 是幂等的；
// 同一 session 的不同 task 是不同行（task_id 为空时以空串参与派生）。
type ExplorationSession struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id"`
	TaskID      string    `json:"task_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Validate 校验会话的作用域字段：workspace 与 session 都必须存在，
// 否则节点无法被工作区隔离查询命中。task_id 允许为空（回退 session 语义）。
func (s ExplorationSession) Validate() error {
	if strings.TrimSpace(s.WorkspaceID) == "" {
		return errors.New("workspace_id is required")
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return errors.New("session_id is required")
	}
	return nil
}

// ExplorationNode 是 exploration_nodes 表的一行：一条探索记忆。
//
// ID 由 (exploration_id, target) 派生：同一会话内同一 target 重复写入
// 命中同一行（use_count 累加、last_used_at 刷新），而不是堆叠重复行。
//
// UseCount / LastUsedAt / CreatedAt 是写入侧维护的观察状态：
// AppendExplorationNode 忽略调用方传入的 UseCount，首次写入置 1，
// 后续同 target 写入累加；LastUsedAt 首次为 NULL（零值时间）。
type ExplorationNode struct {
	ID            string `json:"id"`
	ExplorationID string `json:"exploration_id"`
	// NodeType ∈ file|symbol|query|answer。
	NodeType NodeType `json:"node_type"`
	// Target 是节点指向的目标（文件相对路径 / 符号限定名 / 查询摘要）。
	Target string `json:"target"`
	// FileID / SymbolID 是可选的知识库行引用；空串写 NULL。
	FileID   string `json:"file_id,omitempty"`
	SymbolID string `json:"symbol_id,omitempty"`
	// Summary 是低成本摘要；空串写 NULL。
	Summary string `json:"summary,omitempty"`
	// Confidence 是 04 §4.4 的置信度；<=0 时写入侧落到 DDL 默认 0.5。
	Confidence float64 `json:"confidence"`
	// KnowledgeVersion 是产生本节点时的工作区知识版本，必填（非空）。
	// 版本不匹配的节点在复用侧直接不可用（W3）。
	KnowledgeVersion string    `json:"knowledge_version"`
	CreatedAt        time.Time `json:"created_at"`
	LastUsedAt       time.Time `json:"last_used_at,omitempty"`
	UseCount         int       `json:"use_count"`
}

// Validate 校验节点的作用域与必填字段。knowledge_version 是硬约束：
// 缺版本的行无法参与复用判定，宁可在这里失败也不让空版本进库。
func (n ExplorationNode) Validate() error {
	if strings.TrimSpace(n.ExplorationID) == "" {
		return errors.New("exploration_id is required")
	}
	if strings.TrimSpace(n.Target) == "" {
		return errors.New("target is required")
	}
	if !n.NodeType.Valid() {
		return fmt.Errorf("invalid node_type %q", n.NodeType)
	}
	if strings.TrimSpace(n.KnowledgeVersion) == "" {
		return errors.New("knowledge_version is required")
	}
	if n.Confidence < 0 || n.Confidence > 1 {
		return fmt.Errorf("confidence %v out of range [0,1]", n.Confidence)
	}
	return nil
}

// ExplorationEdge 是 exploration_edges 表的一行：节点之间的关系。
//
// ID 由 (exploration_id, from_node_id, to_node_id, edge_type) 派生，
// 重复追加同一关系是幂等的（权重取最新观察值）。
type ExplorationEdge struct {
	ID            string   `json:"id"`
	ExplorationID string   `json:"exploration_id"`
	FromNodeID    string   `json:"from_node_id"`
	ToNodeID      string   `json:"to_node_id"`
	EdgeType      EdgeType `json:"edge_type"`
	Weight        float64  `json:"weight"`
}

// Validate 校验边的作用域与必填字段。
func (e ExplorationEdge) Validate() error {
	if strings.TrimSpace(e.ExplorationID) == "" {
		return errors.New("exploration_id is required")
	}
	if strings.TrimSpace(e.FromNodeID) == "" {
		return errors.New("from_node_id is required")
	}
	if strings.TrimSpace(e.ToNodeID) == "" {
		return errors.New("to_node_id is required")
	}
	if !e.EdgeType.Valid() {
		return fmt.Errorf("invalid edge_type %q", e.EdgeType)
	}
	if e.Weight < 0 {
		return fmt.Errorf("weight %v must not be negative", e.Weight)
	}
	return nil
}

// ExplorationNodeQuery 选择探索记忆中的节点。
//
// WorkspaceID 必填（隔离边界）。三条读取路径（W3/W4 复用判定的读取入口）：
//   - 任务工作集：TaskID 非空，按 sessions.task_id 精确匹配；
//   - 会话级工作集：TaskID 为空、SessionID 非空，取该会话下 task_id 为空的
//     节点（W1 DTO 的"宿主无任务语义"回退口径，06 §4 Phase 2 W6）；
//   - 跨任务：两者都为空，Target 作为加权检索键（exact > prefix > contains，
//     限定名末段与全名都参与；归一化规则见 store_sqlite_exploration_read.go
//     的 explorationLookupKeys），Target 为空时不按目标过滤。
//
// 过滤条件之间是 AND。
type ExplorationNodeQuery struct {
	// WorkspaceID 限定工作区；必填。
	WorkspaceID string `json:"workspace_id"`
	// TaskID 限定同一任务的工作集；空表示不按任务过滤。
	TaskID string `json:"task_id,omitempty"`
	// SessionID 限定会话级工作集：该会话下 task_id 为空的节点（不含任务锚点
	// 行）。与 TaskID 同给时是 AND 语义。
	SessionID string `json:"session_id,omitempty"`
	// Target 是目标过滤 / 检索键：任务、会话路径按 n.target 精确匹配；
	// 跨任务路径作为加权检索键（exact > prefix > contains，支持限定名与
	// 路径符号的末段匹配）。空表示任意。
	Target string `json:"target,omitempty"`
	// Type 过滤节点类型；空表示任意。
	Type NodeType `json:"node_type,omitempty"`
	// Limit 限制返回行数；<=0 表示使用 store 默认上限（不是无限）。
	Limit int `json:"limit,omitempty"`
}

// ExplorationSessionID 返回 exploration_sessions 行的稳定主键。
func ExplorationSessionID(workspaceID, sessionID, taskID string) string {
	return "es_" + digest(workspaceID, sessionID, taskID)
}

// ExplorationNodeID 返回 exploration_nodes 行的稳定主键。
//
// 只取 (exploration_id, target)：与 idx_explore_nodes_target 的查询形状一致，
// 保证"同 target 幂等"是由主键而非额外去重查询实现的。
func ExplorationNodeID(explorationID, target string) string {
	return "en_" + digest(explorationID, target)
}

// ExplorationEdgeID 返回 exploration_edges 行的稳定主键。
func ExplorationEdgeID(explorationID, fromNodeID, toNodeID string, edgeType EdgeType) string {
	return "ee_" + digest(explorationID, fromNodeID, toNodeID, string(edgeType))
}
