package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func writeRuntimeServerLayer(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// P2 接线：runtime-server 启动时通过分层栈加载 runtime.yaml —— 用户级覆盖只读的
// portable（仓库）层；任何层都不存在时，管理器路径指向用户级写入目标，使设置页
// 显示与实际落盘一致（不再出现指向不存在仓库文件的路径）。
func TestLoadRuntimeServerManagerUsesLayeredRuntimeConfig(t *testing.T) {
	home := t.TempDir()
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { config.SetUserHomeDirForTest(previous) })

	projectDir := t.TempDir()
	t.Chdir(projectDir)

	portable := filepath.Join(projectDir, "configs", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeServerLayer(t, portable, "agent:\n  maxSteps: 1\n")
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeServerLayer(t, userConfig, "agent:\n  maxSteps: 7\n")

	manager := runtimecfg.NewRuntimeManager(portable)
	require.NoError(t, loadRuntimeServerManager(manager))
	require.Equal(t, 7, manager.Get().Agent.MaxMaxSteps,
		"the user layer must override the portable repository default")
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(manager.GetFilePath()))

	// 全新安装：磁盘上没有任何 runtime.yaml —— 用内置默认值，路径指向用户级目标。
	require.NoError(t, os.Remove(userConfig))
	require.NoError(t, os.Remove(portable))
	fresh := runtimecfg.NewRuntimeManager("")
	require.NoError(t, loadRuntimeServerManager(fresh))
	require.Equal(t, 0, fresh.Get().Agent.MaxMaxSteps, "a fresh install falls back to built-in defaults")
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(fresh.GetFilePath()))
}
