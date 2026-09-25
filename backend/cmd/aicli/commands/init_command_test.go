package commands

import (
	"os"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func isolateInitHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { config.SetUserHomeDirForTest(previous) })
	return home
}

func runInitWithFlags(t *testing.T, flags map[string]string) (initCommandResult, error) {
	t.Helper()
	cmd := NewInitCommand()
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set flag %s: %v", name, err)
		}
	}
	cmd.SetArgs(nil)
	result, _, err := runInitCommand(cmd)
	return result, err
}

// Decision D4: a bare `aicli init` must not create a project-local file in an
// arbitrary working directory; it creates the user-level config instead.
func TestInitCommandDefaultsToUserLevelConfig(t *testing.T) {
	home := isolateInitHome(t)
	projectDir := t.TempDir()
	chdirTest(t, projectDir)

	result, err := runInitWithFlags(t, nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	want := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	if result.ConfigPath != want {
		t.Fatalf("config path = %q, want %q", result.ConfigPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected the user-level starter file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("bare init must not create a project-level config (stat err=%v)", err)
	}
}

func TestInitCommandProjectFlagCreatesProjectLevelConfig(t *testing.T) {
	home := isolateInitHome(t)
	projectDir := t.TempDir()
	chdirTest(t, projectDir)

	result, err := runInitWithFlags(t, map[string]string{"project": "true"})
	if err != nil {
		t.Fatalf("init --project: %v", err)
	}
	// The project target keeps the historical project-relative spelling.
	want := config.ResolveProjectConfigPath()
	if result.ConfigPath != want {
		t.Fatalf("config path = %q, want %q", result.ConfigPath, want)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)); err != nil {
		t.Fatalf("expected the project-level starter file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("--project must not create a user-level config (stat err=%v)", err)
	}
}

// The legacy --global flag keeps working for existing scripts.
func TestInitCommandGlobalFlagStillCreatesUserLevelConfig(t *testing.T) {
	home := isolateInitHome(t)
	chdirTest(t, t.TempDir())

	result, err := runInitWithFlags(t, map[string]string{"global": "true"})
	if err != nil {
		t.Fatalf("init --global: %v", err)
	}
	if want := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName); result.ConfigPath != want {
		t.Fatalf("config path = %q, want %q", result.ConfigPath, want)
	}
}

func TestInitCommandRejectsConflictingFlags(t *testing.T) {
	isolateInitHome(t)
	chdirTest(t, t.TempDir())

	if _, err := runInitWithFlags(t, map[string]string{"project": "true", "global": "true"}); err == nil {
		t.Fatal("expected --project/--global conflict to fail")
	}
	configPath := filepath.Join(t.TempDir(), "custom.yaml")
	if _, err := runInitWithFlags(t, map[string]string{"config": configPath, "project": "true"}); err == nil {
		t.Fatal("expected --config/--project conflict to fail")
	}
}
