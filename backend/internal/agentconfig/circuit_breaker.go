package agentconfig

import (
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigScalar 接住「可能是数字、时长或未解析占位符」的配置标量。
//
// 同一字段在不同来源的配置里形态不同，而加载路径是
// `expandEnvVars(...)` → `unmarshalYAML(...)`（config.go）：
//
//   - 仓库模板：failure_rate: ${CB_FAILURE_RATE:-0.5}
//   - 展开后：  failure_rate: 0.5（浮点标量）
//
// 声明为 string 会被 yaml.v3 判为类型不匹配（!!float → string），整份配置
// 加载失败；声明为 float64 又无法接住占位符未被展开的情况。因此统一按标量
// 原文接住，由 Parse* 决定取值还是回落缺省——配置层永远不该因为一个可选字段
// 的形状而拒绝启动。
type ConfigScalar string

func (s *ConfigScalar) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		*s = ""
		return nil
	}
	*s = ConfigScalar(strings.TrimSpace(value.Value))
	return nil
}

// Raw 返回标量原文（已去空白）。
func (s ConfigScalar) Raw() string { return strings.TrimSpace(string(s)) }

// ParseInt 解析正整数；空值、占位符残留或非法值一律回落 fallback。
func (s ConfigScalar) ParseInt(fallback int) int {
	raw := s.Raw()
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// ParseFloat 解析正浮点；空值、占位符残留或非法值一律回落 fallback。
func (s ConfigScalar) ParseFloat(fallback float64) float64 {
	raw := s.Raw()
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// ParseDuration 解析时长；同时接受 Go 时长写法（60s）与纯数字秒数（60）。
func (s ConfigScalar) ParseDuration(fallback time.Duration) time.Duration {
	raw := s.Raw()
	if raw == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return fallback
}

// CircuitBreakerConfig 映射顶层 circuit_breaker 块，描述 provider 健康熔断策略。
//
// 该块此前只存在于配置模板中，没有任何 Go 结构消费它（runtimeserver 的配置
// 文档层把它归类为 runtimeConfigPathInactive）。这里把它接上，作为子 Agent
// 路由动态健康源的策略来源——复用既有配置键，而不是新增一套。
type CircuitBreakerConfig struct {
	FailureRate      ConfigScalar `yaml:"failure_rate" mapstructure:"failure_rate"`
	FailureThreshold ConfigScalar `yaml:"failure_threshold" mapstructure:"failure_threshold"`
	HalfOpenMaxCalls ConfigScalar `yaml:"half_open_max_calls" mapstructure:"half_open_max_calls"`
	OpenTimeout      ConfigScalar `yaml:"open_timeout" mapstructure:"open_timeout"`
	SampleThreshold  ConfigScalar `yaml:"sample_threshold" mapstructure:"sample_threshold"`
	WindowDuration   ConfigScalar `yaml:"window_duration" mapstructure:"window_duration"`
}
