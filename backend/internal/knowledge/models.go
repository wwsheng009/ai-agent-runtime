package knowledge

import "time"

// SymbolKind 是归一化的、与语言无关的符号分类。
//
// 各语言生产者把自身节点类型映射到这个闭集；工具面与上下文打包只依赖这些值，
// 从而保证 `code.*` 跨语言可用（ADR-0007）。
type SymbolKind string

const (
	SymbolUnknown   SymbolKind = "unknown"
	SymbolPackage   SymbolKind = "package"
	SymbolModule    SymbolKind = "module"
	SymbolNamespace SymbolKind = "namespace"
	SymbolType      SymbolKind = "type"
	SymbolInterface SymbolKind = "interface"
	SymbolEnum      SymbolKind = "enum"
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolField     SymbolKind = "field"
	SymbolConstant  SymbolKind = "constant"
	SymbolVariable  SymbolKind = "variable"
)

// IndexState 是 files.index_state 的取值（04 §4.3）。
type IndexState string

const (
	IndexUnknown IndexState = "unknown"
	IndexLight   IndexState = "light"
	IndexDeep    IndexState = "deep"
	IndexStale   IndexState = "stale"
	IndexError   IndexState = "error"
)

// RefKind 是 refs.kind 的取值。
type RefKind string

const (
	RefReference RefKind = "reference"
	RefCall      RefKind = "call"
	RefImport    RefKind = "import"
	RefImplement RefKind = "implement"
)

// RefSource 是 refs.source 的取值（生产者标识，参与 confidence 计算）。
type RefSource string

const (
	SourceLSP        RefSource = "lsp"
	SourceTreeSitter RefSource = "tree-sitter"
	SourceHeuristic  RefSource = "heuristic"
	SourceFTS        RefSource = "fts"
	SourceBuiltin    RefSource = "regex_builtin"
	SourceRuntime    RefSource = "runtime_evidence"
)

// Position 是文件内的 1-based 行 / 0-based 列。
//
// 列始终是磁盘上的 UTF-8 码元；与 LSP（UTF-16）通信的生产者必须在边界处转换，
// 消费者永远看不到 UTF-16 列（ADR-0006）。
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Range 是半开区间源码跨度，可直接用于构建 snippet。
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Workspace 是 workspaces 表的一行；id 复用 workspaceregistry，不新造主键。
type Workspace struct {
	ID        string    `json:"id"`
	RootPath  string    `json:"root_path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FileRecord 是 files 表的一行。
//
// content_hash 是增量索引的唯一判据：哈希不变则跳过解析；mtime/size 只用于
// 廉价预筛（先看 mtime，再决定是否读文件算哈希）。
type FileRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// Path 是 workspace 相对路径，统一使用 '/' 分隔。
	Path        string     `json:"path"`
	Language    string     `json:"language"`
	Size        int64      `json:"size"`
	MTimeNS     int64      `json:"mtime_ns"`
	ContentHash string     `json:"content_hash"`
	IsTest      bool       `json:"is_test"`
	IsGenerated bool       `json:"is_generated"`
	IndexState  IndexState `json:"index_state"`
	IndexedAt   time.Time  `json:"indexed_at"`
}

// Symbol 是 symbols 表的一行。
//
// ID 是 StableKeyID(kind=SymbolKind, path, qualified_name) 的摘要形式；
// StableKey 承载可读身份，ID 承载定长主键。
type Symbol struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	FileID        string     `json:"file_id"`
	StableKey     string     `json:"stable_key"`
	Name          string     `json:"name"`
	QualifiedName string     `json:"qualified_name"`
	Kind          SymbolKind `json:"kind"`
	Language      string     `json:"language"`
	// OwnerSymbolID 指向所属符号（方法 → 类型）；空表示顶层符号。
	OwnerSymbolID string `json:"owner_symbol_id,omitempty"`
	// Signature 是单行渲染的签名，由生产者负责截断。
	Signature     string `json:"signature,omitempty"`
	SignatureHash string `json:"signature_hash,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
	Range         Range  `json:"range"`
	IsExported    bool   `json:"is_exported"`
	IsTest        bool   `json:"is_test"`
	// DeletedAt 非零表示已标记删除（保留 30 天或 N 个版本后 GC，§4.4）。
	DeletedAt int64 `json:"deleted_at,omitempty"`
}

// Reference 是 refs 表的一行：一条使用点记录。
type Reference struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// FromSymbolID 是引用所在符号；空表示文件级（如 import）。
	FromSymbolID string `json:"from_symbol_id,omitempty"`
	// ToSymbolID 是解析到的目标符号；空表示仅凭名字猜测存在同名声明。
	ToSymbolID string `json:"to_symbol_id,omitempty"`
	// ToSymbolName 是使用点字面写下的目标名。
	//
	// 它与 ToSymbolID 是两份独立事实：目标是 stdlib / 第三方 / 尚未索引的符号时
	// ToSymbolID 为空，此时名字是唯一的线索（04 §4.3 的 refs 缺少该列，见
	// migrations/0001_init.sql 的差异说明）。
	ToSymbolName    string    `json:"to_symbol_name,omitempty"`
	ToSymbolVersion int       `json:"to_symbol_version,omitempty"`
	Kind            RefKind   `json:"kind"`
	FileID          string    `json:"file_id"`
	Line            int       `json:"line"`
	Col             int       `json:"col"`
	Snippet         string    `json:"snippet,omitempty"`
	Confidence      float64   `json:"confidence"`
	Source          RefSource `json:"source"`
}

// InvalidationReasonAdapterConflict 是 04 §4.4 的歧义规则要求的失效事件原因：
// 同一 workspace 内出现 stable_key 冲突 = adapter 缺陷，必须可观测。
const InvalidationReasonAdapterConflict = "adapter_conflict"

// InvalidationEvent 是 invalidation_events 表的一行：索引失效的可解释记录。
type InvalidationEvent struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Reason      string    `json:"reason"`
	ScopeJSON   string    `json:"scope_json,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
