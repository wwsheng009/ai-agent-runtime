package remotetools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDefaultsArtifactsRootToWorkspaceAICLIDir(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.Artifacts.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := filepath.Join(dir, ".aicli", "artifacts")
	if cfg.Artifacts.Root != want {
		t.Fatalf("artifacts.root = %q, want %q", cfg.Artifacts.Root, want)
	}
}

func TestNormalizeKeepsExplicitArtifactsRoot(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit-artifacts")
	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.Artifacts.Root = explicit
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Artifacts.Root != explicit {
		t.Fatalf("artifacts.root = %q, want explicit %q", cfg.Artifacts.Root, explicit)
	}
}

func TestNormalizeEnvDDoesNotDefaultArtifactsRoot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Backend = BackendEnvD
	cfg.Workspace = "/srv/app"
	cfg.EnvD.Addr = "127.0.0.1:49983"
	cfg.EnvD.User = "dev"
	cfg.Artifacts.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Artifacts.Root != "" {
		t.Fatalf("envd artifacts.root = %q, want left empty (remote path)", cfg.Artifacts.Root)
	}
}

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

func TestNormalizeShellEnvAllowlistDefaultsToInheritedEnvironment(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Shell.EnvAllowlist != nil {
		t.Fatalf("default env allowlist = %v, want nil (inherit the process environment)", cfg.Shell.EnvAllowlist)
	}
	if cfg.Shell.DisableEnvInheritance {
		t.Fatal("default disableEnvInheritance = true, want false")
	}
}

func TestNormalizeShellEnvAllowlistExplicitEmptyStaysEmpty(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.EnvAllowlist = []string{}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Shell.EnvAllowlist == nil || len(cfg.Shell.EnvAllowlist) != 0 {
		t.Fatalf("explicit empty env allowlist = %#v, want a non-nil empty slice", cfg.Shell.EnvAllowlist)
	}
}

func TestNormalizeShellEnvAllowlistAddsToBaseline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.EnvAllowlist = []string{"APPDATA"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !containsEnvName(cfg.Shell.EnvAllowlist, "PATH") {
		t.Fatalf("configured env allowlist dropped PATH: %v", cfg.Shell.EnvAllowlist)
	}
	if !containsEnvName(cfg.Shell.EnvAllowlist, "APPDATA") {
		t.Fatalf("configured env allowlist dropped APPDATA: %v", cfg.Shell.EnvAllowlist)
	}
}

func TestNormalizeShellEnvAllowlistDisableInheritanceUsesBaseline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.DisableEnvInheritance = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !containsEnvName(cfg.Shell.EnvAllowlist, "PATH") {
		t.Fatalf("baseline env allowlist = %v, want PATH", cfg.Shell.EnvAllowlist)
	}
	if containsEnvName(cfg.Shell.EnvAllowlist, "LOCALAPPDATA") {
		t.Fatalf("disabled inheritance still contains LOCALAPPDATA: %v", cfg.Shell.EnvAllowlist)
	}
}

// TestLoadConfigFileDistinguishesOmittedAndEmptyEnvAllowlist pins the
// nil-vs-empty contract across YAML decoding: omitting the field opts into the
// inherited environment, while an explicit [] keeps the environment empty.
func TestLoadConfigFileDistinguishesOmittedAndEmptyEnvAllowlist(t *testing.T) {
	root := t.TempDir()
	base := fmt.Sprintf("workspace: %s\nshell:\n  allowedCommands: [echo]\n", filepath.ToSlash(root))

	omittedPath := filepath.Join(t.TempDir(), "omitted.yaml")
	if err := os.WriteFile(omittedPath, []byte(base), 0o600); err != nil {
		t.Fatalf("write omitted config: %v", err)
	}
	omitted, err := LoadConfigFile(omittedPath)
	if err != nil {
		t.Fatalf("load omitted config: %v", err)
	}
	if err := omitted.Normalize(); err != nil {
		t.Fatalf("normalize omitted config: %v", err)
	}
	if omitted.Shell.EnvAllowlist != nil {
		t.Fatalf("omitted envAllowlist = %v, want nil (inherit)", omitted.Shell.EnvAllowlist)
	}

	emptyPath := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(emptyPath, []byte(base+"  envAllowlist: []\n"), 0o600); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	empty, err := LoadConfigFile(emptyPath)
	if err != nil {
		t.Fatalf("load empty config: %v", err)
	}
	if err := empty.Normalize(); err != nil {
		t.Fatalf("normalize empty config: %v", err)
	}
	if empty.Shell.EnvAllowlist == nil || len(empty.Shell.EnvAllowlist) != 0 {
		t.Fatalf("explicit [] envAllowlist = %#v, want a non-nil empty slice", empty.Shell.EnvAllowlist)
	}
}

func TestLoadConfigFileDisableEnvInheritanceFallsBackToBaseline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "disable-env.yaml")
	body := fmt.Sprintf("workspace: %s\nshell:\n  allowedCommands: [echo]\n  disableEnvInheritance: true\n", filepath.ToSlash(root))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !cfg.Shell.DisableEnvInheritance {
		t.Fatal("disableEnvInheritance was not decoded")
	}
	if !containsEnvName(cfg.Shell.EnvAllowlist, "PATH") {
		t.Fatalf("disabled inheritance did not restore the baseline: %v", cfg.Shell.EnvAllowlist)
	}
}

func containsEnvName(names []string, want string) bool {
	for _, name := range names {
		if strings.EqualFold(name, want) {
			return true
		}
	}
	return false
}
