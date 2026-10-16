package toolkit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// isBuiltinCoreToolName 的 shell 家族判定已交给注册表别名表。这里钉住"别名仍然算
// 核心工具"这一外部契约，避免该名单退化成又一份手写别名清单（此前 bash 与
// execute_shell_command 就是逐名写死的）。
func TestIsBuiltinCoreToolNameShellFamily(t *testing.T) {
	for _, name := range []string{
		"shell",
		"bash",
		"execute_shell_command",
		" Shell ",
		"BASH",
		"Execute_Shell_Command",
	} {
		require.Truef(t, isBuiltinCoreToolName(name), "%q 应是内置核心工具", name)
	}

	require.True(t, isBuiltinCoreToolName(ToolSearchName))
	require.True(t, isBuiltinCoreToolName("view"))
	require.False(t, isBuiltinCoreToolName("my_mcp_tool"))
	require.False(t, isBuiltinCoreToolName(""))
}
