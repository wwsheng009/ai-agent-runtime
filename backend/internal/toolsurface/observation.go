// 观测 payload 的共享构建点（docs/tools/unified-tool-registry-plan.md §2.2-A/§2.2-C）。
//
// 两个宿主（runtime-server 的会话级 GET /api/runtime/tools、aicli 的 TUI /tools）
// 都从这里取 groups[]/filtered[]/每组工具的 metadata{}，保证"端点显示"与"TUI 显示"
// 是同一份构建结果的两种呈现，而不是两套各自维护的口径（10 vs 22 缺口的根因）。
//
// 约束：
//   - 分类事实只有两个来源：来源账本走 Ledger.Classify（surface_source，单一实现点），
//     身份/分组/暴露层走 internal/toolregistry（T6：不新增第二套分类判定）；
//   - 只改观测副本：Enrich 在 metadata 副本上写入，绝不回写请求面定义
//     （agent/loop.go 的 token 估算依赖请求定义的稳定性）；
//   - JSON 契约可加不可改：tools/count/source/session_id 语义不变，只新增字段。
package toolsurface

import (
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 观测 metadata 键（§2.2-A）。这些键只出现在观测 payload 里。
const (
	MetadataSurfaceSource = "surface_source"
	MetadataGroup         = "group"
	MetadataExposure      = "exposure"
	MetadataEssential     = "essential"
	MetadataAliasOf       = "alias_of"
)

// FilterReason 是 filtered[] 的裁剪原因取值域（§2.2-A）。
type FilterReason string

const (
	// FilterProfileDeny：会话/宿主 profile 的 allowlist/denylist 把工具裁掉。
	FilterProfileDeny FilterReason = "profile_deny"
	// FilterGroupDisabled：分组被会话级 disabled_groups 关闭（P3 落地，见 buildGroups）。
	FilterGroupDisabled FilterReason = "group_disabled"
	// FilterMCPDisabled：会话级 MCP 停用（session.MCPDisabled）摘掉了该 MCP 来源。
	FilterMCPDisabled FilterReason = "mcp_disabled"
	// FilterHostReadOnly：宿主以只读方式挂载，写类工具不进入请求面。
	FilterHostReadOnly FilterReason = "host_read_only"
)

// FilteredTool 是一条被裁剪的工具/来源及其原因（§2.2-A filtered[]）。
//
// Name 对 MCP 来源用 "mcp:<server>" 口径（与 Ledger.Classify 的 surface_source
// 取值一致），对具体工具用工具名。
type FilteredTool struct {
	Name   string       `json:"name"`
	Reason FilterReason `json:"reason"`
}

// Group 是一个工具分组的目录项（§2.2-A groups[]）。
//
// ToolCount 只统计**目录里实际存在**且能归到该组的工具（未登记的外部工具不虚构
// 分组，也不计入任何组）；Enabled 在 P0 恒为 true——disabled_groups 落地后
// （P3）由会话裁剪事实驱动，见 buildGroups。
type Group struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Protected bool   `json:"protected"`
	Enabled   bool   `json:"enabled"`
	ToolCount int    `json:"tool_count"`
}

// Observation 是工具面观测的共享 payload（runtime-server 响应体与 TUI 渲染共用）。
type Observation struct {
	Tools    []runtimetypes.ToolDefinition `json:"tools"`
	Groups   []Group                       `json:"groups"`
	Filtered []FilteredTool                `json:"filtered"`
}

// Observe 把调用方收集的候选目录装配成观测 payload：先走 Build（去重 + 来源账本 +
// 排序，单一分类实现点），再经 Enrich 补分组/元数据。
//
// ledger 必须是调用方的真实来源账本（broker 等），否则 surface_source 会把编排面
// 误标成 core。空 catalog 返回 ok=false，调用方据此保留各自的旧回退口径。
func Observe(catalog []runtimetypes.ToolDefinition, ledger Ledger, filtered []FilteredTool) (Observation, bool) {
	tools, ok := Build(catalog, ledger)
	if !ok {
		return Observation{}, false
	}
	return Enrich(tools, ledger, filtered), true
}

