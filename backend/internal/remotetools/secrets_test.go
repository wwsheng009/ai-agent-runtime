package remotetools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSecretFileWinsOverEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv("AICLI_TEST_SECRET", "env-secret")

	got, err := ResolveSecret("test.tokenFile", "AICLI_TEST_SECRET", path)
	if err != nil {
		t.Fatalf("ResolveSecret(file): %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("file secret = %q, want file-secret (trailing newline trimmed)", got)
	}

	got, err = ResolveSecret("test.tokenEnv", "AICLI_TEST_SECRET", "")
	if err != nil || got != "env-secret" {
		t.Fatalf("env fallback = %q, %v", got, err)
	}

	got, err = ResolveSecret("test.none", "", "")
	if err != nil || got != "" {
		t.Fatalf("unconfigured = %q, %v; want empty", got, err)
	}
}

func TestResolveSecretFileFailsClosed(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing")
	if _, err := ResolveSecret("test", "", missing); err == nil || !strings.Contains(err.Error(), "read secret file") {
		t.Fatalf("missing file error = %v", err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("write empty token: %v", err)
	}
	if _, err := ResolveSecret("test", "", empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file error = %v", err)
	}

	if _, err := ResolveSecret("test", "", dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory error = %v", err)
	}
}

func TestResolveSecretRejectsWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission bits do not express ownership")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if _, err := ResolveSecret("test", "", path); err == nil || !strings.Contains(err.Error(), "too permissive") {
		t.Fatalf("0644 file error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if got, err := ResolveSecret("test", "", path); err != nil || got != "secret" {
		t.Fatalf("0600 file = %q, %v", got, err)
	}
}
