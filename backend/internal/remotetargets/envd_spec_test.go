package remotetargets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// TestParseSpecEnvD covers the envd:// form end to end.
//
// The envd scheme reuses Host/User/Port rather than inventing parallel fields, so
// these cases pin that reuse: if the user ever silently stopped carrying the
// executing account, envd would answer 401 at the first tool call instead of
// failing during parsing.
func TestParseSpecEnvD(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantHost  string
		wantUser  string
		wantPort  int
		wantWS    string
		wantError string
	}{
		{
			name:     "default port",
			raw:      "envd://user@10.0.0.9",
			wantHost: "10.0.0.9",
			wantUser: "user",
			wantPort: DefaultEnvDPort,
		},
		{
			name:     "explicit port",
			raw:      "envd://user@10.0.0.9:51000",
			wantHost: "10.0.0.9",
			wantUser: "user",
			wantPort: 51000,
		},
		{
			name:     "workspace path",
			raw:      "envd://user@10.0.0.9/srv/app",
			wantHost: "10.0.0.9",
			wantUser: "user",
			wantPort: DefaultEnvDPort,
			wantWS:   "/srv/app",
		},
		{
			name:      "invalid port",
			raw:       "envd://user@10.0.0.9:99999",
			wantError: "invalid port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseSpec(tc.raw)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("ParseSpec(%q) returned no error, want one containing %q", tc.raw, tc.wantError)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("ParseSpec(%q) error = %v, want it to contain %q", tc.raw, err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSpec(%q): %v", tc.raw, err)
			}
			if spec.Transport != TransportEnvD {
				t.Errorf("Transport = %q, want %q", spec.Transport, TransportEnvD)
			}
			if spec.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", spec.Host, tc.wantHost)
			}
			if spec.User != tc.wantUser {
				t.Errorf("User = %q, want %q", spec.User, tc.wantUser)
			}
			if spec.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d", spec.Port, tc.wantPort)
			}
			if spec.Workspace != tc.wantWS {
				t.Errorf("Workspace = %q, want %q", spec.Workspace, tc.wantWS)
			}
		})
	}
}

// TestExpandSpecEnvD proves a parsed envd spec expands into a target the registry
// can turn into an MCP entry.
func TestExpandSpecEnvD(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:51000/srv/app")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if expanded == nil || len(expanded.Servers) == 0 {
		t.Fatalf("Expand produced no servers: %+v", expanded)
	}
	var found bool
	for name, cfg := range expanded.Servers {
		if cfg.ExecutionScope != mcpconfig.ExecutionScopeRemote {
			continue
		}
		found = true
		if !strings.Contains(strings.Join(cfg.Args, " "), "10.0.0.9:51000") {
			t.Errorf("server %q args = %q, want the envd address", name, cfg.Args)
		}
	}
	if !found {
		t.Errorf("no remote-scoped server in %+v", expanded.Servers)
	}
}

// TestExpandOneEnvDRequiresUser pins the precondition envd itself enforces.
//
// A target with no user cannot work: envd takes the executing account from HTTP
// Basic credentials and answers `unauthenticated: no user specified` otherwise.
// Failing during expansion turns that into a configuration error instead of a
// confusing 401 at the first tool call.
func TestExpandOneEnvDRequiresUser(t *testing.T) {
	_, _, err := expandOne("envdtest", Target{
		Transport: TransportEnvD,
		SSH:       SSHConfig{Host: "10.0.0.9", Port: DefaultEnvDPort},
		Remote:    RemoteConfig{Profile: "readonly", Workspace: "/srv/app"},
	})
	if err == nil {
		t.Fatal("expandOne accepted an envd target with no user")
	}
	if !strings.Contains(err.Error(), "user") {
		t.Errorf("error = %v, want it to mention the missing user", err)
	}
}

