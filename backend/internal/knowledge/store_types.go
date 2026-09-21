package knowledge

// 查询对象与命中记录：Layer 与 Store 之间交换的 DTO。
//
// Store 契约（store.go）刻意保持窄面：Layer 把面向 agent 的调用翻译成这些
// DTO，具体实现独占全部 SQL。DTO 不含 SQL 概念，便于将来增加第二个后端
// （远程服务）而不触碰 Layer。
//
// 所有 Limit 遵循同一约定：Limit <= 0 表示"使用 store 默认值"，绝不是"无限"。

// SymbolQuery 选择索引中的符号定义。
//
// Name 在 Exact 为真时精确匹配，否则按大小写敏感的子串匹配，
// 使 FindSymbols("Open") 也能找到 OpenFile 这类辅助函数。
// 过滤条件之间是 AND；零值选择全部（受 store 默认上限约束）。
type SymbolQuery struct {
	// Name 是符号名（或子串）。
	Name string `json:"name,omitempty"`
	// Kind 过滤归一化分类；空表示任意。
	Kind SymbolKind `json:"kind,omitempty"`
	// Lang 过滤语言标签；空表示任意。
	Lang string `json:"lang,omitempty"`
	// PathPrefix 限定 workspace 相对路径前缀。
	PathPrefix string `json:"path_prefix,omitempty"`
	// Exact 把 Name 从子串切换为精确匹配。
	Exact bool `json:"exact,omitempty"`
	// Limit 限制返回行数。
	Limit int `json:"limit,omitempty"`
}

// RefQuery 选择引用（调用点、导入、类型使用）。
//
// 目标既可按符号名也可按符号 id 指定；两者同时给出表示
// "这个 id，但名字仍须匹配"，用于在增量重建后保持结果稳定。
type RefQuery struct {
	// ToSymbolID 是被引用符号的主键（已知时）。
	ToSymbolID string `json:"to_symbol_id,omitempty"`
	// ToSymbolName 是被引用符号名（未知 id 时使用，或作为一致性校验）。
	ToSymbolName string `json:"to_symbol_name,omitempty"`
	// FromSymbolID 限定引用所在符号。
	FromSymbolID string `json:"from_symbol_id,omitempty"`
	// Kind 过滤引用种类；空表示任意。
	Kind RefKind `json:"kind,omitempty"`
	// PathPrefix 限定 workspace 相对路径前缀。
	PathPrefix string `json:"path_prefix,omitempty"`
	// Limit 限制返回行数。
	Limit int `json:"limit,omitempty"`
}

// SearchQuery 是 symbols_fts 的全文查询。
type SearchQuery struct {
	// Text 是原始查询串。tokenize 由 store 决定；调用方不得假设支持任何查询语言。
	Text string `json:"text,omitempty"`
	// Lang 过滤语言标签；空表示任意。
	Lang string `json:"lang,omitempty"`
	// PathPrefix 限定 workspace 相对路径前缀。
	PathPrefix string `json:"path_prefix,omitempty"`
	// Limit 限制返回条数。
	Limit int `json:"limit,omitempty"`
}

// SearchHit 是一条全文命中，按相关性由 store 排好序。
//
// Score 只在同一结果集内具备相对意义，不可跨查询或跨实现比较。
type SearchHit struct {
	SymbolID      string     `json:"symbol_id"`
	Name          string     `json:"name"`
	QualifiedName string     `json:"qualified_name,omitempty"`
	Kind          SymbolKind `json:"kind,omitempty"`
	Path          string     `json:"path"`
	Language      string     `json:"language,omitempty"`
	Line          int        `json:"line,omitempty"`
	Signature     string     `json:"signature,omitempty"`
	Score         float64    `json:"score,omitempty"`
}

// Stats 是状态面（CLI 状态栏 / runtime-server /knowledge）渲染的轻量汇总。
type Stats struct {
	// SchemaVersion 是 schema_migrations 中的最高版本。
	SchemaVersion int   `json:"schema_version"`
	Files         int64 `json:"files"`
	Symbols       int64 `json:"symbols"`
	Refs          int64 `json:"refs"`
	// IndexedAt 是最近一次成功写事务的 unix 毫秒；0 表示尚无索引。
	IndexedAt int64 `json:"indexed_at"`
	// Truncated 表示预算上限提前终止了索引；工具面据此标记结果可能陈旧（ADR-0004）。
	Truncated bool `json:"truncated"`
}
