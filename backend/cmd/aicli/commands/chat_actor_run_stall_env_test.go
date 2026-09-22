package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLocalChatRunStallTimeoutFromEnv 回归：run 无进展 watchdog 默认必须关闭。
//
// 该 watchdog 触发时会以 context.Canceled 中止整个 run/turn（用户侧表现为
// "操作错误: context canceled"），自动化长任务不应被它意外打断；只有显式
// 配置 AICLI_RUN_STALL_TIMEOUT 才启用。上游挂死由请求级超时 + 重试负责。
func TestLocalChatRunStallTimeoutFromEnv(t *testing.T) {
	t.Setenv(runStallTimeoutEnv, "")
	require.Zero(t, defaultLocalChatRunStallTimeout,
		"默认阈值必须是 0：run 级看门狗默认关闭")
	require.Zero(t, localChatRunStallTimeoutFromEnv(),
		"未设置时必须保持关闭，而不是回落到某个 15m 之类的硬编码值")

	t.Setenv(runStallTimeoutEnv, "30m")
	require.Equal(t, 30*time.Minute, localChatRunStallTimeoutFromEnv())

	t.Setenv(runStallTimeoutEnv, " 1h ")
	require.Equal(t, time.Hour, localChatRunStallTimeoutFromEnv())

	t.Setenv(runStallTimeoutEnv, "off")
	require.Zero(t, localChatRunStallTimeoutFromEnv(), "off 必须显式关闭")

	t.Setenv(runStallTimeoutEnv, "0s")
	require.Zero(t, localChatRunStallTimeoutFromEnv(), "0s 必须显式关闭")

	t.Setenv(runStallTimeoutEnv, "30")
	require.Zero(t, localChatRunStallTimeoutFromEnv(),
		"非法值（漏写单位）必须回退默认（关闭），不能误开看门狗")
}
