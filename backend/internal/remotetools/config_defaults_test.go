package remotetools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDefaultsWorkspace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Workspace == "" || !filepath.IsAbs(cfg.Workspace) {
		t.Fatalf("workspace was not defaulted to an absolute path: %q", cfg.Workspace)
	}
}

func TestNormalizeEnvDStillRequiresWorkspace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Backend = BackendEnvD
	cfg.Workspace = ""
	cfg.EnvD.Addr = "127.0.0.1:49983"
	cfg.EnvD.User = "dev"
	err := cfg.Normalize()
	if err == nil || !strings.Contains(err.Error(), "workspace is required for the envd backend") {
		t.Fatalf("envd with empty workspace error = %v", err)
	}
}

func TestNormalizeListenDefaults(t *testing.T) {
	t.Setenv("AICLI_GATEWAY_TOKEN", "")

	local := DefaultConfig()
	local.HTTP.Listen = ""
	if err := local.Normalize(); err != nil {
		t.Fatalf("local normalize: %v", err)
	}
	if local.HTTP.Listen != "127.0.0.1:18765" {
		t.Fatalf("local listen = %q, want the stable default", local.HTTP.Listen)
	}

	enabled := true
	remote := DefaultConfig()
	remote.HTTP.Listen = ""
	remote.Gateway = GatewayConfig{UseAICLIConfig: &enabled}
	if err := remote.Normalize(); err != nil {
		t.Fatalf("gateway normalize: %v", err)
	}
	if remote.HTTP.Listen != "127.0.0.1:0" {
		t.Fatalf("gateway listen = %q, want a random loopback port", remote.HTTP.Listen)
	}
}
