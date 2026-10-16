package runtimeapi

import (
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// toolCatalogTool 是目录里的单个工具身份条目。
//
// 只投影注册表里**已有**的事实，不臆造字段：toolregistry.Entry 目前不含
// Description/Source（见 internal/toolregistry/registry.go 的 Entry 定义），
// 故此处也不输出——否则就成了第二份需要人工同步的名单（T6 不变量）。
type toolCatalogTool struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Group      string   `json:"group"`
	Exposure   []string `json:"exposure"`
	Essential  bool     `json:"essential"`
	WriteLike  bool     `json:"write_like"`
	ShellLike  bool     `json:"shell_like"`
	Delegation bool     `json:"delegation"`
	PlanWrite  bool     `json:"plan_write"`
}

// toolCatalogGroup 是组目录条目，tool_count 取自注册表的同一份条目集合。
type toolCatalogGroup struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Protected bool   `json:"protected"`
	ToolCount int    `json:"tool_count"`
}

// toolCatalogResponse 是无会话目录端点的响应体。
type toolCatalogResponse struct {
	Source string             `json:"source"`
	Count  int                `json:"count"`
	Tools  []toolCatalogTool  `json:"tools"`
	Groups []toolCatalogGroup `json:"groups"`
}

// ListBuiltinToolCatalog 返回**全量内置工具注册表 + 组目录**，不依赖任何会话。
//
// 诉求落点（docs/tools/unified-tool-registry-plan.md §2.2-B）：前端 profile 编辑器
// 要渲染"可关闭组"清单与工具身份说明，而会话级 /sessions/{id}/runtime/tools 受该
// 会话实时装配约束（面可能被裁剪，装配不可得时还会退回旧口径），不适合作为配置
// 渲染的来源；本端点给出与任何会话无关的稳定目录。
//
// 与 T6 不变量一致：身份/分组/暴露面全部取自 internal/toolregistry 单一真源，本函数
// 只做 JSON 投影，不自持任何名单；组目录同样来自 toolregistry.Groups()。
//
// 与 §2.2-A 的分工：这里**刻意不含** groups[].enabled 与 filtered[] —— 那两个字段
// 表达的是"某个会话被裁掉了什么"，属会话语义，只应出现在会话级端点上。
func (h *Handler) ListBuiltinToolCatalog(w http.ResponseWriter, r *http.Request) {
	entries := toolregistry.Entries()
	tools := make([]toolCatalogTool, 0, len(entries))
	groupCounts := make(map[toolregistry.Group]int, len(entries))
	for _, entry := range entries {
		groupCounts[entry.Group]++
		tools = append(tools, toolCatalogTool{
			Name:       entry.Name,
			Aliases:    entry.Aliases,
			Group:      string(entry.Group),
			Exposure:   toolExposureStrings(entry.Exposure),
			Essential:  entry.Essential,
			WriteLike:  entry.WriteLike,
			ShellLike:  entry.ShellLike,
			Delegation: entry.Delegation,
			PlanWrite:  entry.PlanWrite,
		})
	}

	catalog := toolregistry.Groups()
	groups := make([]toolCatalogGroup, 0, len(catalog))
	for _, group := range catalog {
		groups = append(groups, toolCatalogGroup{
			ID:        string(group.ID),
			Label:     group.Label,
			Protected: group.Protected,
			ToolCount: groupCounts[group.ID],
		})
	}

	h.writeJSON(w, http.StatusOK, toolCatalogResponse{
		Source: "toolregistry",
		Count:  len(tools),
		Tools:  tools,
		Groups: groups,
	})
}

// toolExposureStrings 把暴露面声明投影为字符串切片；空声明保持 nil，
// 便于 JSON 侧用 omitempty 之外的方式区分"未声明"与"声明为空"。
func toolExposureStrings(exposures []toolregistry.Exposure) []string {
	if len(exposures) == 0 {
		return nil
	}
	out := make([]string, 0, len(exposures))
	for _, exposure := range exposures {
		out = append(out, string(exposure))
	}
	return out
}
