package catalog

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// ToolSource 描述 catalog 刷新所需的最小工具来源。
type ToolSource interface {
	ListTools() []skill.ToolInfo
}

// Gateway 封装 MCP tools 的目录刷新与搜索。
type Gateway struct {
	source  ToolSource
	catalog *Catalog
}

// NewGateway 创建一个新的 catalog gateway。
func NewGateway(source ToolSource, catalog *Catalog) *Gateway {
	if catalog == nil {
		catalog = New()
	}
	gateway := &Gateway{
		source:  source,
		catalog: catalog,
	}
	gateway.Refresh()
	return gateway
}

// NewGatewayWithStore 创建一个带可选 snapshot store 的 catalog gateway。
func NewGatewayWithStore(source ToolSource, store SnapshotStore) *Gateway {
	return NewGateway(source, NewWithStore(store))
}

// Refresh 从 source 同步最新工具目录。
func (g *Gateway) Refresh() RefreshStats {
	if g == nil || g.source == nil || g.catalog == nil {
		return RefreshStats{}
	}
	return g.catalog.Refresh(g.source.ListTools())
}

// Search 返回最相关工具。
func (g *Gateway) Search(query string, limit int) []skill.ToolInfo {
	if g == nil || g.catalog == nil {
		return nil
	}
	return g.catalog.Search(query, limit)
}

// Catalog 返回底层 catalog。
func (g *Gateway) Catalog() *Catalog {
	if g == nil {
		return nil
	}
	return g.catalog
}

// RefreshStats 返回最近一次 refresh 的统计。
func (g *Gateway) RefreshStats() RefreshStats {
	if g == nil || g.catalog == nil {
		return RefreshStats{}
	}
	return g.catalog.RefreshStats()
}

// Close 关闭底层 catalog 持有的可选持久化资源。
func (g *Gateway) Close() error {
	if g == nil || g.catalog == nil {
		return nil
	}
	return g.catalog.Close()
}
