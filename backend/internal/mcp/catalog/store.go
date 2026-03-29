package catalog

import "github.com/ai-gateway/ai-agent-runtime/internal/skill"

// Snapshot 表示一份可持久化的 catalog 快照。
type Snapshot struct {
	Tools []skill.ToolInfo `json:"tools"`
	Stats RefreshStats     `json:"stats"`
}

// SnapshotStore 是可选的 catalog 快照持久化接口。
// 当前默认仍使用内存目录；该接口用于后续接入 FTS/数据库实现。
type SnapshotStore interface {
	LoadCatalogSnapshot() (*Snapshot, error)
	SaveCatalogSnapshot(snapshot Snapshot) error
}

// SearchStore 是可选的持久化查询接口。
// 当 store 同时实现该接口时，Catalog.Search 会优先使用持久化索引查询。
type SearchStore interface {
	SearchCatalogTools(query string, limit int) ([]skill.ToolInfo, error)
}
