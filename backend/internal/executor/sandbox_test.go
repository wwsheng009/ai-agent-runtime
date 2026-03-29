package executor

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSandboxCheckPermission_AllowsAndDeniesPaths(t *testing.T) {
	root := t.TempDir()
	allowedDir := filepath.Join(root, "allowed")
	deniedDir := filepath.Join(root, "denied")
	if err := os.MkdirAll(allowedDir, 0o755); err != nil {
		t.Fatalf("mkdir allowed: %v", err)
	}
	if err := os.MkdirAll(deniedDir, 0o755); err != nil {
		t.Fatalf("mkdir denied: %v", err)
	}

	sandbox := NewSandbox(&SandboxConfig{
		AllowedPaths: []string{allowedDir},
		DeniedPaths:  []string{deniedDir},
	})

	allowedFile := filepath.Join(allowedDir, "file.txt")
	if err := sandbox.CheckPermission(OpRead, allowedFile); err != nil {
		t.Fatalf("expected allowed path, got %v", err)
	}

	deniedFile := filepath.Join(deniedDir, "secret.txt")
	if err := sandbox.CheckPermission(OpRead, deniedFile); err == nil {
		t.Fatal("expected denied path to fail")
	}
}

func TestSandboxCheckPermission_ReadOnlyBlocksWrite(t *testing.T) {
	root := t.TempDir()
	readOnlyDir := filepath.Join(root, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0o755); err != nil {
		t.Fatalf("mkdir readonly: %v", err)
	}

	sandbox := NewSandbox(&SandboxConfig{
		AllowedPaths:  []string{root},
		ReadOnlyPaths: []string{readOnlyDir},
	})

	target := filepath.Join(readOnlyDir, "file.txt")
	if err := sandbox.CheckPermission(OpWrite, target); err == nil {
		t.Fatal("expected write into readonly path to fail")
	}
}

func TestSandboxValidateCommand(t *testing.T) {
	sandbox := NewSandbox(&SandboxConfig{
		AllowedCommands: []string{"git", "sh", "cmd"},
		DeniedCommands:  []string{"powershell"},
	})

	if err := sandbox.ValidateCommand("git"); err != nil {
		t.Fatalf("expected git to be allowed, got %v", err)
	}

	if err := sandbox.ValidateCommand("powershell"); err == nil {
		t.Fatal("expected denied command to fail")
	}

	if err := sandbox.ValidateCommand("python"); err == nil {
		t.Fatal("expected command outside allowlist to fail")
	}
}

func TestSandboxCheckCommandDenied(t *testing.T) {
	sandbox := NewSandbox(&SandboxConfig{
		DeniedCommands: []string{"sh", "powershell"},
	})

	if err := sandbox.CheckCommandDenied("sh"); err == nil {
		t.Fatal("expected denied shell launcher to fail")
	}
	if err := sandbox.CheckCommandDenied("git"); err != nil {
		t.Fatalf("expected git to bypass deny-only check, got %v", err)
	}
}

func TestSandboxCheckURL(t *testing.T) {
	sandbox := NewSandbox(&SandboxConfig{
		AllowedHosts: []string{"example.com"},
		DeniedHosts:  []string{"blocked.com"},
	})

	if err := sandbox.CheckURL("https://api.example.com/search"); err != nil {
		t.Fatalf("expected example.com subdomain to be allowed, got %v", err)
	}
	if err := sandbox.CheckURL("https://blocked.com/data"); err == nil {
		t.Fatal("expected blocked host to fail")
	}
	if err := sandbox.CheckURL("https://other.com/data"); err == nil {
		t.Fatal("expected non-allowlisted host to fail")
	}

	parsed, _ := url.Parse("https://example.com/path")
	if err := sandbox.CheckURL(parsed.String()); err != nil {
		t.Fatalf("expected normalized url to be allowed, got %v", err)
	}
}

func TestSandboxFilterEnv(t *testing.T) {
	sandbox := NewSandbox(&SandboxConfig{
		EnvWhitelist: []string{"PATH", "HOME"},
	})

	filtered := sandbox.FilterEnv([]string{
		"PATH=/tmp/bin",
		"HOME=/tmp/home",
		"SECRET=value",
	})

	if len(filtered) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(filtered))
	}
	for _, entry := range filtered {
		if strings.HasPrefix(entry, "SECRET=") {
			t.Fatalf("unexpected secret env retained: %s", entry)
		}
	}
}

func TestSandboxExecuteCommand(t *testing.T) {
	root := t.TempDir()
	maxExecutionTime := 5 * time.Second
	if runtime.GOOS == "windows" {
		maxExecutionTime = 8 * time.Second
	}
	sandbox := NewSandbox(&SandboxConfig{
		MaxExecutionTime: maxExecutionTime,
		AllowedPaths:     []string{root},
	})

	command := "sh"
	args := []string{"-c", "printf sandbox-ok"}
	if runtime.GOOS == "windows" {
		command = "cmd"
		args = []string{"/c", "echo sandbox-ok"}
	}
	sandbox.config.AllowedCommands = []string{normalizeCommandName(command)}

	output, err := sandbox.ExecuteCommand(context.Background(), command, args, root)
	if err != nil {
		t.Fatalf("expected command to succeed, got %v", err)
	}
	if !strings.Contains(strings.ToLower(output), "sandbox-ok") {
		t.Fatalf("unexpected output: %q", output)
	}
}
