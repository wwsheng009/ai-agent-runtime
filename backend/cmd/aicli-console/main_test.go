package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConsoleLauncherArgs(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantTarget    string
		wantForwarded []string
		wantErr       string
	}{
		{
			name:          "separate target value",
			args:          []string{"--target", `C:\Tools\renamed-aicli.exe`, "chat", "--compat-mode"},
			wantTarget:    `C:\Tools\renamed-aicli.exe`,
			wantForwarded: []string{"chat", "--compat-mode"},
		},
		{
			name:          "equals target value",
			args:          []string{`--target=C:\Program Files\AI CLI\aicli-win7.exe`, "version"},
			wantTarget:    `C:\Program Files\AI CLI\aicli-win7.exe`,
			wantForwarded: []string{"version"},
		},
		{
			name:          "last target wins",
			args:          []string{"--target", "first.exe", "--target=second.exe", "chat"},
			wantTarget:    "second.exe",
			wantForwarded: []string{"chat"},
		},
		{
			name:          "double dash stops launcher parsing",
			args:          []string{"chat", "--", "--target", "model-target"},
			wantForwarded: []string{"chat", "--", "--target", "model-target"},
		},
		{
			name:    "missing target value",
			args:    []string{"chat", "--target"},
			wantErr: "requires an executable path",
		},
		{
			name:    "empty equals target",
			args:    []string{"--target="},
			wantErr: "requires a non-empty executable path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, forwarded, err := parseConsoleLauncherArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseConsoleLauncherArgs() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseConsoleLauncherArgs() error = %v", err)
			}
			if target != tt.wantTarget {
				t.Fatalf("target = %q, want %q", target, tt.wantTarget)
			}
			if strings.Join(forwarded, "\x00") != strings.Join(tt.wantForwarded, "\x00") {
				t.Fatalf("forwarded = %#v, want %#v", forwarded, tt.wantForwarded)
			}
		})
	}
}

func TestResolveAICLIExecutableFromPrefersCommandLineTarget(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	explicit := writeTestExecutable(t, filepath.Join(dir, "custom-aicli.exe"))
	environment := writeTestExecutable(t, filepath.Join(dir, "environment-aicli.exe"))
	writeTestExecutable(t, filepath.Join(dir, aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		explicit,
		environment,
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

func TestResolveAICLIExecutableFromPrefersEnvironmentTarget(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	environment := writeTestExecutable(t, filepath.Join(dir, "environment-aicli.exe"))
	writeTestExecutable(t, filepath.Join(dir, aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		"",
		environment,
		func(string) (string, error) {
			t.Fatal("PATH lookup should not run for an environment target")
			return "", errors.New("unreachable")
		},
	)
	if err != nil {
		t.Fatalf("resolveAICLIExecutableFrom() error = %v", err)
	}
	if got != environment {
		t.Fatalf("resolveAICLIExecutableFrom() = %q, want %q", got, environment)
	}
}

func TestResolveAICLIExecutableFromPrefersSibling(t *testing.T) {
	dir := t.TempDir()
	launcher := writeTestExecutable(t, filepath.Join(dir, "aicli-console.exe"))
	sibling := writeTestExecutable(t, filepath.Join(dir, aicliExecutableName()))

	got, err := resolveAICLIExecutableFrom(
		launcher,
		"",
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

	_, err := resolveAICLIExecutableFrom(launcher, launcher, "", nil)
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
