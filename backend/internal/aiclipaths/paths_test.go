package aiclipaths

import (
	"path/filepath"
	"runtime"
	"testing"
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

func isolateHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
