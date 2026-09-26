package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// §4.9 F2：分层文件禁用 bypass 的判定（plan 退出等「非切换入口」的还原路径共用）。

func TestBypassDisabledForWorkspace(t *testing.T) {
	isolatePermissionsHome(t)

	// 无任何层 → 未禁用（也要覆盖：不能因为「读不到」就返回 true）。
	empty := t.TempDir()
	require.False(t, BypassDisabledForWorkspace(empty))

	// 工作区层命中。
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions.yaml"),
		[]byte("version: 1\ndisable_bypass: true\n"), 0o644))
	require.True(t, BypassDisabledForWorkspace(workspace))

	// 坏文件按「整文件丢弃」处理：不参与求值，也不降级。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions.yaml"),
		[]byte("version: 1\ndisable_bypass: [broken\n"), 0o644))
	require.False(t, BypassDisabledForWorkspace(workspace))

	// 用户层（工作区为空）同样生效。
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	homeDir := filepath.Join(home, ".aicli")
	require.NoError(t, os.MkdirAll(homeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, "permissions.yaml"),
		[]byte("version: 1\ndisable_bypass: true\n"), 0o644))
	require.True(t, BypassDisabledForWorkspace(""))
}
