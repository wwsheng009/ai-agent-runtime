package remotetargets_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

// writeTempDeclaration drops a declaration body into a throwaway directory and
// returns its path, so the parser (not just Expand) is exercised.
func writeTempDeclaration(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote-targets.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestExpandSSHStdio locks the stdio entry shape.
func TestExpandSSHStdio(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"dev": {
			Transport: remotetargets.TransportSSHStdio,
			SSH: remotetargets.SSHConfig{
				Host: "dev.internal",
				User: "deploy",
				Port: 2222,
			},
			Remote: remotetargets.RemoteConfig{
				Binary:     "/usr/local/bin/aicli-remote-tools",
				Workspace:  "/srv/my app", // deliberately contains a space
				Profile:    "ops",
				ToolPrefix: "remote_dev",
			},
		},
	}}

	expanded, err := remotetargets.Expand(file)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	server, ok := expanded.Servers["dev"]
	if !ok {
		t.Fatalf("missing server: %v", expanded.Servers)
	}
	if server.Type != "stdio" {
		t.Fatalf("unexpected type: %s", server.Type)
	}
	if server.Command != "ssh-client" {
		t.Fatalf("unexpected command: %s", server.Command)
	}
	if server.ExecutionScope != mcpconfig.ExecutionScopeRemote {
		t.Fatalf("executionScope must be remote, got %q", server.ExecutionScope)
	}
	if server.TrustLevel != mcpconfig.MCPTrustLevelTrustedRemote {
		t.Fatalf("unexpected trust level: %s", server.TrustLevel)
	}

	joined := strings.Join(server.Args, " ")
	if !strings.Contains(joined, "-T") {
		t.Fatal("expected -T (no PTY) in args")
	}
	if !strings.Contains(joined, "--stdio") {
		t.Fatal("expected --stdio in args")
	}
	if !strings.Contains(joined, "deploy@dev.internal") {
		t.Fatalf("expected ssh target in args: %s", joined)
	}
	// The workspace must travel inside the base64 payload, never as a bare shell
	// token (review B4).
	if strings.Contains(joined, "/srv/my app") {
		t.Fatalf("workspace must not appear verbatim in the command line: %s", joined)
	}
	if !strings.Contains(joined, "--config-b64") {
		t.Fatal("expected --config-b64 in args")
	}
}

// TestExpandSSHHTTP uses a local forward and a token header.
func TestExpandSSHHTTP(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"prod": {
			Transport: remotetargets.TransportSSHHTTP,
			SSH: remotetargets.SSHConfig{
				Host:         "prod.internal",
				User:         "ops",
				LocalForward: "127.0.0.1:18765",
			},
			Remote: remotetargets.RemoteConfig{
				Workspace:  "/srv/app",
				Profile:    "readonly",
				ToolPrefix: "remote_prod",
				TokenEnv:   "AICLI_REMOTE_TOKEN_PROD",
			},
		},
	}}

	expanded, err := remotetargets.Expand(file)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	server := expanded.Servers["prod"]
	if server.Type != "streamable" {
		t.Fatalf("unexpected type: %s", server.Type)
	}
	if server.URL != "http://127.0.0.1:18765/mcp" {
		t.Fatalf("unexpected url: %s", server.URL)
	}
	if got := server.Headers["Authorization"]; got != "Bearer ${AICLI_REMOTE_TOKEN_PROD}" {
		t.Fatalf("unexpected auth header: %q", got)
	}
}

// TestExpandRejectsMissingWorkspace locks fail-closed validation.
func TestExpandRejectsMissingWorkspace(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"bad": {Transport: remotetargets.TransportSSHStdio, SSH: remotetargets.SSHConfig{Host: "h"}},
	}}
	if _, err := remotetargets.Expand(file); err == nil {
		t.Fatal("expected missing workspace to fail")
	}
}

// TestExpandRejectsUnknownTransport locks transport validation.
func TestExpandRejectsUnknownTransport(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"bad": {
			Transport: "carrier-pigeon",
			Remote:    remotetargets.RemoteConfig{Workspace: "/srv"},
		},
	}}
	if _, err := remotetargets.Expand(file); err == nil {
		t.Fatal("expected unknown transport to fail")
	}
}

// TestExpandMultiplexesTargets locks multi-target support: several targets expand
// into several distinct servers without colliding.
func TestExpandMultiplexesTargets(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"dev": {
			Transport: remotetargets.TransportSSHStdio,
			SSH:       remotetargets.SSHConfig{Host: "dev.internal"},
			Remote:    remotetargets.RemoteConfig{Workspace: "/srv/dev", ToolPrefix: "remote_dev"},
		},
		"prod": {
			Transport: remotetargets.TransportSSHHTTP,
			SSH:       remotetargets.SSHConfig{Host: "prod.internal", LocalForward: "127.0.0.1:18766"},
			Remote:    remotetargets.RemoteConfig{Workspace: "/srv/prod", ToolPrefix: "remote_prod"},
		},
		"k8s": {
			Transport: remotetargets.TransportHTTP,
			Remote: remotetargets.RemoteConfig{
				Workspace:  "/opt/manifests",
				ToolPrefix: "remote_k8s",
				Listen:     "127.0.0.1:18767",
			},
		},
	}}

	expanded, err := remotetargets.Expand(file)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(expanded.Servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(expanded.Servers))
	}
	// Each transport expands to its own entry shape.
	if expanded.Servers["dev"].Type != "stdio" {
		t.Fatalf("dev must be stdio, got %s", expanded.Servers["dev"].Type)
	}
	if expanded.Servers["prod"].Type != "streamable" {
		t.Fatalf("prod must be streamable, got %s", expanded.Servers["prod"].Type)
	}
	if expanded.Servers["k8s"].URL != "http://127.0.0.1:18767/mcp" {
		t.Fatalf("unexpected k8s url: %s", expanded.Servers["k8s"].URL)
	}
}

