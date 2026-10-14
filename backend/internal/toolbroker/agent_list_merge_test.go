package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMergeAgentStatusResultsDedupesAndSorts 钉住两个宿主共用的合并口径：
// 注册表来源优先（同身份不重复），排序键为 path → session_id → id。
func TestMergeAgentStatusResultsDedupesAndSorts(t *testing.T) {
	primary := []AgentStatusResult{
		{SessionID: "beta", Path: "/root/beta"},
		{SessionID: "alpha"},
	}
	extra := []AgentStatusResult{
		{SessionID: "alpha", Path: "/root/duplicate"},
		{SessionID: "gamma"},
		{ID: "gamma"},
	}
	merged := MergeAgentStatusResults(primary, extra)
	require.Len(t, merged, 3)
	require.Equal(t, "/root/beta", merged[0].Path, "path sort key wins; '/' sorts before letters")
	require.Equal(t, "alpha", merged[1].SessionID)
	require.Empty(t, primary[1].Path, "primary rows are never rewritten by the merge")
	require.Empty(t, merged[1].Path, "primary row wins over the duplicate extra row")
	require.Equal(t, "gamma", merged[2].SessionID)
}

func TestMergeAgentStatusResultsReturnsPrimaryWithoutExtra(t *testing.T) {
	primary := []AgentStatusResult{{SessionID: "solo"}}
	require.Equal(t, primary, MergeAgentStatusResults(primary, nil))
}

// TestPathPrefixCoversWholeAgentTree 钉住批任务账本行的 path_prefix 参与规则：
// 只有「不过滤或覆盖整树」时账本行才并入列表——账本行没有 agent 路径，收窄到
// 子树时不能靠合成路径伪装命中。
func TestPathPrefixCoversWholeAgentTree(t *testing.T) {
	for _, value := range []string{"", "/", "root", "/root", "/root/"} {
		require.True(t, PathPrefixCoversWholeAgentTree(value), "expected whole-tree filter for %q", value)
	}
	for _, value := range []string{"/root/worker", "/root2", "worker"} {
		require.False(t, PathPrefixCoversWholeAgentTree(value), "expected subtree filter for %q", value)
	}
}
