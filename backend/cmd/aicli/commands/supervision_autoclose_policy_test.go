package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestLocalAutoCloseCompletedPolicy 钉住 P1-C 方案 C 的降级口径：解析
// agents.autoCloseCompleted 时，任何"读不到 / 读不懂"的情况都必须回落 off，
// 绝不能把拼写错误或未装配解读成"自动关闭所有子会话"。
func TestLocalAutoCloseCompletedPolicy(t *testing.T) {
	t.Run("nil host and unconfigured host stay off", func(t *testing.T) {
		require.Equal(t, runtimecfg.AutoClosePolicyOff, localAutoCloseCompletedPolicy(nil))
		require.Equal(t, runtimecfg.AutoClosePolicyOff,
			localAutoCloseCompletedPolicy(&localChatRuntimeHost{}),
			"a host without RuntimeConfig must not invent a policy")
	})

	t.Run("configured values are normalized", func(t *testing.T) {
		cases := []struct {
			name       string
			configured string
			want       string
		}{
			{name: "empty falls back to default", configured: "", want: runtimecfg.AutoClosePolicyOff},
			{name: "off", configured: runtimecfg.AutoClosePolicyOff, want: runtimecfg.AutoClosePolicyOff},
			{name: "completed", configured: runtimecfg.AutoClosePolicyCompleted, want: runtimecfg.AutoClosePolicyCompleted},
			{name: "batch_terminal", configured: runtimecfg.AutoClosePolicyBatchTerminal, want: runtimecfg.AutoClosePolicyBatchTerminal},
			{name: "case and padding", configured: "  Batch_Terminal ", want: runtimecfg.AutoClosePolicyBatchTerminal},
			{name: "unknown value fails closed", configured: "everything", want: runtimecfg.AutoClosePolicyOff},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				host := &localChatRuntimeHost{
					RuntimeConfig: &runtimecfg.RuntimeConfig{
						Agents: runtimecfg.AgentsConfig{AutoCloseCompleted: tc.configured},
					},
				}
				require.Equal(t, tc.want, localAutoCloseCompletedPolicy(host))
			})
		}
	})
}
