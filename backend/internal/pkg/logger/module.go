package logger

// Module 定义日志模块分类
// 每个模块可以有独立的日志级别和输出配置
type Module string

const (
	// ModuleGateway 网关核心模块
	// 包括路由、中间件、请求处理等核心逻辑
	ModuleGateway Module = "gateway"

	// ModuleProxy 代理模块
	// 处理与上游 AI 服务的通信
	ModuleProxy Module = "proxy"

	// ModuleTransformer 协议转换模块
	// OpenAI/Anthropic/Gemini 协议转换
	ModuleTransformer Module = "transformer"

	// ModuleProvider 供应商管理模块
	// 供应商选择、负载均衡、健康检查
	ModuleProvider Module = "provider"

	// ModuleAuth 认证授权模块
	// API Key 验证、JWT 处理等
	ModuleAuth Module = "auth"

	// ModuleRateLimit 限流模块
	// 请求限流、配额管理
	ModuleRateLimit Module = "ratelimit"

	// ModuleTracing 链路追踪模块
	// 分布式追踪、性能监控
	ModuleTracing Module = "tracing"

	// ModuleDatabase 数据库模块
	// GORM、Redis 等数据层操作
	ModuleDatabase Module = "database"

	// ModuleConfig 配置模块
	// 配置加载、验证
	ModuleConfig Module = "config"

	// ModuleRouter 路由模块
	// 请求路由、匹配
	ModuleRouter Module = "router"

	// ModuleMiddleware 中间件模块
	// 通用中间件逻辑
	ModuleMiddleware Module = "middleware"

	// ModuleHandler 请求处理模块
	// HTTP handlers
	ModuleHandler Module = "handler"

	// ModuleAdmin 管理后台模块
	// 管理接口、配置管理
	ModuleAdmin Module = "admin"

	// ModuleMCP MCP 协议模块
	// Model Context Protocol 支持
	ModuleMCP Module = "mcp"

	// ModuleVendor 供应商适配模块
	// 供应商注册、映射
	ModuleVendor Module = "vendor"

	// ModuleMetrics 指标收集模块
	// Prometheus 指标、运行时指标
	ModuleMetrics Module = "metrics"

	// ModuleTruncation 上下文截断模块
	// 自动截断、重试
	ModuleTruncation Module = "truncation"

	// ModuleReplay 请求重放模块
	// 请求回放、调试
	ModuleReplay Module = "replay"

	// ModuleDiscovery API 发现模块
	// 协议检测、API 探测
	ModuleDiscovery Module = "discovery"

	// ModuleCapability 能力追踪模块
	// 供应商能力记录
	ModuleCapability Module = "capability"

	// ModuleTaskQueue 任务队列模块
	// 异步任务处理
	ModuleTaskQueue Module = "taskqueue"

	// ModulePprof pprof 性能分析模块
	// 性能分析端点
	ModulePprof Module = "pprof"

	// ModulePipeline Pipeline 模块
	// 事件驱动 Pipeline 处理
	ModulePipeline Module = "pipeline"
)

// ModuleInfo 模块信息
type ModuleInfo struct {
	Name        Module // 模块标识
	Description string // 模块描述
	Category    string // 模块分类（core, business, infra）
}

// PredefinedModules 预定义模块列表
var PredefinedModules = []ModuleInfo{
	{ModuleGateway, "网关核心模块", "core"},
	{ModuleProxy, "代理模块", "core"},
	{ModuleTransformer, "协议转换模块", "core"},
	{ModuleProvider, "供应商管理模块", "business"},
	{ModuleAuth, "认证授权模块", "business"},
	{ModuleRateLimit, "限流模块", "business"},
	{ModuleTracing, "链路追踪模块", "infra"},
	{ModuleDatabase, "数据库模块", "infra"},
	{ModuleConfig, "配置模块", "infra"},
	{ModuleRouter, "路由模块", "core"},
	{ModuleMiddleware, "中间件模块", "core"},
	{ModuleHandler, "请求处理模块", "core"},
	{ModuleAdmin, "管理后台模块", "business"},
	{ModuleMCP, "MCP 协议模块", "business"},
	{ModuleVendor, "供应商适配模块", "business"},
	{ModuleMetrics, "指标收集模块", "infra"},
	{ModuleTruncation, "上下文截断模块", "business"},
	{ModuleReplay, "请求重放模块", "business"},
	{ModuleDiscovery, "API 发现模块", "business"},
	{ModuleCapability, "能力追踪模块", "business"},
	{ModuleTaskQueue, "任务队列模块", "infra"},
	{ModulePprof, "pprof 性能分析模块", "infra"},
	{ModulePipeline, "Pipeline 模块", "core"},
}

// GetModuleInfo 获取模块信息
func GetModuleInfo(m Module) ModuleInfo {
	for _, info := range PredefinedModules {
		if info.Name == m {
			return info
		}
	}
	return ModuleInfo{m, "未知模块", "unknown"}
}

// AllModules 返回所有模块列表
func AllModules() []Module {
	modules := make([]Module, len(PredefinedModules))
	for i, info := range PredefinedModules {
		modules[i] = info.Name
	}
	return modules
}