// TestApplyToConfigRefusesOverwrite locks the "never shadow" rule.
func TestApplyToConfigRefusesOverwrite(t *testing.T) {
	expanded, err := remotetargets.Expand(remotetargets.File{
		RemoteTargets: map[string]remotetargets.Target{
			"dev": {
				Transport: remotetargets.TransportSSHStdio,
				SSH:       remotetargets.SSHConfig{Host: "dev"},
				Remote:    remotetargets.RemoteConfig{Workspace: "/srv"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"dev": {Name: "hand-written"},
	}}
	if err := expanded.ApplyToConfig(cfg); err == nil {
		t.Fatal("expected an overwrite of a hand-written server to fail")
	}
}

// TestApplyToConfigAddsServers locks the additive merge.
func TestApplyToConfigAddsServers(t *testing.T) {
	expanded, err := remotetargets.Expand(remotetargets.File{
		RemoteTargets: map[string]remotetargets.Target{
			"dev": {
				Transport: remotetargets.TransportSSHStdio,
				SSH:       remotetargets.SSHConfig{Host: "dev"},
				Remote:    remotetargets.RemoteConfig{Workspace: "/srv"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"curated": {Name: "curated"},
	}}
	if err := expanded.ApplyToConfig(cfg); err != nil {
		t.Fatalf("ApplyToConfig: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("expected 1 existing + 1 generated server, got %d", len(cfg.MCPServers))
	}
	if cfg.MCPServers["dev"].ExecutionScope != mcpconfig.ExecutionScopeRemote {
		t.Fatal("generated server must declare remote scope")
	}
}

// TestExpandNonLoopbackRequiresToken locks the http fail-closed rule.
func TestExpandNonLoopbackRequiresToken(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"bad": {
			Transport: remotetargets.TransportHTTP,
			Remote: remotetargets.RemoteConfig{
				Workspace: "/srv",
				Listen:    "0.0.0.0:18765",
			},
		},
	}}
	if _, err := remotetargets.Expand(file); err == nil {
		t.Fatal("expected non-loopback http without tokenEnv to fail")
	}
}

// TestExpandRejectsLongPrefix locks the canonical-name budget.
func TestExpandRejectsLongPrefix(t *testing.T) {
	file := remotetargets.File{RemoteTargets: map[string]remotetargets.Target{
		"bad": {
			Transport: remotetargets.TransportSSHStdio,
			SSH:       remotetargets.SSHConfig{Host: "h"},
			Remote: remotetargets.RemoteConfig{
				Workspace:  "/srv",
				ToolPrefix: strings.Repeat("a", 40),
			},
		},
	}}
	if _, err := remotetargets.Expand(file); err == nil {
		t.Fatal("expected an over-long prefix to fail")
	}
}

// TestPolicyBlockStillLoadsButIsReported locks the migration path for a file
// written against the removed local grant layer.
//
// Two failure modes are both unacceptable, so both are pinned:
//   - the file stops parsing, which bricks a working session over dead keys;
//   - the file parses and the keys are silently ignored, which leaves the reader
//     believing allowWrite: false is enforcing something.
//
// A target that still declares `policy:` therefore expands normally AND warns.
func TestPolicyBlockStillLoadsButIsReported(t *testing.T) {
	const body = `
remoteTargets:
  legacy:
    transport: ssh-stdio
    ssh:
      host: dev
    remote:
      workspace: /srv
      toolPrefix: remote_dev
    policy:
      allowRead: true
      allowWrite: false
      subagentTargets: [researcher]
`
	expanded, err := remotetargets.LoadFile(writeTempDeclaration(t, body))
	if err != nil {
		t.Fatalf("a legacy policy block must still load: %v", err)
	}
	if _, ok := expanded.Servers["legacy"]; !ok {
		t.Fatalf("the target must still expand into a server: %v", expanded.Servers)
	}
	found := false
	for _, warning := range expanded.Warnings {
		if strings.Contains(warning, "policy") && strings.Contains(warning, "no longer gates") {
			found = true
			if !strings.Contains(warning, "allowWrite") || !strings.Contains(warning, "subagentTargets") {
				t.Fatalf("the warning must name the ignored keys, got: %s", warning)
			}
		}
	}
	if !found {
		t.Fatalf("expected a deprecation warning, got %v", expanded.Warnings)
	}
}

// TestNoPolicyBlockProducesNoDeprecationWarning keeps the warning honest: a file
// that never wrote the block must not be nagged about it.
func TestNoPolicyBlockProducesNoDeprecationWarning(t *testing.T) {
	const body = `
remoteTargets:
  clean:
    transport: ssh-stdio
    ssh:
      host: dev
    remote:
      workspace: /srv
      toolPrefix: remote_dev
`
	expanded, err := remotetargets.LoadFile(writeTempDeclaration(t, body))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	for _, warning := range expanded.Warnings {
		if strings.Contains(warning, "no longer gates") {
			t.Fatalf("a declaration without `policy:` must not warn about it: %s", warning)
		}
	}
}
