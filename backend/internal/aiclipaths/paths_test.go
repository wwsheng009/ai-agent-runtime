package aiclipaths

import (
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
