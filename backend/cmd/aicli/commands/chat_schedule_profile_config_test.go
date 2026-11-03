package commands

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// A profile-level runtime.yaml is resolved onto ChatSession.RuntimeConfigPath
// (applyProfileStateToChatSession). This test pins the end-to-end control path:
// profile resolution -> session projection -> runtime config load, without
// changing the process-wide default.
func TestProfileRuntimeConfigControlsSchedule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	chdirTest(t, t.TempDir())

	profilesRoot := t.TempDir()
	writeProfile := func(name, scheduleEnabled string) string {
		t.Helper()
		root := filepath.Join(profilesRoot, name)
		writeTestFile(t, filepath.Join(root, "profile.yaml"),
			"profile:\n  name: "+name+"\n  default_agent: default\n")
		writeTestFile(t, filepath.Join(root, "agents", "default", "agent.yaml"),
			"name: default\ndescription: schedule profile fixture\n")
		runtimePath := filepath.Join(root, "runtime.yaml")
		writeTestFile(t, runtimePath, "schedule:\n  enabled: "+scheduleEnabled+"\n")
		return runtimePath
	}
	disabledPath := writeProfile("readonly", "false")
	enabledPath := writeProfile("writer", "true")

	cfg := &agentconfig.Config{Profiles: &agentconfig.ProfilesConfig{Root: profilesRoot}}

	disabledSession := &ChatSession{Config: cfg}
	disabledState, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "readonly"})
	require.NoError(t, err)
	require.True(t, applyProfileStateToChatSession(disabledSession, disabledState))
	require.Equal(t, disabledPath, disabledSession.RuntimeConfigPath)
	resolved := loadRuntimeToolConfig(cfg, disabledSession)
	require.False(t, resolved.Schedule.Enabled, "profile runtime.yaml must be able to disable schedule")
	require.True(t, runtimecfg.DefaultRuntimeConfig().Schedule.Enabled,
		"profile override must not change the process-wide default")

	enabledSession := &ChatSession{Config: cfg}
	enabledState, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "writer"})
	require.NoError(t, err)
	require.True(t, applyProfileStateToChatSession(enabledSession, enabledState))
	require.Equal(t, enabledPath, enabledSession.RuntimeConfigPath)
	resolved = loadRuntimeToolConfig(cfg, enabledSession)
	require.True(t, resolved.Schedule.Enabled, "profile runtime.yaml must be able to keep schedule enabled")
}