// TestExpandOneEnvDBridge proves the generated MCP entry launches a local bridge
// rather than pointing at envd's port.
//
// envd speaks Connect-RPC, not MCP, so an entry that addressed it directly would be
// a config the runtime could not actually use.
func TestExpandOneEnvDBridge(t *testing.T) {
	cfg, warnings, err := expandOne("envdtest", Target{
		Transport: TransportEnvD,
		SSH:       SSHConfig{Host: "10.0.0.9", Port: DefaultEnvDPort, User: "dev"},
		Remote:    RemoteConfig{Profile: "ops", Workspace: "/srv/app"},
	})
	if err != nil {
		t.Fatalf("expandOne: %v", err)
	}
	if cfg.Type != "stdio" {
		t.Errorf("MCP Type = %q, want stdio (envd needs a local MCP bridge)", cfg.Type)
	}
	if cfg.Command == "" {
		t.Fatal("MCP Command is empty, want the bridge binary")
	}
	if strings.Contains(cfg.Command, "10.0.0.9") {
		t.Errorf("MCP Command = %q, want a local bridge binary, not the remote host", cfg.Command)
	}
	joined := strings.Join(cfg.Args, " ")
	if !strings.Contains(joined, "10.0.0.9:49983") && !strings.Contains(joined, "10.0.0.9:49983 ") {
		t.Errorf("args = %q, want the envd address 10.0.0.9:49983", joined)
	}
	if !strings.Contains(joined, "--user dev") {
		t.Errorf("args = %q, want --user dev", joined)
	}
	if !strings.Contains(joined, "--profile ops") {
		t.Errorf("args = %q, want --profile ops", joined)
	}
	// The warning must say that envd itself enforces nothing, because that is the
	// one fact an operator on this transport has to act on.
	var mentioned bool
	for _, w := range warnings {
		if strings.Contains(w, "no path or command checks") {
			mentioned = true
			break
		}
	}
	if !mentioned {
		t.Errorf("warnings = %v, want one stating envd performs no path or command checks", warnings)
	}
}

// TestParseSpecEnvDTokenEnv covers the environment-variable spelling of the envd
// token: the name travels in the spec, the secret itself never does.
func TestParseSpecEnvDTokenEnv(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?token_env=MY_ENVD_TOKEN")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.EnvDTokenEnv != "MY_ENVD_TOKEN" {
		t.Fatalf("EnvDTokenEnv = %q, want MY_ENVD_TOKEN", spec.EnvDTokenEnv)
	}

	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	if !strings.Contains(args, "--token-env MY_ENVD_TOKEN") {
		t.Fatalf("args = %q, want --token-env MY_ENVD_TOKEN", args)
	}
}

// TestParseSpecEnvDRejectsInlineToken pins the credential rule: envd tokens come
// from the environment, never from the spec string.
func TestParseSpecEnvDRejectsInlineToken(t *testing.T) {
	_, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?token=secret")
	if err == nil {
		t.Fatal("ParseSpec accepted an inline envd token")
	}
	if !strings.Contains(err.Error(), "environment variable") {
		t.Errorf("error = %v, want it to point at the environment variable form", err)
	}
}

// TestExpandSpecEnvDDefaultTokenEnv proves the default environment variable name
// reaches the bridge entry, so nothing depends on an implicit bridge-side
// default.
func TestExpandSpecEnvDDefaultTokenEnv(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	if !strings.Contains(args, "--token-env "+DefaultEnvDTokenEnv) {
		t.Fatalf("args = %q, want --token-env %s", args, DefaultEnvDTokenEnv)
	}
}

// TestExpandSpecEnvDRequiresAbsoluteWorkspace pins the fail-closed default.
//
// `envd://user@host` leaves the workspace empty, and Expand turns that into ".",
// which the bridge cannot use as a path fence: the jail is a prefix comparison
// over cleaned paths, so it needs an absolute root. Failing here beats a jail
// that rejects everything or, worse, one that silently widens.
func TestExpandSpecEnvDRequiresAbsoluteWorkspace(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	_, err = spec.Expand()
	if err == nil {
		t.Fatal("Expand accepted an envd target without an absolute workspace")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v, want it to demand an absolute workspace", err)
	}
}

