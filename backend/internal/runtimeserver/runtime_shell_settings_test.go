package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func boolPtrShell(value bool) *bool { return &value }

func TestPersistShellSettingsPreservesCommentsAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	original := "# 顶部注释\n" +
		"background:\n" +
		"  maxConcurrentJobs: 4\n" +
		"shell:\n" +
		"  # 候选顺序（人工维护）\n" +
		"  prefer: [powershell]\n" +
		"  autoDetect: true\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	cfg := runtimeexecutor.ShellConfig{
		AutoDetect:     boolPtrShell(true),
		Prefer:         []string{"git-bash", "busybox", "powershell"},
		ProbeTimeout:   3 * time.Second,
		StrictProbe:    boolPtrShell(true),
		ScriptDelivery: runtimeexecutor.ScriptDeliveryStdin,
	}
	require.NoError(t, persistShellSettings(path, cfg))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)

	// Comments and section order survive: the writer only touches shell.* value
	// nodes.
	require.Contains(t, text, "# 顶部注释")
	require.Contains(t, text, "# 候选顺序（人工维护）")
	require.Contains(t, text, "maxConcurrentJobs: 4")
	require.Less(t, strings.Index(text, "background:"), strings.Index(text, "shell:"))
	require.Contains(t, text, "- git-bash")

	var decoded struct {
		Shell runtimeexecutor.ShellConfig `yaml:"shell"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &decoded))
	require.Equal(t, []string{"git-bash", "busybox", "powershell"}, decoded.Shell.Prefer)
	require.Equal(t, 3*time.Second, decoded.Shell.ProbeTimeout)
	require.True(t, decoded.Shell.ShellAutoDetect())
	require.Equal(t, runtimeexecutor.ScriptDeliveryStdin, decoded.Shell.EffectiveScriptDelivery())
}

func TestPersistShellSettingsRemovesUnsetKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	original := "shell:\n  path: C:/Program Files/Git/bin/bash.exe\n  prefer: [git-bash]\n  probeTimeout: 5s\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	// Empty config = "use the built-in defaults": the keys must disappear
	// instead of pinning an empty value.
	require.NoError(t, persistShellSettings(path, runtimeexecutor.ShellConfig{}))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	require.NotContains(t, text, "prefer:")
	require.NotContains(t, text, "probeTimeout:")
	require.NotContains(t, text, "C:/Program Files/Git/bin/bash.exe")
}

func TestPersistShellSettingsCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".aicli", "runtime.yaml")

	require.NoError(t, persistShellSettings(path, runtimeexecutor.ShellConfig{
		Prefer: []string{"busybox", "cmd"},
	}))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded struct {
		Shell runtimeexecutor.ShellConfig `yaml:"shell"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &decoded))
	require.Equal(t, []string{"busybox", "cmd"}, decoded.Shell.Prefer)
}
