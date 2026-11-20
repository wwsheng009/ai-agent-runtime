package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestChatConfigFingerprintTracksProviderFragment(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	providerPath := filepath.Join(home, ".aicli", agentconfig.ProviderConfigFileName)
	writeCommandsTestFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	writeCommandsTestFile(t, providerPath, "providers:\n  items:\n    p:\n      protocol: openai\n")
	if _, err := agentconfig.InitGlobalConfigLayered(configPath, ""); err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}

	before, err := chatConfigFingerprintForPath(configPath)
	if err != nil {
		t.Fatalf("fingerprint before: %v", err)
	}
	raw, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatalf("read provider file: %v", err)
	}
	if err := os.WriteFile(providerPath, append(raw, []byte("\n# changed\n")...), 0o600); err != nil {
		t.Fatalf("write provider file: %v", err)
	}
	after, err := chatConfigFingerprintForPath(configPath)
	if err != nil {
		t.Fatalf("fingerprint after: %v", err)
	}
	if before == after {
		t.Fatalf("provider fragment change must change the CLI fingerprint: %q", before)
	}

	explicit, err := agentconfig.InitGlobalConfigLayered(configPath, configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered explicit: %v", err)
	}
	if explicit.ConfigExplicitPath == "" {
		t.Fatalf("expected explicit config marker")
	}
	explicitBefore, err := chatConfigFingerprintForPath(configPath)
	if err != nil {
		t.Fatalf("explicit fingerprint before: %v", err)
	}
	if err := os.WriteFile(providerPath, append(raw, []byte("\n# changed again\n")...), 0o600); err != nil {
		t.Fatalf("write provider file again: %v", err)
	}
	explicitAfter, err := chatConfigFingerprintForPath(configPath)
	if err != nil {
		t.Fatalf("explicit fingerprint after: %v", err)
	}
	if explicitBefore != explicitAfter {
		t.Fatalf("explicit --config fingerprint must ignore provider fragment changes")
	}
}
