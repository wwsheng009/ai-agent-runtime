package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// F5（API 宿主对等能力）：只有 worktree 隔离子会话（且已记录主仓库根）才登记
// 只读外部根；其余会话必须为空，避免把读边界扩大到普通会话。
func TestAPISessionReadOnlyRootsForWorktreeChild(t *testing.T) {
	child := &chat.Session{}
	child.SetContext(toolbroker.AgentSessionContextIsolation, worktree.ModeWorktree)
	child.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, " /repo/main ")
	require.Equal(t, []string{"/repo/main"}, apiSessionReadOnlyRoots(child))

	require.Nil(t, apiSessionReadOnlyRoots(nil))

	plain := &chat.Session{}
	plain.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, "/repo")
	require.Nil(t, apiSessionReadOnlyRoots(plain),
		"a non-worktree session must not exempt the repo")

	missing := &chat.Session{}
	missing.SetContext(toolbroker.AgentSessionContextIsolation, worktree.ModeWorktree)
	require.Nil(t, apiSessionReadOnlyRoots(missing),
		"a worktree session without a repo root gets nothing")
}
