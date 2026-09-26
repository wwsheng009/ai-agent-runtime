package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestAgentsConfigDefaultsMatchZeroFallbacks 是「零值即未设置」的跨包一致性守卫：配置层
// 的 agents.* 默认值必须与各解析器对零值的解释逐一相同。若两者漂移，「不写 agents 块」
// 与「把默认值显式写出来」会得到不同的有效行为，而这种差异只在部分写入配置的环境里才会
// 暴露（方案 附录 B 未决项 4 的根因）。
//
// 放在 api/skills 是因为这是少数能同时 import config 与 agentcontrol 的包（config 经
// contextmgr→team 反向依赖 agentcontrol，后者无法在自己的测试里反向 import config）。
func TestAgentsConfigDefaultsMatchZeroFallbacks(t *testing.T) {
	defaults := runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{})

	// wait 窗口：配置默认值 == 解析器常量，且把默认值显式写进策略与留空等价。
	require.Equal(t, agentcontrol.DefaultWaitTimeoutMs, defaults.DefaultWaitTimeoutMs)
	require.Equal(t, agentcontrol.MinWaitTimeoutMs, defaults.MinWaitTimeoutMs)
	require.Equal(t, agentcontrol.MaxWaitTimeoutMs, defaults.MaxWaitTimeoutMs)
	require.Equal(t, agentcontrol.WaitTimeoutModeClamp, defaults.WaitTimeoutMode)

	zeroPolicy, err := agentcontrol.ResolveWaitTimeout(0, agentcontrol.WaitTimeoutPolicy{})
	require.NoError(t, err)
	require.Equal(t, defaults.DefaultWaitTimeoutMs, zeroPolicy.EffectiveMs)
	configuredPolicy, err := agentcontrol.ResolveWaitTimeout(0, agentcontrol.WaitTimeoutPolicy{
		DefaultMs: defaults.DefaultWaitTimeoutMs,
		MinMs:     defaults.MinWaitTimeoutMs,
		MaxMs:     defaults.MaxWaitTimeoutMs,
		Mode:      defaults.WaitTimeoutMode,
	})
	require.NoError(t, err)
	require.Equal(t, zeroPolicy, configuredPolicy, "writing the defaults out must not change the wait window")

	// 对账：间隔与模式在两条路径上同值。
	require.Equal(t, agentcontrol.DefaultReconcileInterval, defaults.RegistryReconcileInterval)
	require.Equal(t, defaults.RegistryReconcileInterval, agentcontrol.NormalizeReconcileInterval(0))
	require.Equal(t, agentcontrol.ParseReconcileMode(""), agentcontrol.ParseReconcileMode(defaults.RegistryReconcileMode))

	// 配额：0 必须回落到默认上限，而不是「不限」。
	limit, unlimited := agentcontrol.ResolveMaxThreads(0, defaults.MaxThreads)
	require.False(t, unlimited)
	require.Equal(t, defaults.MaxThreads, limit)

	// 保留窗口：配置默认值是 0，解析器把它映射为共享默认窗口。
	require.Equal(t, agentcontrol.DefaultTerminalRetention, agentcontrol.NormalizeTerminalRetention(defaults.RegistryTerminalRetention))
}
