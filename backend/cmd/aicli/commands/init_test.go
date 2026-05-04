package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunInitCommandUsesLocalStarterPathByDefault(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	result, _, err := runInitCommand(nil)
	if err != nil {
		t.Fatalf("runInitCommand failed: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected starter config to be created, got %+v", result)
	}
	if result.ConfigPath != filepath.Clean(".aicli/config.yaml") {
		t.Fatalf("unexpected config path: %q", result.ConfigPath)
	}

	raw, err := os.ReadFile(result.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "providers:") || !strings.Contains(content, "aicli:") {
		t.Fatalf("unexpected starter config content:\n%s", content)
	}
}

func TestRunInitCommandSupportsGlobalFlag(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) {
		return tempDir, nil
	})
	t.Cleanup(func() {
		config.SetUserHomeDirForTest(previous)
		_ = os.Chdir(cwd)
	})

	cmd := NewInitCommand()
	if err := cmd.Flags().Set("global", "true"); err != nil {
		t.Fatalf("Set global flag failed: %v", err)
	}
	result, _, err := runInitCommand(cmd)
	if err != nil {
		t.Fatalf("runInitCommand failed: %v", err)
	}
	if !strings.HasPrefix(result.ConfigPath, tempDir) {
		t.Fatalf("expected global config under temp home, got %q", result.ConfigPath)
	}
	expected := filepath.Join(tempDir, ".aicli", "config.yaml")
	if result.ConfigPath != expected {
		t.Fatalf("unexpected config path: %q, want %q", result.ConfigPath, expected)
	}
	if !result.Created {
		t.Fatalf("expected global starter config to be created, got %+v", result)
	}
}

func TestRunInitCommandExpandsTildeConfigPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) {
		return tempDir, nil
	})
	t.Cleanup(func() {
		config.SetUserHomeDirForTest(previous)
		_ = os.Chdir(cwd)
	})

	cmd := NewInitCommand()
	if err := cmd.Flags().Set("config", "~/.aicli/config.yaml"); err != nil {
		t.Fatalf("Set config flag failed: %v", err)
	}
	result, _, err := runInitCommand(cmd)
	if err != nil {
		t.Fatalf("runInitCommand failed: %v", err)
	}
	expected := filepath.Join(tempDir, ".aicli", "config.yaml")
	if result.ConfigPath != expected {
		t.Fatalf("unexpected config path: %q, want %q", result.ConfigPath, expected)
	}
	if !result.Created {
		t.Fatalf("expected starter config to be created, got %+v", result)
	}
}
