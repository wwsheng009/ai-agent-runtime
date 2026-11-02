package remotetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPBannerNamesTheEndpointAndCredentialSource(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "mcp.token")
	if err := os.WriteFile(tokenPath, []byte("super-secret-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.HTTP.TokenFile = tokenPath
	server, err := NewServer(cfg, "banner-test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if !strings.Contains(body, "http://127.0.0.1:43210/mcp") {
		t.Fatalf("banner lacks the endpoint:\n%s", body)
	}
	if !strings.Contains(body, "auth     : bearer token from file") {
		t.Fatalf("banner lacks the credential source:\n%s", body)
	}
	if !strings.Contains(body, "token    : "+tokenPath) {
		t.Fatalf("banner lacks the token file location:\n%s", body)
	}
	if !strings.Contains(body, "aicli chat --remote http://127.0.0.1:43210/mcp") {
		t.Fatalf("banner lacks the connect hint:\n%s", body)
	}
	if strings.Contains(body, "super-secret-token") {
		t.Fatalf("banner leaked the token:\n%s", body)
	}
	if !strings.Contains(body, "workspace: "+dir) {
		t.Fatalf("banner lacks the workspace:\n%s", body)
	}
}

func TestHTTPBannerShowsTokenEnvLocation(t *testing.T) {
	const (
		envName = "AICLI_BANNER_TEST_TOKEN"
		token   = "banner-test-token-0123456789abcdef"
	)
	t.Setenv(envName, token)

	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.HTTP.TokenEnv = envName
	server, err := NewServer(cfg, "banner-token-env")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if !strings.Contains(body, "auth     : bearer token from env") {
		t.Fatalf("banner lacks the credential source:\n%s", body)
	}
	if !strings.Contains(body, "token    : env "+envName) {
		t.Fatalf("banner lacks the token env location:\n%s", body)
	}
	if strings.Contains(body, token) {
		t.Fatalf("banner leaked the token:\n%s", body)
	}
}

func TestHTTPBannerShowsResolvedShell(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	server, err := NewServer(cfg, "banner-shell")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if !strings.Contains(body, "profile  : all (readOnly=false)") {
		t.Fatalf("banner lacks the effective profile:\n%s", body)
	}
	if !strings.Contains(body, "shell    : "+shellLabel(cfg.ResolvedShell)) {
		t.Fatalf("banner lacks the resolved shell:\n%s", body)
	}
	// With no explicit artifacts.root, Normalize now defaults it to a .aicli
	// directory under the workspace, so the missing-store warning must not appear
	// and the recoverable-capture tool must be registered.
	if strings.Contains(body, "artifacts.root is empty") {
		t.Fatalf("banner should not warn about empty artifacts.root (now defaults to workspace .aicli):\n%s", body)
	}
	if !strings.Contains(strings.Join(server.ToolNames(), ","), ToolArtifactRead) {
		t.Fatalf("artifact_read tool not registered under default artifacts.root; tools=%v", server.ToolNames())
	}
}

func TestHTTPBannerShowsNetworkPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Network.Enabled = true
	cfg.Network.AllowedHosts = []string{"example.com"}
	server, err := NewServer(cfg, "banner-network")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if !strings.Contains(body, "network  : enabled (allowed hosts: example.com)") {
		t.Fatalf("banner lacks the network policy:\n%s", body)
	}
}

func TestHTTPBannerWarnsWithoutTokenAndUsesHTTPS(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.HTTP.TLSCert = "cert.pem"
	cfg.HTTP.TLSKey = "key.pem"
	server, err := NewServer(cfg, "banner-tls")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:4443"), "\n")
	if !strings.Contains(body, "https://127.0.0.1:4443/mcp") {
		t.Fatalf("banner did not switch to https:\n%s", body)
	}
	if !strings.Contains(body, "no token configured") {
		t.Fatalf("banner lacks the unauthenticated warning:\n%s", body)
	}
}

func TestHTTPBannerShowsInstanceAndIdentity(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.Gateway.Instance = "backend"
	cfg.Gateway.IdentityFile = filepath.Join(dir, "mount-id")
	server, err := NewServer(cfg, "banner-instance")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if !strings.Contains(body, "instance : backend") {
		t.Fatalf("banner lacks the instance label:\n%s", body)
	}
	if !strings.Contains(body, "identity : "+cfg.Gateway.IdentityFile) {
		t.Fatalf("banner lacks the identity path:\n%s", body)
	}
}

func TestHTTPBannerShellWarningIncludesEnableGuidance(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	server, err := NewServer(cfg, "banner-shell")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	for _, want := range []string{
		"shell is visible but no allowlist is configured",
		"shell.allowedCommands",
		"shell.allowedCommandPatterns",
		"denyShellMetachars",
		"docs/user-guide/aicli-remote-tools.md §7",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("banner warning lacks %q:\n%s", want, body)
		}
	}
}

func TestHTTPBannerShellWarningRespectsReadOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.ReadOnly = true
	server, err := NewServer(cfg, "banner-readonly")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	body := strings.Join(server.httpBannerLines("127.0.0.1:43210"), "\n")
	if strings.Contains(body, "shell is visible but no allowlist") {
		t.Fatalf("readOnly banner must not claim shell is visible:\n%s", body)
	}
}
