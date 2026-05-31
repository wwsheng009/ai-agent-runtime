package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunUninstallCommandDeletesUserAndLocalAICLIDirs(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	withTestHomeAndCWD(t, homeDir, workDir)

	userFile := filepath.Join(homeDir, ".aicli", "config.yaml")
	localFile := filepath.Join(workDir, ".aicli", "config.yaml")
	writeUninstallTestFile(t, userFile, "user")
	writeUninstallTestFile(t, localFile, "local")

	result, err := runUninstallCommand(uninstallRequest{Yes: true})
	if err != nil {
		t.Fatalf("runUninstallCommand failed: %v", err)
	}
	if result.DeletedCount != 2 {
		t.Fatalf("deleted count = %d, want 2; result=%+v", result.DeletedCount, result)
	}
	assertPathMissing(t, filepath.Join(homeDir, ".aicli"))
	assertPathMissing(t, filepath.Join(workDir, ".aicli"))
}

func TestRunUninstallCommandDryRunDoesNotDelete(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	withTestHomeAndCWD(t, homeDir, workDir)

	userDir := filepath.Join(homeDir, ".aicli")
	localDir := filepath.Join(workDir, ".aicli")
	writeUninstallTestFile(t, filepath.Join(userDir, "auth.json"), "{}")
	writeUninstallTestFile(t, filepath.Join(localDir, "config.yaml"), "local")

	result, err := runUninstallCommand(uninstallRequest{DryRun: true})
	if err != nil {
		t.Fatalf("runUninstallCommand failed: %v", err)
	}
	if result.DeletedCount != 0 || result.MissingCount != 0 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	assertPathExists(t, userDir)
	assertPathExists(t, localDir)
}

func TestRunUninstallCommandRequiresConfirmation(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	withTestHomeAndCWD(t, homeDir, workDir)

	_, err := runUninstallCommand(uninstallRequest{})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
}

func TestRunUninstallCommandDedupesWhenHomeIsWorkDir(t *testing.T) {
	root := t.TempDir()
	withTestHomeAndCWD(t, root, root)

	aicliDir := filepath.Join(root, ".aicli")
	writeUninstallTestFile(t, filepath.Join(aicliDir, "config.yaml"), "same")

	result, err := runUninstallCommand(uninstallRequest{Yes: true})
	if err != nil {
		t.Fatalf("runUninstallCommand failed: %v", err)
	}
	if result.DeletedCount != 1 || len(result.Targets) != 1 {
		t.Fatalf("expected one deduped target, got %+v", result)
	}
	if result.Targets[0].Scope != "user+local" {
		t.Fatalf("unexpected scope: %q", result.Targets[0].Scope)
	}
	assertPathMissing(t, aicliDir)
}

func TestRunUninstallCommandSupportsScopeFlags(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	withTestHomeAndCWD(t, homeDir, workDir)

	userDir := filepath.Join(homeDir, ".aicli")
	localDir := filepath.Join(workDir, ".aicli")
	writeUninstallTestFile(t, filepath.Join(userDir, "config.yaml"), "user")
	writeUninstallTestFile(t, filepath.Join(localDir, "config.yaml"), "local")

	result, err := runUninstallCommand(uninstallRequest{Yes: true, UserOnly: true})
	if err != nil {
		t.Fatalf("runUninstallCommand failed: %v", err)
	}
	if result.DeletedCount != 1 || result.Targets[0].Scope != "user" {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertPathMissing(t, userDir)
	assertPathExists(t, localDir)
}

func TestRunUninstallCommandDeletesNestedLocalAICLIDirs(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	withTestHomeAndCWD(t, homeDir, workDir)

	rootLocalDir := filepath.Join(workDir, ".aicli")
	nestedLocalDir := filepath.Join(workDir, "nested", "project", ".aicli")
	writeUninstallTestFile(t, filepath.Join(rootLocalDir, "config.yaml"), "root")
	writeUninstallTestFile(t, filepath.Join(nestedLocalDir, "config.yaml"), "nested")

	result, err := runUninstallCommand(uninstallRequest{Yes: true, LocalOnly: true})
	if err != nil {
		t.Fatalf("runUninstallCommand failed: %v", err)
	}
	if result.DeletedCount != 2 || len(result.Targets) != 2 {
		t.Fatalf("expected two local targets, got %+v", result)
	}
	assertPathMissing(t, rootLocalDir)
	assertPathMissing(t, nestedLocalDir)
}

func TestRunUninstallCommandRejectsConflictingScopeFlags(t *testing.T) {
	_, err := runUninstallCommand(uninstallRequest{DryRun: true, UserOnly: true, LocalOnly: true})
	if err == nil || !strings.Contains(err.Error(), "不能同时使用") {
		t.Fatalf("expected conflicting flag error, got %v", err)
	}
}

func TestValidateUninstallTargetRejectsNonAICLIDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-aicli")
	err := validateUninstallTarget(uninstallTarget{Scope: "test", Path: path})
	if err == nil || !strings.Contains(err.Error(), "非 .aicli") {
		t.Fatalf("expected non-.aicli rejection, got %v", err)
	}
}

func withTestHomeAndCWD(t *testing.T, homeDir, workDir string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	previous := config.UserHomeDirForTest()
	config.SetUserHomeDirForTest(func() (string, error) {
		return homeDir, nil
	})
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		config.SetUserHomeDirForTest(previous)
		_ = os.Chdir(cwd)
	})
}

func writeUninstallTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path to exist %s: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path to be missing %s, stat err=%v", path, err)
	}
}