// TestEnvDTargetNameUsesHost pins the name the tool prefix derives from.
//
// The registry builds "remote_<name>" and the bridge appends "__<tool>". Before
// this was fixed, an envd spec fell through TargetName's URL branch, produced the
// literal name "remote", and real tool calls arrived as "remote_remote__view"
// (observed in a live run on 2026-10-28) — neither readable nor unique per host.
func TestEnvDTargetNameUsesHost(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if got := spec.TargetName(); got != "10-0-0-9" {
		t.Fatalf("TargetName = %q, want 10-0-0-9", got)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for name, cfg := range expanded.Servers {
		if name != "10-0-0-9" {
			t.Errorf("server name = %q, want the host-derived name", name)
		}
		if !strings.Contains(strings.Join(cfg.Args, " "), "--tool-prefix remote_10-0-0-9") {
			t.Errorf("args = %q, want --tool-prefix remote_10-0-0-9", cfg.Args)
		}
	}
}

// TestParseSpecEnvDCallTimeout covers the per-call deadline override.
func TestParseSpecEnvDCallTimeout(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?call_timeout=30m")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.CallTimeout != "30m" {
		t.Fatalf("CallTimeout = %q, want 30m", spec.CallTimeout)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	if !strings.Contains(args, "--call-timeout 30m") {
		t.Fatalf("args = %q, want --call-timeout 30m", args)
	}
}

// TestExpandOneEnvDRejectsInvalidCallTimeout pins the fail-closed parse: a
// malformed duration must not silently fall back to the default.
func TestExpandOneEnvDRejectsInvalidCallTimeout(t *testing.T) {
	_, _, err := expandOne("envdtest", Target{
		Transport: TransportEnvD,
		SSH:       SSHConfig{Host: "10.0.0.9", Port: DefaultEnvDPort, User: "dev"},
		Remote:    RemoteConfig{Profile: "ops", Workspace: "/srv/app", CallTimeout: "soon"},
	})
	if err == nil {
		t.Fatal("expandOne accepted an invalid call timeout")
	}
	if !strings.Contains(err.Error(), "callTimeout") {
		t.Errorf("error = %v, want it to name callTimeout", err)
	}
}

// TestParseSpecEnvDAuditLog covers the local audit path override.
func TestParseSpecEnvDAuditLog(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?audit_log=/tmp/envd-audit.jsonl")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.AuditLog != "/tmp/envd-audit.jsonl" {
		t.Fatalf("AuditLog = %q, want /tmp/envd-audit.jsonl", spec.AuditLog)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	if !strings.Contains(args, "--audit-log /tmp/envd-audit.jsonl") {
		t.Fatalf("args = %q, want --audit-log /tmp/envd-audit.jsonl", args)
	}
}

// TestParseSpecEnvDCAFile covers the TLS switch: a CA file makes the bridge
// address https:// (HTTP/2) instead of plain host:port.
func TestParseSpecEnvDCAFile(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?ca_file=/tmp/envd-ca.pem")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.CAFile != "/tmp/envd-ca.pem" {
		t.Fatalf("CAFile = %q, want /tmp/envd-ca.pem", spec.CAFile)
	}
	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	if !strings.Contains(args, "--addr https://10.0.0.9:49983") {
		t.Errorf("args = %q, want an https address", args)
	}
	if !strings.Contains(args, "--ca-file /tmp/envd-ca.pem") {
		t.Errorf("args = %q, want --ca-file", args)
	}
}

// TestParseSpecEnvDCommandPolicy covers the spec spelling of the shared shell
// policy. Repeated query keys are how one string can carry more than one rule.
func TestParseSpecEnvDCommandPolicy(t *testing.T) {
	spec, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app" +
		"?allow_command=uptime" +
		"&allow_command=host-inspect.sh" +
		"&allow_command_pattern=deploy238_*.sh" +
		"&deny_command=rm" +
		"&deny_command_pattern=sudo%20*" +
		"&allow_shell_metachars=true")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if got := strings.Join(spec.AllowedCommands, ","); got != "uptime,host-inspect.sh" {
		t.Errorf("AllowedCommands = %q, want uptime,host-inspect.sh", got)
	}
	if got := strings.Join(spec.AllowedCommandPatterns, ","); got != "deploy238_*.sh" {
		t.Errorf("AllowedCommandPatterns = %q", got)
	}
	if got := strings.Join(spec.DeniedCommands, ","); got != "rm" {
		t.Errorf("DeniedCommands = %q", got)
	}
	if got := strings.Join(spec.DeniedCommandPatterns, ","); got != "sudo *" {
		t.Errorf("DeniedCommandPatterns = %q", got)
	}
	if !spec.AllowShellMetachars {
		t.Error("AllowShellMetachars = false, want true")
	}

	expanded, err := spec.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var args string
	for _, cfg := range expanded.Servers {
		args = strings.Join(cfg.Args, " ")
	}
	for _, want := range []string{
		"--allow-command uptime",
		"--allow-command host-inspect.sh",
		"--allow-command-pattern deploy238_*.sh",
		"--deny-command rm",
		"--deny-command-pattern sudo *",
		"--deny-shell-metachars=false",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args = %q, want %q", args, want)
		}
	}
}

