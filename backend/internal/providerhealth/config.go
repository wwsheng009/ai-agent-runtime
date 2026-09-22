package providerhealth

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// SpecFromConfig 把顶层 circuit_breaker 配置块翻译成熔断策略。
//
// 该块在配置里的字段可能是已展开的数字（failure_rate: 0.5），也可能是环境变量
// 未设置而残留的占位符；agentconfig.ConfigScalar 统一按标量原文接住，这里逐个
// 按目标类型解析。任何缺失、非法或不可解析的值都回落缺省——熔断策略是可选调优，
// 配置写错只应退回保守默认，不应让路由整体不可用。
//
// 依赖方向刻意是 providerhealth → agentconfig：反向会让 agentconfig 依赖 llm
// （providerhealth 经 ClassifyFailure 依赖 llm），从而成环。
//
// cfg 为 nil（宿主未声明该块）等价于全缺省。
func SpecFromConfig(cfg *agentconfig.CircuitBreakerConfig) Spec {
	spec := DefaultSpec()
	if cfg == nil {
		return spec
	}
	spec.FailureThreshold = cfg.FailureThreshold.ParseInt(spec.FailureThreshold)
	spec.FailureRate = cfg.FailureRate.ParseFloat(spec.FailureRate)
	spec.SampleThreshold = cfg.SampleThreshold.ParseInt(spec.SampleThreshold)
	spec.WindowDuration = cfg.WindowDuration.ParseDuration(spec.WindowDuration)
	spec.OpenTimeout = cfg.OpenTimeout.ParseDuration(spec.OpenTimeout)
	spec.HalfOpenMaxCalls = cfg.HalfOpenMaxCalls.ParseInt(spec.HalfOpenMaxCalls)
	return spec.Normalized()
}
