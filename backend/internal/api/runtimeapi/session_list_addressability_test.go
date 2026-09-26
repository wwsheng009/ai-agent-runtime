package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func sessionRowWithID(t *testing.T, id string) *chat.Session {
	t.Helper()
	session := chat.NewSession("tester")
	require.NotNil(t, session)
	session.ID = id
	return session
}

// 历史脏数据（带路径分隔符 / <nil> 的 ID）不得出现在会话列表里：读取路径会先做
// NormalizeSessionID，这类行点开必然 404，列表暴露它们只会产生无解的前端报错。
func TestFilterAddressableSessionsDropsUnreachableRows(t *testing.T) {
	readable := sessionRowWithID(t, "session_20260915_ab12cd34")
	agentRow := sessionRowWithID(t, "lead")
	pathRow := sessionRowWithID(t, "/root/p26s3b")
	trailingRow := sessionRowWithID(t, "p26s3b/")
	paddedRow := sessionRowWithID(t, " p26s3b ")
	nilRow := sessionRowWithID(t, "<nil>")

	got := filterAddressableSessions([]*chat.Session{
		nil, readable, pathRow, agentRow, trailingRow, paddedRow, nilRow,
	})

	require.Len(t, got, 2)
	require.Equal(t, "session_20260915_ab12cd34", got[0].ID)
	require.Equal(t, "lead", got[1].ID)
}

func TestFilterAddressableSessionsPreservesEmptyShape(t *testing.T) {
	require.Nil(t, filterAddressableSessions(nil))
	require.Empty(t, filterAddressableSessions([]*chat.Session{}))
}
