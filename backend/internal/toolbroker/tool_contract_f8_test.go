package toolbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// F8：apply_agent_worktree 的工具面契约——
//  1. id/paths/keep/force 必须原样到达宿主控制器；
//  2. 宿主在 H14 主树冲突时拒绝（force=false）的可执行错误必须原样上抛，
//     让父代理看到冲突路径与 next_action，而不是被折叠成裸 git 失败。
type conflictApplyController struct {
	*fakeAgentSessionController
	refused error
}

func (c *conflictApplyController) ApplyWorktree(ctx context.Context, args ApplyAgentWorktreeArgs) (*AgentWorktreeResult, error) {
	c.lastApply = args
	return nil, c.refused
}

func TestBrokerApplyAgentWorktreePlumbsArgs(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	result, summary, err := broker.Execute(context.Background(), "parent-session", ToolApplyAgentWorktree, map[string]interface{}{
		"id":    "child-1",
		"paths": []interface{}{"docs/a.md", "docs/b.md"},
		"keep":  true,
		"force": true,
	})
	require.NoError(t, err)
	require.Equal(t, "child-1", controller.lastApply.ID)
	require.Equal(t, []string{"docs/a.md", "docs/b.md"}, controller.lastApply.Paths)
	require.True(t, controller.lastApply.Keep)
	require.True(t, controller.lastApply.Force)

	worktreeResult, ok := result.(*AgentWorktreeResult)
	require.True(t, ok)
	require.True(t, worktreeResult.Applied)
	require.Equal(t, true, summary["applied"])
	require.Equal(t, true, summary["kept"])
}

func TestBrokerApplyAgentWorktreeSurfacesRefusedApply(t *testing.T) {
	// 与 worktree.ApplyConflictError 同形的拒绝文本：冲突路径 + next_action。
	refused := errors.New("apply refused: main tree has local changes in 1 path(s) that aicli/agent/x would overwrite (README.md); commit or stash those main-tree paths first, re-run apply with a narrower paths list that excludes them, or pass force=true to overwrite them deliberately")
	controller := &conflictApplyController{
		fakeAgentSessionController: &fakeAgentSessionController{},
		refused:                    refused,
	}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolApplyAgentWorktree, map[string]interface{}{
		"id": "child-1",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, refused, "the refused apply must not be folded into a bare git failure")
	require.Contains(t, err.Error(), "README.md")
	require.Contains(t, err.Error(), "force=true")
	require.False(t, controller.lastApply.Force, "the default apply stays non-forcing")
}
