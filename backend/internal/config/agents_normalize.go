package config

import "strings"

// DefaultAgentsConfig returns the built-in `agents.*` defaults. It is the single
// definition of what an "unset" agents knob resolves to.
func DefaultAgentsConfig() AgentsConfig {
	return DefaultRuntimeConfig().Agents
}

// NormalizeAgentsConfig resolves every unset field of an `agents:` block against
// the built-in defaults, so a *partially* written block behaves exactly like a
// block that spells all defaults out.
//
// 缺口背景（方案 附录 B 未决项 4）：运行期把 0/"" 读作「未设置，回落默认」，但这个
// 回落过去散落在各消费者里，且两个宿主各自带了一份「整个结构体全零才回退」的短路
// 判断。于是「部分写入」的配置块会绕过那份快照，改由各消费者自行解释零值——多数消
// 费者解释一致，唯独 `agents.maxDepth` 有一个消费者把 0 读成「无上限」
// （`applyAPIAgentChildDepthPolicy` / `applyLocalChildDepthPolicy` 的 `maxDepth <= 0`
// 早退），结果是只设了 `agents.maxThreads` 的配置会静默抬掉默认的深度上限（fail-open）。
// 归一化必须在**任何读取者之前**发生一次，"未设置" 才只有一个含义。
//
// 有意保留的语义（不被默认值覆盖）：
//   - MaxThreads == -1 仍是「显式不限」（agentcontrol.MaxThreadsUnlimited），只有 0 是未设置；
//   - RegistryTerminalRetention < 0 仍是「永不清除」的显式退出；
//   - ReclaimIdleMs 的默认值本身就是 0（保守回收：只回收可证明已死/终态的子会话），
//     因此 0 归一化后仍是 0，不会把保守策略变成 TTL 驱逐。
func NormalizeAgentsConfig(cfg AgentsConfig) AgentsConfig {
	defaults := DefaultAgentsConfig()
	if cfg.MaxThreads == 0 {
		cfg.MaxThreads = defaults.MaxThreads
	}
	if cfg.MaxDepth == 0 {
		cfg.MaxDepth = defaults.MaxDepth
	}
	// P1-4/H12: per-batch subagent ceiling. 0 是「未设置」→ 默认 4（与调度器
	// 内建默认一致，保证缺失配置时行为不变）；正数是显式上限。
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = defaults.MaxConcurrent
	}
	// 背压默认关闭：0 归一化后仍是 0，不会把「无限等待」意外变成有界队列。
	if cfg.MaxConcurrentQueueDepth == 0 {
		cfg.MaxConcurrentQueueDepth = defaults.MaxConcurrentQueueDepth
	}
	if cfg.MaxConcurrentQueueTimeoutMs == 0 {
		cfg.MaxConcurrentQueueTimeoutMs = defaults.MaxConcurrentQueueTimeoutMs
	}
	if cfg.DefaultWaitTimeoutMs == 0 {
		cfg.DefaultWaitTimeoutMs = defaults.DefaultWaitTimeoutMs
	}
	if cfg.MinWaitTimeoutMs == 0 {
		cfg.MinWaitTimeoutMs = defaults.MinWaitTimeoutMs
	}
	if cfg.MaxWaitTimeoutMs == 0 {
		cfg.MaxWaitTimeoutMs = defaults.MaxWaitTimeoutMs
	}
	if strings.TrimSpace(cfg.WaitTimeoutMode) == "" {
		cfg.WaitTimeoutMode = defaults.WaitTimeoutMode
	}
	if strings.TrimSpace(cfg.DefaultForkTurns) == "" {
		cfg.DefaultForkTurns = defaults.DefaultForkTurns
	}
	if cfg.RegistryReconcileInterval == 0 {
		cfg.RegistryReconcileInterval = defaults.RegistryReconcileInterval
	}
	if strings.TrimSpace(cfg.RegistryReconcileMode) == "" {
		cfg.RegistryReconcileMode = defaults.RegistryReconcileMode
	}
	if cfg.RegistryTerminalRetention == 0 {
		cfg.RegistryTerminalRetention = defaults.RegistryTerminalRetention
	}
	if cfg.ReclaimIdleMs == 0 {
		cfg.ReclaimIdleMs = defaults.ReclaimIdleMs
	}
	if strings.TrimSpace(cfg.AutoCloseCompleted) == "" {
		cfg.AutoCloseCompleted = defaults.AutoCloseCompleted
	}
	return cfg
}