// TestParseSpecEnvDCommandPolicyRejectsBadBool keeps a typo from silently
// falling back to the safe default while the operator believes metachars are
// allowed.
func TestParseSpecEnvDCommandPolicyRejectsBadBool(t *testing.T) {
	_, err := ParseSpec("envd://user@10.0.0.9:49983/srv/app?allow_shell_metachars=yes-please")
	if err == nil {
		t.Fatal("ParseSpec accepted a non-boolean allow_shell_metachars")
	}
}

// TestExpandOneEnvDWarnsWhenOpsHasNoAllowlist makes the fail-closed default
// visible during expansion, not only at the first shell call.
func TestExpandOneEnvDWarnsWhenOpsHasNoAllowlist(t *testing.T) {
	_, warnings, err := expandOne("envdtest", Target{
		Transport: TransportEnvD,
		SSH:       SSHConfig{Host: "10.0.0.9", Port: DefaultEnvDPort, User: "dev"},
		Remote:    RemoteConfig{Profile: "ops", Workspace: "/srv/app"},
	})
	if err != nil {
		t.Fatalf("expandOne: %v", err)
	}
	var mentioned bool
	for _, warning := range warnings {
		if strings.Contains(warning, "fail-closed") && strings.Contains(warning, "allowedCommands") {
			mentioned = true
			break
		}
	}
	if !mentioned {
		t.Fatalf("warnings = %v, want a fail-closed shell warning", warnings)
	}
}

// TestLoadFileShellPolicyDefaultsDenyMetachars proves a YAML shell block that
// omits denyShellMetachars still gets the safe default before it reaches the
// bridge, where a false value would allow `allowed; anything`.
func TestLoadFileShellPolicyDefaultsDenyMetachars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-targets.yaml")
	body := `remoteTargets:
  envdtest:
    transport: envd
    ssh:
      host: 10.0.0.9
      user: dev
      port: 49983
    remote:
      workspace: /srv/app
      profile: ops
      shell:
        allowedCommands: [uptime]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	expanded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	server := expanded.Servers["envdtest"]
	joined := strings.Join(server.Args, " ")
	if !strings.Contains(joined, "--allow-command uptime") {
		t.Errorf("args = %q, want the allowed command", joined)
	}
	if !strings.Contains(joined, "--deny-shell-metachars=true") {
		t.Errorf("args = %q, want the safe metachar default", joined)
	}
}

// TestExpandOneEnvDTLSUsesHTTPS covers a YAML target that sets tls.caFile
// directly, without going through ParseSpec.
func TestExpandOneEnvDTLSUsesHTTPS(t *testing.T) {
	cfg, _, err := expandOne("envdtest", Target{
		Transport: TransportEnvD,
		SSH:       SSHConfig{Host: "10.0.0.9", Port: DefaultEnvDPort, User: "dev"},
		Remote: RemoteConfig{
			Profile:   "readonly",
			Workspace: "/srv/app",
			TLS:       &TLSFiles{CAFile: "/tmp/envd-ca.pem"},
		},
	})
	if err != nil {
		t.Fatalf("expandOne: %v", err)
	}
	joined := strings.Join(cfg.Args, " ")
	if !strings.Contains(joined, "--addr https://10.0.0.9:49983") {
		t.Errorf("args = %q, want an https address", joined)
	}
	if !strings.Contains(joined, "--ca-file /tmp/envd-ca.pem") {
		t.Errorf("args = %q, want --ca-file", joined)
	}
}
