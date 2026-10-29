package remotetools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSearchPathsAndDiscovery(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	previous := remoteToolsHomeDir
	remoteToolsHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { remoteToolsHomeDir = previous })
	t.Chdir(work)

	project := filepath.Join(work, ".aicli", "remote-tools", "config.yaml")
	user := filepath.Join(home, ".aicli", "remote-tools", "config.yaml")

	paths := ConfigSearchPaths()
	if len(paths) != 2 || paths[0] != project || paths[1] != user {
		t.Fatalf("search paths = %v, want [%s %s]", paths, project, user)
	}
	if got := FindConfigFile(); got != "" {
		t.Fatalf("empty stack returned %q", got)
	}

	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		t.Fatalf("mkdir user: %v", err)
	}
	if err := os.WriteFile(user, []byte("http:\n  path: /from-user\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	if got := FindConfigFile(); got != user {
		t.Fatalf("FindConfigFile = %q, want the user layer", got)
	}

	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(project, []byte("http:\n  path: /from-project\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if got := FindConfigFile(); got != project {
		t.Fatalf("FindConfigFile = %q, want the project layer to win", got)
	}
	loaded, err := LoadConfigFile(project)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if loaded.HTTP.Path != "/from-project" {
		t.Fatalf("path = %q", loaded.HTTP.Path)
	}
}

func TestEnsureTokenFileIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.token")
	created, err := EnsureTokenFile(path)
	if err != nil || !created {
		t.Fatalf("first create = (%v, %v)", created, err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if len(strings.TrimSpace(string(first))) != 64 {
		t.Fatalf("token is not 64 hex chars: %q", first)
	}

	created, err = EnsureTokenFile(path)
	if err != nil || created {
		t.Fatalf("second create = (%v, %v), want (false, nil)", created, err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("token was rotated by a second init")
	}
}

func TestWriteDefaultConfigNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteDefaultConfig(path, true); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "useAICLIConfig: true") || !strings.Contains(body, "path: /mcp") {
		t.Fatalf("default config content:\n%s", body)
	}
	if strings.Contains(body, "workspace:") {
		t.Fatalf("auto config must not pin a workspace:\n%s", body)
	}

	if err := WriteDefaultConfig(path, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write error = %v, want os.ErrExist", err)
	}
}
