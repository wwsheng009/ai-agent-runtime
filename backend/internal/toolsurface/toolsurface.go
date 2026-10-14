// Package toolsurface 是工具面装配与观测的单一实现点落位
// （docs/architecture/unified-tool-surface-registry.md §4.1/§4.2）。
//
// 职责边界（阶段 2 渐进收敛）：
//   - Surface：一次装配产出的工具定义列表 + 每工具来源标注（surface_source），
//     供 /runtime/tools 观测端点与未来宿主装配共用；
//   - Classify：来源分类（core / mcp:<server> / orchestration / capability），
//     单一分类函数，aicli 与 runtime-server 共用——两宿主各自的散装注册点
//     （chat_actor_host.go / session_agent_build.go）在后续提交里切换到本包。
//
// 依赖刻意最小：不依赖 internal/agent（调用方把目录与 broker 传进来），
// 避免与 agent 包形成环，也让 TUI 侧（持 skill.MCPManager + broker 同样两个输入）
// 可以无成本接入。
package toolsurface

import (
	"sort"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// brokerClassifier 判断一个工具名是否属于 broker 编排面
// （job/subagent/team/schedule/plan/question 家族）。与
// (*toolbroker.Broker).IsBrokerTool 同语义；用接口注入避免依赖 toolbroker 具体类型，
// 调用方直接传 *toolbroker.Broker 即可。
type brokerClassifier interface {
	IsBrokerTool(name string) bool
}

// SourceLabel 是工具在工具面里的来源分类（metadata.surface_source 的取值域）。
type SourceLabel string

const (
	SourceCore          SourceLabel = "core"
	SourceOrchestration SourceLabel = "orchestration"
	SourceCapability    SourceLabel = "capability"
	// SourceMCP 前缀形态为 "mcp:<server>"，无独立常量（按 mcp_name 拼装）。
)

// Ledger 携带来源账本分类所需的宿主输入。CapabilityTools 是「非 MCP 包装的
// capability 工具名集合」（如未来 goal 直配面）；nil = 宿主无此类面，goal 工具
// 经 aicli_goal 等 MCP 包装到达时按 mcp_name 归类。
type Ledger struct {
	Broker          brokerClassifier
	CapabilityTools map[string]bool
}

// Classify 按既有元数据与账本输入给工具打来源标签（单一分类函数）：
//   - "mcp:<server>"：定义带 mcp_name（含 goal capability 的 aicli_goal 包装）；
//   - "capability"：CapabilityTools 命中（非 MCP 包装的能力面）；
//   - "orchestration"：broker 编排面（job/subagent/team/schedule/plan/question）；
//   - "core"：其余内置。
//
// 优先级：mcp_name > capability 名单 > broker > core——包装层归属优先于能力层语义，
// 因为观测面回答的是"这个定义从哪个暴露层来"。
func (l Ledger) Classify(def runtimetypes.ToolDefinition) SourceLabel {
	if def.Metadata != nil {
		if mcpName, ok := def.Metadata["mcp_name"].(string); ok && strings.TrimSpace(mcpName) != "" {
			return SourceLabel("mcp:" + strings.TrimSpace(mcpName))
		}
	}
	if len(l.CapabilityTools) > 0 && l.CapabilityTools[strings.TrimSpace(def.Name)] {
		return SourceCapability
	}
	if l.Broker != nil && l.Broker.IsBrokerTool(def.Name) {
		return SourceOrchestration
	}
	return SourceCore
}

// Build 把调用方收集的候选目录装配成带来源账本的 Surface。
//
// catalog 由宿主从 agent.Agent.CollectToolCatalogDefinitions（冻结面前的完整候选
// 目录，内置/MCP/scheduler/broker 四源同采）取得；本函数不改写定义内容，只做：
//   - 幂等去重（按 name，保留首个）；
//   - surface_source 标注（不覆盖既有值——冻结面里可能已带账本）；
//   - 按 name 排序（观测面稳定输出）。
//
// 空 catalog 返回 ok=false：调用方据此回退各自的旧口径（runtimeapi 的 MCP+goal
// 回退 / aicli 的本地目录），不虚构空面。
func Build(catalog []runtimetypes.ToolDefinition, ledger Ledger) ([]runtimetypes.ToolDefinition, bool) {
	if len(catalog) == 0 {
		return nil, false
	}
	tools := make([]runtimetypes.ToolDefinition, 0, len(catalog))
	seen := make(map[string]bool, len(catalog))
	for _, def := range catalog {
		name := strings.TrimSpace(def.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		metadata := def.Metadata
		if metadata == nil {
			metadata = map[string]interface{}{}
		} else {
			metadata = cloneMetadata(metadata)
		}
		if _, exists := metadata["surface_source"]; !exists {
			metadata["surface_source"] = string(ledger.Classify(def))
		}
		tools = append(tools, runtimetypes.ToolDefinition{
			Name:        name,
			Description: def.Description,
			Parameters:  cloneMetadataParams(def.Parameters),
			Metadata:    metadata,
		})
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return strings.TrimSpace(tools[i].Name) < strings.TrimSpace(tools[j].Name)
	})
	return tools, true
}

// SummarizeSources 按目录名册生成「工具名 → surface_source」的请求级账本汇总
// （§15.4 修订）：定义本身保持原样（不推高 token 估算），账本随请求 metadata
// 可审计。与 Build 共用 Classify 单一分类实现点。
func SummarizeSources(catalog []runtimetypes.ToolDefinition, ledger Ledger) map[string]string {
	if len(catalog) == 0 {
		return nil
	}
	sources := make(map[string]string, len(catalog))
	for _, def := range catalog {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		if _, exists := def.Metadata["surface_source"]; exists {
			// 冻结面已带的账本优先（观测面与历史快照同值）。
			if label, ok := def.Metadata["surface_source"].(string); ok && label != "" {
				sources[name] = label
				continue
			}
		}
		sources[name] = string(ledger.Classify(def))
	}
	return sources
}

func cloneMetadata(input map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneMetadataParams(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	return cloneMetadata(input)
}
