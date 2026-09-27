package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// F5：只有 worktree 隔离子会话（且已记录主仓库根）才登记只读外部根；
// 基会话、非 worktree 会话、缺 repo root 的会话都必须为空。
func TestChatReadOnlyRootsForWorktreeChild(t *testing.T) {
	child := &runtimechat.Session{}
	child.SetContext(toolbroker.AgentSessionContextIsolation, worktree.ModeWorktree)
	child.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, " /repo/main ")
	require.Equal(t, []string{"/repo/main"}, chatReadOnlyRoots(&ChatSession{RuntimeSession: child}))

	require.Nil(t, chatReadOnlyRoots(nil))
	require.Nil(t, chatReadOnlyRoots(&ChatSession{}), "a session without a runtime session gets nothing")

	plain := &runtimechat.Session{}
	plain.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, "/repo")
	require.Nil(t, chatReadOnlyRoots(&ChatSession{RuntimeSession: plain}),
		"a non-worktree session must not exempt the repo")

	missing := &runtimechat.Session{}
	missing.SetContext(toolbroker.AgentSessionContextIsolation, worktree.ModeWorktree)
	require.Nil(t, chatReadOnlyRoots(&ChatSession{RuntimeSession: missing}),
		"a worktree session without a repo root gets nothing")
}
