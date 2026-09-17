package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func isolateRuntimeLayerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { config.SetUserHomeDirForTest(previous) })
	return home
}

func writeRuntimeLayerFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// 分层层栈快照：portable 层标记只读，用户/项目层可写，路径按存在性给出绝对路径。
func TestRuntimeConfigLayersProviderMarksPortableReadOnly(t *testing.T) {
	home := isolateRuntimeLayerHome(t)
	projectDir := t.TempDir()
	t.Chdir(projectDir)

	portable := filepath.Join(projectDir, "configs", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeLayerFile(t, portable, "agent:\n  maxSteps: 1\n")
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeLayerFile(t, userConfig, "agent:\n  maxSteps: 7\n")

	layers := NewRuntimeConfigLayersProvider()()
	find := func(kind string, present bool) (skillsapi.ConfigDocumentLayer, bool) {
		for _, layer := range layers {
			if layer.Kind == kind && layer.Present == present {
				return layer, true
			}
		}
		return skillsapi.ConfigDocumentLayer{}, false
	}

	portableLayer, ok := find("portable", true)
	require.True(t, ok, "the present portable layer must appear: %#v", layers)
	require.True(t, portableLayer.ReadOnly)
	require.Equal(t, filepath.Clean(portable), filepath.Clean(portableLayer.Path))

	userLayer, ok := find("user", true)
	require.True(t, ok, "the present user layer must appear: %#v", layers)
	require.False(t, userLayer.ReadOnly)
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(userLayer.Path))

	projectLayer, ok := find("project", false)
	require.True(t, ok, "the absent project candidate must appear: %#v", layers)
	require.False(t, projectLayer.ReadOnly)
}

// 端到端：只有只读 portable 层时，保存 agent.maxSteps 落在用户级新文件，
// 仓库/portable 文件保持字节不变；读取端返回的写入目标也是该用户级路径。
func TestLayeredMaxStepsPersisterWritesWritableLayer(t *testing.T) {
	home := isolateRuntimeLayerHome(t)
	projectDir := t.TempDir()
	t.Chdir(projectDir)

	portable := filepath.Join(projectDir, "configs", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeLayerFile(t, portable, "agent:\n  maxSteps: 1\n")

	manager := runtimecfg.NewRuntimeManager(portable)
	require.NoError(t, manager.Load())
	require.Equal(t, 1, manager.Get().Agent.MaxMaxSteps)

	configFile, err := NewLayeredRuntimeAgentMaxStepsPersister(manager)(12)
	require.NoError(t, err)

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(configFile))

	userRaw, err := os.ReadFile(userConfig)
	require.NoError(t, err, "the writable layer must be created")
	require.Contains(t, string(userRaw), "maxSteps: 12")

	portableRaw, err := os.ReadFile(portable)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(portableRaw), "maxSteps: 1"),
		"the read-only portable layer must stay untouched:\n%s", portableRaw)

	maxSteps, path, err := NewLayeredRuntimeAgentMaxStepsReader(manager)()
	require.NoError(t, err)
	require.Equal(t, 12, maxSteps)
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(path))
}

// 用户级层存在时，保存回到同一层（幂等：同值重复保存不重写文件）。
func TestLayeredMaxStepsPersisterIsIdempotentOnSameValue(t *testing.T) {
	home := isolateRuntimeLayerHome(t)
	projectDir := t.TempDir()
	t.Chdir(projectDir)

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeRuntimeLayerFile(t, userConfig, "agent:\n  maxSteps: 3\n")
	before, err := os.Stat(userConfig)
	require.NoError(t, err)

	manager := runtimecfg.NewRuntimeManager(userConfig)
	require.NoError(t, manager.Load())

	configFile, err := NewLayeredRuntimeAgentMaxStepsPersister(manager)(3)
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(userConfig), filepath.Clean(configFile))

	after, err := os.Stat(userConfig)
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "an unchanged value must not rewrite the file")
}
