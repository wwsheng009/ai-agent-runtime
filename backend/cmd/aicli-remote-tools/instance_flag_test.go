package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func writeInstanceConfig(t *testing.T, workspace, tokenFile, identityFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf(
		"workspace: %s\nhttp:\n  tokenFile: %s\ngateway:\n  url: wss://gw\n  identityFile: %s\n  selfName: self\n  name: shared-node\n",
		filepath.ToSlash(workspace), filepath.ToSlash(tokenFile), filepath.ToSlash(identityFile),
	)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestInstanceFlagOverridesSharedConfigIdentityAndLabels(t *testing.T) {
	dir := t.TempDir()
	configPath := writeInstanceConfig(t, dir, filepath.Join(dir, "mcp.token"), filepath.Join(dir, "shared-mount-id"))

	cfg, err := loadConfig(&serveFlags{configPath: configPath, instance: "backend"})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	wantIdentity, err := remotetools.InstanceIdentityPath("backend")
	if err != nil {
		t.Fatalf("InstanceIdentityPath: %v", err)
	}
	if cfg.Gateway.IdentityFile != wantIdentity {
		t.Fatalf("identity = %q, want %q", cfg.Gateway.IdentityFile, wantIdentity)
	}
	if cfg.Gateway.SelfName != "backend" || cfg.Gateway.Name != "backend" {
		t.Fatalf("labels = selfName %q, name %q", cfg.Gateway.SelfName, cfg.Gateway.Name)
	}
}

func TestInstanceFlagKeepsExplicitIdentityOverride(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit-mount-id")
	configPath := writeInstanceConfig(t, dir, filepath.Join(dir, "mcp.token"), filepath.Join(dir, "shared-mount-id"))

	cfg, err := loadConfig(&serveFlags{
		configPath:      configPath,
		instance:        "backend",
		gatewayIdentity: explicit,
		gatewaySelfName: "self",
	})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Gateway.IdentityFile != explicit {
		t.Fatalf("explicit identity = %q, want %q", cfg.Gateway.IdentityFile, explicit)
	}
	if cfg.Gateway.SelfName != "self" {
		t.Fatalf("explicit selfName = %q, want self", cfg.Gateway.SelfName)
	}
}
