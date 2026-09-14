package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestLocalChatSessionCheckpointIntervalFromEnv 回归：长 turn 中途落库间隔的
// 环境变量覆盖语义——未设置用默认值，显式 off/0 关闭，非法值回退默认，
// 避免"漏写单位"这类笔误静默关掉落库。
func TestLocalChatSessionCheckpointIntervalFromEnv(t *testing.T) {
	t.Setenv("AICLI_SESSION_CHECKPOINT_INTERVAL", "")
	require.Zero(t, localChatSessionCheckpointIntervalFromEnv(), "未设置时必须回退 actor 默认间隔")
	require.Equal(t, 15*time.Second, chat.DefaultSessionCheckpointInterval,
		"CLI 未设置时落到的默认间隔（actor 兜底）必须仍是 15s")

	t.Setenv("AICLI_SESSION_CHECKPOINT_INTERVAL", "5s")
	require.Equal(t, 5*time.Second, localChatSessionCheckpointIntervalFromEnv())

	t.Setenv("AICLI_SESSION_CHECKPOINT_INTERVAL", " off ")
	require.Negative(t, localChatSessionCheckpointIntervalFromEnv(), "off 必须显式关闭中途落库")

	t.Setenv("AICLI_SESSION_CHECKPOINT_INTERVAL", "0s")
	require.Negative(t, localChatSessionCheckpointIntervalFromEnv(), "0s 必须显式关闭中途落库")

	t.Setenv("AICLI_SESSION_CHECKPOINT_INTERVAL", "15")
	require.Zero(t, localChatSessionCheckpointIntervalFromEnv(), "非法值必须回退默认而不是静默关闭")
}
