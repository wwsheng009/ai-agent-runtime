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
	if !strings.Contains(body, "token from file "+tokenPath) {
		t.Fatalf("banner lacks the credential source:\n%s", body)
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
