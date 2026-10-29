package remotetools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func optionalNames(cfg *Config) map[string]bool {
	names := map[string]bool{}
	for _, name := range cfg.enabledTools() {
		names[name] = true
	}
	return names
}

func TestOptionalToolsAreOptInAndProfileGated(t *testing.T) {
	base := DefaultConfig()
	base.Workspace = t.TempDir()
	if err := base.Normalize(); err != nil {
		t.Fatalf("base normalize: %v", err)
	}
	if got := optionalNames(base); len(got) != 14 || got[ToolMultiedit] || got[ToolFetch] {
		t.Fatalf("default surface changed: %v", got)
	}

	standard := DefaultConfig()
	standard.Workspace = t.TempDir()
	standard.Profile = ProfileStandard
	standard.OptionalTools.Enabled = []string{"*"}
	standard.Network.Enabled = true
	if err := standard.Normalize(); err != nil {
		t.Fatalf("standard normalize: %v", err)
	}
	standardNames := optionalNames(standard)
	if !standardNames[ToolMultiedit] || !standardNames[ToolDownload] {
		t.Fatalf("standard missing write optional tools: %v", standardNames)
	}
	if standardNames[ToolFetch] || standardNames[ToolSourcegraph] {
		t.Fatalf("standard exposed ops-only network tools: %v", standardNames)
	}

	ops := DefaultConfig()
	ops.Workspace = t.TempDir()
	ops.Profile = ProfileOps
	ops.OptionalTools.Enabled = []string{"*"}
	ops.Network.Enabled = true
	if err := ops.Normalize(); err != nil {
		t.Fatalf("ops normalize: %v", err)
	}
	opsNames := optionalNames(ops)
	for _, name := range []string{ToolMultiedit, ToolDownload, ToolFetch, ToolSourcegraph} {
		if !opsNames[name] {
			t.Fatalf("ops missing %q: %v", name, opsNames)
		}
	}

	readonly := DefaultConfig()
	readonly.Workspace = t.TempDir()
	readonly.Profile = ProfileReadonly
	readonly.OptionalTools.Enabled = []string{"*"}
	readonly.Network.Enabled = true
	if err := readonly.Normalize(); err != nil {
		t.Fatalf("readonly normalize: %v", err)
	}
	for name := range optionalNames(readonly) {
		if _, ok := optionalToolByName(name); ok {
			t.Fatalf("readonly exposed optional tool %q", name)
		}
	}
}

func TestLoadConfigFileSelectsOptionalToolsAndNetwork(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "remote.yaml")
	body := fmt.Sprintf(`workspace: %s
profile: ops
optionalTools:
  enabled: [multiedit, fetch]
network:
  enabled: true
  allowedHosts: [example.com]
`, filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := strings.Join(cfg.OptionalToolNames(), ","); got != "multiedit,fetch" {
		t.Fatalf("optional tools = %q", got)
	}
	if !cfg.Network.Enabled || len(cfg.Network.AllowedHosts) != 1 || cfg.Network.AllowedHosts[0] != "example.com" {
		t.Fatalf("network config = %+v", cfg.Network)
	}
}

func TestNormalizeRejectsUnknownOptionalTool(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.OptionalTools.Enabled = []string{"multiedt"}
	err := cfg.Normalize()
	if err == nil || !strings.Contains(err.Error(), "unknown optional tool") {
		t.Fatalf("unknown optional tool error = %v", err)
	}
}

func TestOptionalNetworkToolsRequireNetworkEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.OptionalTools.Enabled = []string{"*"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	names := optionalNames(cfg)
	if !names[ToolMultiedit] {
		t.Fatalf("multiedit should not require network: %v", names)
	}
	for _, name := range []string{ToolDownload, ToolFetch, ToolSourcegraph} {
		if names[name] {
			t.Fatalf("network tool %q exposed while network is disabled", name)
		}
	}
}

func TestOptionalToolsRespectExplicitToolAllowlist(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.OptionalTools.Enabled = []string{"*"}
	cfg.Network.Enabled = true
	cfg.Tools = []string{ToolMultiedit}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	names := optionalNames(cfg)
	if len(names) != 1 || !names[ToolMultiedit] {
		t.Fatalf("tools allowlist did not narrow optional surface: %v", names)
	}
	if got := cfg.OptionalToolNames(); len(got) != 1 || got[0] != ToolMultiedit {
		t.Fatalf("OptionalToolNames did not reflect tools narrowing: %v", got)
	}
}

func TestOptionalToolsOnlyAvailableForLocalBackend(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Backend = BackendEnvD
	cfg.Workspace = "/srv/app"
	cfg.EnvD.Addr = "127.0.0.1:49983"
	cfg.EnvD.User = "dev"
	cfg.OptionalTools.Enabled = []string{"*"}
	cfg.Network.Enabled = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for name := range optionalNames(cfg) {
		if _, ok := optionalToolByName(name); ok {
			t.Fatalf("envd backend exposed local optional tool %q", name)
		}
	}
}

func TestOptionalToolsAreRegisteredInServerSurface(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileOps
	cfg.OptionalTools.Enabled = []string{"*"}
	cfg.Network.Enabled = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	server, err := NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	names := map[string]bool{}
	for _, name := range server.ToolNames() {
		names[name] = true
	}
	for _, name := range []string{ToolMultiedit, ToolDownload, ToolFetch, ToolSourcegraph} {
		if !names[name] {
			t.Fatalf("optional tool %q is not registered: %v", name, server.ToolNames())
		}
	}
}

func TestOptionalMultieditRejectsPathOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Workspace = root
	cfg.OptionalTools.Enabled = []string{ToolMultiedit}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	tools, err := buildOptionalTools(cfg, workspace, sandbox)
	if err != nil {
		t.Fatalf("buildOptionalTools: %v", err)
	}
	var multiedit *remoteOptionalTool
	for _, tool := range tools {
		if wrapper, ok := tool.(*remoteOptionalTool); ok && wrapper.Name() == ToolMultiedit {
			multiedit = wrapper
		}
	}
	if multiedit == nil {
		t.Fatalf("multiedit was not constructed: %v", toolNames(cfg))
	}

	result, callErr := multiedit.Execute(t.Context(), map[string]interface{}{
		"file_path": outside,
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": "do not touch",
				"new_string": "touched",
			},
		},
	})
	if callErr != nil {
		t.Fatalf("multiedit returned protocol error: %v", callErr)
	}
	if result == nil || result.Success {
		t.Fatalf("outside path was accepted: %+v", result)
	}
	if !strings.Contains(result.Error.Error(), "denied by the remote workspace") {
		t.Fatalf("unexpected refusal: %v", result.Error)
	}
	if got := readOptionalTestFile(t, outside); got != "do not touch\n" {
		t.Fatalf("outside file changed: %q", got)
	}
}

func TestOptionalMultieditEditsInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Workspace = root
	cfg.OptionalTools.Enabled = []string{ToolMultiedit}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	tools, err := buildOptionalTools(cfg, workspace, sandbox)
	if err != nil {
		t.Fatalf("buildOptionalTools: %v", err)
	}
	var multiedit *remoteOptionalTool
	for _, tool := range tools {
		if wrapper, ok := tool.(*remoteOptionalTool); ok && wrapper.Name() == ToolMultiedit {
			multiedit = wrapper
		}
	}
	if multiedit == nil {
		t.Fatal("multiedit was not constructed")
	}
	result, callErr := multiedit.Execute(t.Context(), map[string]interface{}{
		"file_path": "note.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_string": "before", "new_string": "after"},
		},
	})
	if callErr != nil {
		t.Fatalf("multiedit returned protocol error: %v", callErr)
	}
	if result == nil || !result.Success {
		t.Fatalf("inside-workspace edit failed: %+v", result)
	}
	if got := readOptionalTestFile(t, target); got != "after\n" {
		t.Fatalf("target content = %q, want after", got)
	}
}

func TestRemoteSandboxNetworkPolicyCoversOptionalTools(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Network.Enabled = true
	cfg.Network.AllowedHosts = []string{"example.com"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	if err := sandbox.ExecutorSandbox().CheckURL("https://example.com/x"); err != nil {
		t.Fatalf("allowed host rejected: %v", err)
	}
	if err := sandbox.ExecutorSandbox().CheckURL("https://other.example/x"); err == nil {
		t.Fatal("host outside allowlist was accepted")
	}

	cfg.Network.Enabled = false
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("re-normalize: %v", err)
	}
	sandbox, err = NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("NewSandbox disabled: %v", err)
	}
	if err := sandbox.ExecutorSandbox().CheckURL("https://example.com/x"); err == nil {
		t.Fatal("network check was accepted while network was disabled")
	}
}

func TestOptionalFetchUsesRemoteNetworkPolicy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from optional fetch"))
	}))
	defer upstream.Close()

	build := func(allowed []string) *remoteOptionalTool {
		t.Helper()
		cfg := DefaultConfig()
		cfg.Workspace = t.TempDir()
		cfg.OptionalTools.Enabled = []string{ToolFetch}
		cfg.Network.Enabled = true
		cfg.Network.AllowedHosts = allowed
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		sandbox, err := NewSandbox(cfg, workspace)
		if err != nil {
			t.Fatalf("NewSandbox: %v", err)
		}
		tools, err := buildOptionalTools(cfg, workspace, sandbox)
		if err != nil {
			t.Fatalf("buildOptionalTools: %v", err)
		}
		for _, tool := range tools {
			if wrapper, ok := tool.(*remoteOptionalTool); ok && wrapper.Name() == ToolFetch {
				return wrapper
			}
		}
		t.Fatal("fetch was not constructed")
		return nil
	}

	allowed := build([]string{"127.0.0.1"})
	result, err := allowed.Execute(t.Context(), map[string]interface{}{"url": upstream.URL})
	if err != nil {
		t.Fatalf("fetch protocol error: %v", err)
	}
	if result == nil || !result.Success || !strings.Contains(result.Content, "hello from optional fetch") {
		t.Fatalf("allowed fetch failed: %+v", result)
	}

	denied := build([]string{"example.com"})
	result, err = denied.Execute(t.Context(), map[string]interface{}{"url": upstream.URL})
	if err != nil {
		t.Fatalf("denied fetch protocol error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("fetch outside network allowlist was accepted: %+v", result)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not allowed") {
		t.Fatalf("unexpected network refusal: %v", result.Error)
	}
}

func readOptionalTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
