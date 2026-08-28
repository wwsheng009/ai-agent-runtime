package aiclipaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExpandUserPathExpandsCurrentUserHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	if got := ExpandUserPath("~"); got != filepath.Clean(home) {
		t.Fatalf("expected home %q, got %q", filepath.Clean(home), got)
	}

	got := ExpandUserPath("~/.aicli/logs/aicli.log")
	expected := filepath.Join(home, ".aicli", "logs", "aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestExpandUserPathExpandsWindowsSeparatorOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows separator expansion is only meaningful on Windows")
	}

	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	got := ExpandUserPath("~\\.aicli\\logs\\aicli.log")
	expected := filepath.Join(home, ".aicli", "logs", "aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestExpandUserPathLeavesNonCurrentUserTildePathsAlone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	got := ExpandUserPath("~other/.aicli/logs/aicli.log")
	expected := filepath.Clean("~other/.aicli/logs/aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDefaultRuntimeConfigSearchPathsUseActiveProfileFilename(t *testing.T) {
	got := DefaultRuntimeConfigSearchPaths()
	want := []string{
		filepath.Join("configs", DefaultRuntimeConfigFileName),
		filepath.Join("backend", "configs", DefaultRuntimeConfigFileName),
	}
	if len(got) != len(want) {
		t.Fatalf("runtime config search paths = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("runtime config search paths = %v, want %v", got, want)
		}
	}
}

func TestResolveDefaultRuntimeConfigPathFromBaseSupportsBundleAndRepositoryLayouts(t *testing.T) {
	for _, relativePath := range []string{
		filepath.Join("configs", DefaultRuntimeConfigFileName),
		filepath.Join("backend", "configs", DefaultRuntimeConfigFileName),
	} {
		t.Run(relativePath, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, relativePath)
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
			if err := os.WriteFile(configPath, []byte("version: v1\n"), 0o644); err != nil {
				t.Fatalf("write runtime config: %v", err)
			}
			startDir := filepath.Join(root, "work", "nested")
			if err := os.MkdirAll(startDir, 0o755); err != nil {
				t.Fatalf("create start directory: %v", err)
			}

			if got := resolveDefaultRuntimeConfigPathFromBase(startDir); got != configPath {
				t.Fatalf("resolved runtime config = %q, want %q", got, configPath)
			}
		})
	}
}

func TestResolveRuntimeConfigBootstrapPathPreservesExplicitPath(t *testing.T) {
	explicit := filepath.Join("custom", "runtime.yaml")
	if got := ResolveRuntimeConfigBootstrapPath(explicit); got != explicit {
		t.Fatalf("explicit runtime config = %q, want %q", got, explicit)
	}
}

func TestDatePartitionUsesLocalYMD(t *testing.T) {
	stamp := time.Date(2026, 7, 25, 15, 4, 5, 0, time.Local)
	year, month, day := DatePartition(stamp)
	if year != "2026" || month != "07" || day != "25" {
		t.Fatalf("unexpected partition: %s/%s/%s", year, month, day)
	}
}

func TestJoinDatePartitionNestsUnderYMD(t *testing.T) {
	stamp := time.Date(2026, 7, 25, 15, 4, 5, 0, time.Local)
	got := JoinDatePartition(filepath.Join("root", "chat-logs"), stamp, "session-id", "debug.log")
	want := filepath.Join("root", "chat-logs", "2026", "07", "25", "session-id", "debug.log")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseTimestampedSessionIDTime(t *testing.T) {
	chatID := "20260725_150405.123_ab12cd34"
	got, ok := ParseTimestampedSessionIDTime(chatID)
	if !ok {
		t.Fatal("expected chat log session id to parse")
	}
	if got.Format("20060102_150405.000") != "20260725_150405.123" {
		t.Fatalf("unexpected chat id time: %v", got)
	}

	fileID := "session_20260725150405_abcdEF12"
	got, ok = ParseTimestampedSessionIDTime(fileID)
	if !ok {
		t.Fatal("expected file session id to parse")
	}
	if got.Format("20060102150405") != "20260725150405" {
		t.Fatalf("unexpected file id time: %v", got)
	}
}

func isolateHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