// Enrich 对**已经装配过**的目录（Build 的产物，或 actor 冻结面快照）补齐观测字段：
//
//   - surface_source：缺失时按 ledger.Classify 补（已带账本的定义原样保留，绝不覆盖）；
//   - group/exposure/essential/alias_of：按 toolregistry 现役名字解析（LookupLive），
//     未登记的外部工具（MCP 等）不加这些键——不猜、不套用退役名的类别；
//   - groups[]：toolregistry.Groups() 目录 + 本目录的组计数；
//   - filtered[]：调用方给的裁剪事实，规范化（trim/去重/按名排序）。
//
// 返回的新定义只在 metadata 副本上写入，输入切片与 map 不被修改。
func Enrich(tools []runtimetypes.ToolDefinition, ledger Ledger, filtered []FilteredTool) Observation {
	enriched := make([]runtimetypes.ToolDefinition, 0, len(tools))
	counts := make(map[string]int, len(tools))
	for _, def := range tools {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		metadata := cloneMetadata(def.Metadata)
		applyRegistryMetadata(metadata, name)
		if _, exists := metadata[MetadataSurfaceSource]; !exists {
			// 与 Build 共用 Classify 单一实现点：mcp_name → mcp:<server>，其余按
			// 能力名单/broker 账本，最后 default core。
			metadata[MetadataSurfaceSource] = string(ledger.Classify(runtimetypes.ToolDefinition{Name: name, Metadata: metadata}))
		}
		if group, ok := metadata[MetadataGroup].(string); ok && group != "" {
			counts[group]++
		}
		enriched = append(enriched, runtimetypes.ToolDefinition{
			Name:        name,
			Description: def.Description,
			Parameters:  cloneMetadataParams(def.Parameters),
			Metadata:    metadata,
		})
	}
	sort.SliceStable(enriched, func(i, j int) bool {
		return strings.TrimSpace(enriched[i].Name) < strings.TrimSpace(enriched[j].Name)
	})
	return Observation{
		Tools:    enriched,
		Groups:   buildGroups(counts, filtered),
		Filtered: normalizeFiltered(filtered),
	}
}

// applyRegistryMetadata 用 registry 的现役条目补分组事实（就地写 metadata 副本）。
func applyRegistryMetadata(metadata map[string]interface{}, name string) {
	entry, ok := toolregistry.LookupLive(name)
	if !ok {
		return
	}
	if entry.Group != "" {
		metadata[MetadataGroup] = string(entry.Group)
	}
	if len(entry.Exposure) > 0 {
		exposure := make([]string, 0, len(entry.Exposure))
		for _, item := range entry.Exposure {
			if exposureItem := strings.TrimSpace(string(item)); exposureItem != "" {
				exposure = append(exposure, exposureItem)
			}
		}
		if len(exposure) > 0 {
			metadata[MetadataExposure] = exposure
		}
	}
	metadata[MetadataEssential] = entry.Essential
	// alias_of 对现役登记的工具恒存在：规范名自身为空串，别名拼写写 canonical
	// （§2.2-A 的响应形状 `"alias_of": ""`，消费方不必区分"缺字段"与"空"）。
	// 未登记的外部工具（MCP 等）不写这个键——不猜它的归属。
	canonical := strings.TrimSpace(entry.Name)
	if canonical == "" || canonical == toolregistry.Normalize(name) {
		canonical = ""
	}
	metadata[MetadataAliasOf] = canonical
}

// buildGroups 用 registry 分组目录 + 本目录计数生成 groups[]。
//
// P0：Enabled 恒 true（disabled_groups 尚未落地，任何别的取值都是编造）。P3 接入
// 会话级 disabled_groups 时，这里只消费 filtered[] 里 reason=group_disabled 的
// 条目即可，无需新增判定点。
func buildGroups(counts map[string]int, filtered []FilteredTool) []Group {
	disabled := map[string]bool{}
	for _, item := range normalizeFiltered(filtered) {
		if item.Reason == FilterGroupDisabled {
			disabled[item.Name] = true
		}
	}
	catalog := toolregistry.Groups()
	groups := make([]Group, 0, len(catalog))
	for _, info := range catalog {
		id := string(info.ID)
		groups = append(groups, Group{
			ID:        id,
			Label:     info.Label,
			Protected: info.Protected,
			Enabled:   !disabled[id],
			ToolCount: counts[id],
		})
	}
	return groups
}

// normalizeFiltered 规范化裁剪明细：trim、丢空、按名去重、按名排序，返回非 nil 切片
// （JSON 里是 []，不是 null）。
func normalizeFiltered(filtered []FilteredTool) []FilteredTool {
	out := make([]FilteredTool, 0, len(filtered))
	seen := make(map[string]bool, len(filtered))
	for _, item := range filtered {
		name := strings.TrimSpace(item.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, FilteredTool{Name: name, Reason: item.Reason})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
