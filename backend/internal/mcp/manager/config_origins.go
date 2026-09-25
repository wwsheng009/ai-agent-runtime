package manager

import "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"

// ConfigOriginReporter 暴露「分层加载」的来源信息（计划 §4.5 Step 1）。
//
// 与 StderrDiagnosticsProvider 一样属于可选能力，不进入 Manager 接口：
// 主线 *manager 实现它，mergedManager 做并集转发，Win7 兼容实现不提供，
// 调用方用类型断言按需探测。
//
// 用法：
//
//	if reporter, ok := mgr.(manager.ConfigOriginReporter); ok {
//		origins := reporter.MCPConfigOrigins()
//	}
//
// 本文件不带构建标签，两种构建下都存在，保证调用方可编译。
type ConfigOriginReporter interface {
	// MCPConfigOrigins 返回每个 server 的胜出来源与被遮蔽来源。
	MCPConfigOrigins() map[string]config.ServerOrigin
	// MCPConfigWarnings 返回分层加载期间的非致命告警（如低优先级文件损坏被跳过）。
	MCPConfigWarnings() []string
}

// LayeredConfigLoader 暴露「分层加载」入口的可选能力（计划 §4.5 Step 1）。
//
// 不进入 Manager 接口：主线 *manager 与 mergedManager 实现它；Win7 兼容的
// disabledManager 不实现，调用方按需探测并回退到单文件 LoadConfig。
type LayeredConfigLoader interface {
	// LoadConfigEffective 按 MCP 配置发现链分层加载；explicitPath 非空且不是
	// 约定路径时退化为精确加载该文件。
	LoadConfigEffective(explicitPath string) error
}
