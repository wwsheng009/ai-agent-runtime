package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSkillInstallCommandCopiesSkillFromRoot(t *testing.T) {
	sourceRoot := t.TempDir()
	writeSkillTestFile(t, filepath.Join(sourceRoot, "aicli", "SKILL.md"), "---\nname: aicli\ndescription: test\n---\n")
	writeSkillTestFile(t, filepath.Join(sourceRoot, "aicli", "scripts", "helper.ps1"), "Write-Output ok\n")
	targetRoot := filepath.Join(t.TempDir(), "skills")

	result, _, err := runSkillInstallCommand(skillInstallOptions{
		SkillName: "aicli",
		SourceDir: sourceRoot,
		TargetDir: targetRoot,
	})
	if err != nil {
		t.Fatalf("runSkillInstallCommand: %v", err)
	}
	if !result.Installed || result.TargetDir != filepath.Join(targetRoot, "aicli") {
		t.Fatalf("unexpected install result: %+v", result)
	}
	if result.FileCount != 2 || result.DirCount != 1 {
		t.Fatalf("unexpected copied counts: %+v", result)
	}
	if got := string(mustReadSkillTestFile(t, filepath.Join(targetRoot, "aicli", "SKILL.md"))); !strings.Contains(got, "name: aicli") {
		t.Fatalf("expected copied SKILL.md, got %q", got)
	}
	if got := string(mustReadSkillTestFile(t, filepath.Join(targetRoot, "aicli", "scripts", "helper.ps1"))); !strings.Contains(got, "Write-Output ok") {
		t.Fatalf("expected copied script, got %q", got)
	}
}

func TestRunSkillInstallCommandDryRunDoesNotWrite(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "aicli")
	writeSkillTestFile(t, filepath.Join(sourceDir, "SKILL.md"), "---\nname: aicli\ndescription: test\n---\n")
	targetRoot := filepath.Join(t.TempDir(), "skills")

	result, _, err := runSkillInstallCommand(skillInstallOptions{
		SkillName: "aicli",
		SourceDir: sourceDir,
		TargetDir: targetRoot,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("runSkillInstallCommand: %v", err)
	}
	if !result.DryRun || result.Installed {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, "aicli")); !os.IsNotExist(err) {
		t.Fatalf("expected dry-run not to create target, stat err=%v", err)
	}
}

func TestRunSkillInstallCommandRefusesExistingUnlessForce(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "aicli")
	writeSkillTestFile(t, filepath.Join(sourceDir, "SKILL.md"), "---\nname: aicli\ndescription: new\n---\n")
	targetRoot := filepath.Join(t.TempDir(), "skills")
	writeSkillTestFile(t, filepath.Join(targetRoot, "aicli", "SKILL.md"), "old\n")

	_, _, err := runSkillInstallCommand(skillInstallOptions{
		SkillName: "aicli",
		SourceDir: sourceDir,
		TargetDir: targetRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected existing target error with --force hint, got %v", err)
	}

	result, _, err := runSkillInstallCommand(skillInstallOptions{
		SkillName: "aicli",
		SourceDir: sourceDir,
		TargetDir: targetRoot,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("runSkillInstallCommand force: %v", err)
	}
	if !result.Installed || !result.Overwritten {
		t.Fatalf("expected force overwrite result, got %+v", result)
	}
	if got := string(mustReadSkillTestFile(t, filepath.Join(targetRoot, "aicli", "SKILL.md"))); !strings.Contains(got, "description: new") {
		t.Fatalf("expected overwritten SKILL.md, got %q", got)
	}
}

func TestSkillInstallCommandDefaultsToAICLI(t *testing.T) {
	sourceRoot := t.TempDir()
	writeSkillTestFile(t, filepath.Join(sourceRoot, "aicli", "SKILL.md"), "---\nname: aicli\ndescription: test\n---\n")
	targetRoot := filepath.Join(t.TempDir(), "skills")

	cmd := NewSkillCommand()
	cmd.SetArgs([]string{"install", "--source-dir", sourceRoot, "--target-dir", targetRoot, "--output", "json"})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cmd.Execute: %v", err)
		}
	})

	if !strings.Contains(output, `"skill_name":"aicli"`) {
		t.Fatalf("expected default aicli skill in JSON output, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, "aicli", "SKILL.md")); err != nil {
		t.Fatalf("expected installed SKILL.md: %v", err)
	}
}

func TestResolveSkillInstallTargetRootUsesToolHomes(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	aicliHome := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("AICLI_HOME", aicliHome)

	codexRoot, err := resolveSkillInstallTargetRoot("codex", "")
	if err != nil {
		t.Fatalf("resolve codex root: %v", err)
	}
	if codexRoot != filepath.Join(codexHome, "skills") {
		t.Fatalf("unexpected codex root: %q", codexRoot)
	}

	aicliRoot, err := resolveSkillInstallTargetRoot("aicli", "")
	if err != nil {
		t.Fatalf("resolve aicli root: %v", err)
	}
	if aicliRoot != filepath.Join(aicliHome, "skills") {
		t.Fatalf("unexpected aicli root: %q", aicliRoot)
	}
}

func writeSkillTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func mustReadSkillTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}
