package ripgrep

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWithPrefersEnvironmentOverride(t *testing.T) {
	rgPath := writeTestExecutable(t, filepath.Join(t.TempDir(), executableName()))
	resolution, err := resolveWith(resolverDeps{
		getenv:     func(string) string { return rgPath },
		executable: func() (string, error) { return "", errors.New("unused") },
		lookPath:   func(string) (string, error) { return "", errors.New("unused") },
		stat:       os.Stat,
	})
	if err != nil {
		t.Fatalf("resolve environment override: %v", err)
	}
	if resolution.Source != SourceEnvironment || resolution.Path != rgPath {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveWithPrefersBundledRipgrepOverPath(t *testing.T) {
	root := t.TempDir()
	executablePath := writeTestExecutable(t, filepath.Join(root, "aicli"))
	bundledPath := writeTestExecutable(t, filepath.Join(root, "codex-path", executableName()))
	pathCandidate := writeTestExecutable(t, filepath.Join(t.TempDir(), executableName()))

	resolution, err := resolveWith(resolverDeps{
		getenv:     func(string) string { return "" },
		executable: func() (string, error) { return executablePath, nil },
		lookPath:   func(string) (string, error) { return pathCandidate, nil },
		stat:       os.Stat,
	})
	if err != nil {
		t.Fatalf("resolve bundled ripgrep: %v", err)
	}
	if resolution.Source != SourceBundled || resolution.Path != bundledPath {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveWithFallsBackToPath(t *testing.T) {
	pathCandidate := writeTestExecutable(t, filepath.Join(t.TempDir(), executableName()))
	resolution, err := resolveWith(resolverDeps{
		getenv:     func(string) string { return "" },
		executable: func() (string, error) { return filepath.Join(t.TempDir(), "aicli"), nil },
		lookPath:   func(string) (string, error) { return pathCandidate, nil },
		stat:       os.Stat,
	})
	if err != nil {
		t.Fatalf("resolve PATH ripgrep: %v", err)
	}
	if resolution.Source != SourcePath || resolution.Path != pathCandidate {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveWithRejectsBrokenExplicitOverride(t *testing.T) {
	_, err := resolveWith(resolverDeps{
		getenv:     func(string) string { return filepath.Join(t.TempDir(), executableName()) },
		executable: func() (string, error) { return "", errors.New("unused") },
		lookPath: func(string) (string, error) {
			return writeTestExecutable(t, filepath.Join(t.TempDir(), executableName())), nil
		},
		stat: os.Stat,
	})
	if err == nil {
		t.Fatal("expected invalid explicit override to fail without PATH fallback")
	}
}

func writeTestExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return abs
}
