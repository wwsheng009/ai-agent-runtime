package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func isolateMCPHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}

func writeMCPConfig(t *testing.T, path, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// 「用户级基础 + 项目级增量」：项目级同名覆盖，状态里能看到来源与 shadow 关系。
func TestManagerLoadConfigEffectiveMergesLayers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	userPath := writeMCPConfig(t, filepath.Join(home, ".aicli", "mcp.yaml"), `mcpServers:
  base:
    type: stdio
    command: base-cmd
    timeout: 30s
  shared:
    type: stdio
    command: from-user
    timeout: 30s
`)
	projectPath := writeMCPConfig(t, filepath.Join(project, ".aicli", "mcp.yaml"), `mcpServers:
  shared:
    type: streamable
    url: https://project.example.com/mcp
    timeout: 30s
  extra:
    type: stdio
    command: extra-cmd
    timeout: 30s
`)

	mgr := newTestManager(nil)
	connectCalls := 0
	mgr.newClient = func(name string, _ *config.MCPConfig) (client.Client, error) {
		connectCalls++
		return &fakeClient{name: name, connected: true}, nil
	}
	require.NoError(t, mgr.LoadConfigEffective(""))
	require.NoError(t, mgr.Start(context.Background()))

	statuses := mgr.ListMCPs()
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		names = append(names, status.Name)
	}
	require.ElementsMatch(t, []string{"base", "shared", "extra"}, names)
	require.Equal(t, 3, connectCalls, "合并后的 server 都应参与连接")

	status, err := mgr.GetMCPStatus("shared")
	require.NoError(t, err)
	require.Equal(t, "project", status.ConfigSource)
	require.Equal(t, projectPath, status.ConfigPath)
	require.Len(t, status.ShadowedSources, 1)
	require.Equal(t, userPath, status.ShadowedSources[0].Path)
	require.Equal(t, "user", status.ShadowedSources[0].Source)

	base, err := mgr.GetMCPStatus("base")
	require.NoError(t, err)
	require.Equal(t, "user", base.ConfigSource)
	require.Empty(t, base.ShadowedSources)

	// 合并后 project 覆盖了 shared 的传输类型（整体覆盖，不残留 stdio 字段）。
	cfg := mgr.lookupMCPConfig("shared")
	require.NotNil(t, cfg)
	require.Equal(t, "streamable", cfg.Type)
	require.Empty(t, cfg.Command)

	// 可选能力：来源报告器。
	reporter, ok := any(mgr).(ConfigOriginReporter)
	require.True(t, ok)
	origins := reporter.MCPConfigOrigins()
	require.Equal(t, "project", origins["extra"].Source)
	require.Empty(t, reporter.MCPConfigWarnings())
}

// 低优先级文件损坏时：跳过并给出告警，高层配置照常生效。
func TestManagerLoadConfigEffectiveWarnsOnBrokenLowerLayer(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	writeMCPConfig(t, filepath.Join(home, ".aicli", "mcp.yaml"), "{broken-json")
	projectPath := writeMCPConfig(t, filepath.Join(project, ".aicli", "mcp.yaml"), `mcpServers:
  ok:
    type: stdio
    command: ok-cmd
    timeout: 30s
`)

	mgr := newTestManager(nil)
	require.NoError(t, mgr.LoadConfigEffective(""))
	reporter := any(mgr).(ConfigOriginReporter)
	require.Len(t, reporter.MCPConfigWarnings(), 1)
	_, err := mgr.GetMCPStatus("ok")
	require.NoError(t, err)
	require.Equal(t, projectPath, any(mgr).(*manager).origins["ok"].Path)
}

// 显式覆盖（--config-file）保持精确加载：不合并发现链上的其它文件。
func TestManagerLoadConfigEffectiveExplicitWins(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	writeMCPConfig(t, filepath.Join(home, ".aicli", "mcp.yaml"), `mcpServers:
  from-user:
    type: stdio
    command: user-cmd
    timeout: 30s
`)
	explicit := writeMCPConfig(t, filepath.Join(project, "custom", "mcp.yaml"), `mcpServers:
  from-explicit:
    type: stdio
    command: explicit-cmd
    timeout: 30s
`)

	mgr := newTestManager(nil)
	require.NoError(t, mgr.LoadConfigEffective(explicit))
	status, err := mgr.GetMCPStatus("from-explicit")
	require.NoError(t, err)
	require.Equal(t, "explicit", status.ConfigSource)
	require.Equal(t, explicit, status.ConfigPath)
	_, err = mgr.GetMCPStatus("from-user")
	require.Error(t, err, "显式覆盖时不应合并用户级文件")

	// 显式路径不存在 → 明确报错。
	require.Error(t, mgr.LoadConfigEffective(filepath.Join(project, "missing.yaml")))
}

// 单文件加载（LoadConfig）也要报告来源，便于 list/status 显示「来自哪个文件」。
func TestManagerLoadConfigReportsFileOrigin(t *testing.T) {
	path := writeMCPConfig(t, filepath.Join(t.TempDir(), "mcp.yaml"), `mcpServers:
  solo:
    type: stdio
    command: solo-cmd
    timeout: 30s
`)
	mgr := newTestManager(nil)
	require.NoError(t, mgr.LoadConfig(path))
	status, err := mgr.GetMCPStatus("solo")
	require.NoError(t, err)
	require.Equal(t, "file", status.ConfigSource)
	require.Equal(t, path, status.ConfigPath)
}
