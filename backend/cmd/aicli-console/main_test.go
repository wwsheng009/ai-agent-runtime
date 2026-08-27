package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAICLIExecutableFromPrefersExplicitTarget(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	explicit := writeTestExecutable(t, filepath.Join(dir, "custom-aicli.exe"))
	writeTestExecutable(t, filepath.Join(dir, aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		explicit,
		func(string) (string, error) {
			t.Fatal("PATH lookup should not run for an explicit target")
			return "", errors.New("unreachable")
		},
	)
	if err != nil {
		t.Fatalf("resolveAICLIExecutableFrom() error = %v", err)
	}
	if got != explicit {
		t.Fatalf("resolveAICLIExecutableFrom() = %q, want %q", got, explicit)
	}
}

func TestResolveAICLIExecutableFromPrefersSibling(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	sibling := writeTestExecutable(t, filepath.Join(dir, aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		"",
		func(string) (string, error) {
			t.Fatal("PATH lookup should not run when the sibling exists")
			return "", errors.New("unreachable")
		},
	)
	if err != nil {
		t.Fatalf("resolveAICLIExecutableFrom() error = %v", err)
	}
	if got != sibling {
		t.Fatalf("resolveAICLIExecutableFrom() = %q, want %q", got, sibling)
	}
}

func TestResolveAICLIExecutableFromFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	pathTarget := writeTestExecutable(t, filepath.Join(t.TempDir(), aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		"",
		func(name string) (string, error) {
			if name != aicliExecutableName() {
				t.Fatalf("PATH lookup name = %q, want %q", name, aicliExecutableName())
			}
			return pathTarget, nil
		},
	)
	if err != nil {
		t.Fatalf("resolveAICLIExecutableFrom() error = %v", err)
	}
	if got != pathTarget {
		t.Fatalf("resolveAICLIExecutableFrom() = %q, want %q", got, pathTarget)
	}
}

func TestResolveAICLIExecutableFromRejectsLauncherAsTarget(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))

	_, err := resolveAICLIExecutableFrom(launcher, launcher, nil)
	if err == nil || !strings.Contains(err.Error(), "launcher itself") {
		t.Fatalf("resolveAICLIExecutableFrom() error = %v, want launcher recursion error", err)
	}
}

func TestResolveAICLIExecutableFromReportsMissingTarget(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))

	_, err := resolveAICLIExecutableFrom(
		launcher,
		"",
		func(string) (string, error) { return "", errors.New("not found") },
	)
	if err == nil {
		t.Fatal("resolveAICLIExecutableFrom() error = nil, want missing target error")
	}
	if !strings.Contains(err.Error(), targetEnvironmentVariable) {
		t.Fatalf("missing target error = %q, want %s guidance", err, targetEnvironmentVariable)
	}
}

func writeTestExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatalf("write test executable %q: %v", path, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("make test executable path absolute: %v", err)
	}
	return absolute
}
