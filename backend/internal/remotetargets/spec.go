package remotetargets

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// SpecDepth selects the capability grant of a spec-derived target.
type SpecDepth string

const (
	// DepthReadonly exposes the four read tools and grants read only.
	DepthReadonly SpecDepth = "readonly"
	// DepthStandard adds the file-mutation tools and grants read+write.
	DepthStandard SpecDepth = "standard"
	// DepthOps adds shell and grants read+write+shell.
	DepthOps SpecDepth = "ops"
	// DepthAll exposes every tool family the remote backend/store can provide.
	// It is the default so an agent is not silently downgraded to read-only.
	DepthAll SpecDepth = "all"
)

// DefaultSpecPort is the port aicli-remote-tools serves on by default.
const DefaultSpecPort = 18765

// DefaultEnvDPort is the port envd listens on by default.
//
// It differs from DefaultSpecPort because it is not ours to choose: envd is a
// third-party daemon that already picked 49983, and a spec that made the caller
// repeat it would be noise.
const DefaultEnvDPort = 49983

// DefaultEnvDTokenEnv is the environment variable the envd bridge reads the
// access token from when a spec does not name one via ?token_env=NAME.
const DefaultEnvDTokenEnv = "AICLI_ENVD_TOKEN"

// Spec is the parsed form of the one-argument remote target syntax.
//
// It exists because the full remoteTargets + mcp.yaml pair is the right shape for
// a fleet of servers but far too much ceremony for "point this session at that
// box". A spec is the whole declaration in one string:
//
//	http://10.0.0.9:18765            HTTP, token from the environment
//	http://10.0.0.9:18765#ops        same, with the ops capability grant
//	ssh://ops@10.0.0.9               SSH stdio, all tools (default)
//	ssh://ops@10.0.0.9:2222#standard SSH stdio on a non-default port
type Spec struct {
	// Transport is TransportHTTP or TransportSSHStdio.
	Transport string
	// Depth is the capability grant.
	Depth SpecDepth
	// DepthExplicit records whether the caller wrote a #depth marker. HTTP
	// targets only warn about a profile when it was explicitly requested; the
	// default all is the server's own default and must not produce noise.
	DepthExplicit bool

	// HTTP fields.
	URL string

	// SSH fields.
	Host string
	User string
	Port int
	// Workspace is the remote directory the target serves. Empty means "let the
	// remote server decide" (its own --workspace / config default).
	Workspace string
	// Binary is the remote aicli-remote-tools path. Empty means "appears on PATH
	// as aicli-remote-tools".
	Binary string
	// IdentityFile is the SSH private key. Empty falls back to ssh-agent/defaults.
	IdentityFile string

	// EnvDTokenEnv names the local environment variable holding the envd access
	// token, used as the HTTP Basic password. Empty falls back to
	// DefaultEnvDTokenEnv. When that variable is also unset, envd is assumed to be
	// running with auth disabled, which is only acceptable on a loopback or
	// firewalled port.
	//
	// The token lives in an environment variable rather than in the spec string
	// for the same reason SSH credentials do: a spec is pasted into shell history
	// and into issue reports, and a secret in either is a leak.
	//
	// The name is EnvDTokenEnv rather than TokenEnv because Spec already has a
	// TokenEnv method for the aicli-remote-tools bearer token; a field of that name
	// would collide with it.
	EnvDTokenEnv string

	// CallTimeout is the envd bridge's per-call deadline as a Go duration string
	// ("30m"). Empty keeps the bridge default. It is part of the spec because
	// long migrations and backups are normal operations, not an edge case.
	CallTimeout string

	// AuditLog is the local file the bridge appends one JSONL metadata record per
	// tool call to. Empty disables auditing. It never contains argument or result
	// payloads.
	AuditLog string

	// CAFile is the local PEM bundle used to verify the envd TLS certificate.
	// Setting it also switches the generated bridge address to https://, which is
	// what enables HTTP/2 and the client-streaming RPCs.
	CAFile string

	// Command policy for an envd target. These travel as repeated query keys
	// (allow_command, allow_command_pattern, deny_command,
	// deny_command_pattern) and feed the same ShellConfig used by
	// aicli-remote-tools. An empty allowlist is fail-closed.
	AllowedCommands        []string
	AllowedCommandPatterns []string
	DeniedCommands         []string
	DeniedCommandPatterns  []string
	// AllowShellMetachars opts out of the default refusal of pipes, redirection,
	// substitution and command sequencing.
	AllowShellMetachars bool
}

// ParseSpec parses the compact remote target syntax.
//
// Grammar:
//
//	<scheme>://[user@]host[:port][/path][#depth][?workspace=...&binary=...&identity=...]
//
// envd targets additionally accept ?token_env=NAME, naming the environment
// variable the bridge reads the access token from. An inline ?token= is refused:
// a spec is pasted into shell history and issue reports, and a secret in either is
// a leak.
//
// envd targets also accept ?call_timeout=<duration> (for example 30m) to override
// the bridge's per-call deadline.
//
// envd targets also accept ?audit_log=<local-path> to enable the bridge's JSONL
// audit trail (metadata only).
//
// envd targets also accept ?ca_file=<local-pem> to verify a TLS endpoint; the
// generated bridge address becomes https:// and negotiates HTTP/2.
//
// envd targets also accept repeated ?allow_command=, ?allow_command_pattern=,
// ?deny_command= and ?deny_command_pattern= values; they use the same matching
// rules as aicli-remote-tools' shell block. ?allow_shell_metachars=true is the
// explicit opt-out from the metacharacter refusal. Values must be URL-encoded.
//
// The query keys mirror the fields of SSHConfig / RemoteConfig that are commonly
// non-default; everything else keeps its documented default.
func ParseSpec(raw string) (*Spec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty remote spec")
	}

	// Split off the fragment first: url.Parse treats `#` as a fragment, which is
	// what the depth marker is, but keeping the split explicit makes the error
	// messages name the offending part.
	body := trimmed
	depthToken := ""
	if idx := strings.LastIndex(body, "#"); idx >= 0 {
		depthToken = body[idx+1:]
		body = body[:idx]
	}

	parsed, err := url.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse remote spec %q: %w", raw, err)
	}

	spec := &Spec{Depth: DepthAll}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		spec.Transport = TransportHTTP
		if strings.TrimSpace(parsed.Host) == "" {
			return nil, fmt.Errorf("remote spec %q has no host", raw)
		}
		spec.URL = body
	case "ssh":
		spec.Transport = TransportSSHStdio
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return nil, fmt.Errorf("remote spec %q has no host", raw)
		}
		spec.Host = parsed.Hostname()
		spec.User = parsed.User.Username()
		if portText := parsed.Port(); portText != "" {
			port, convErr := strconv.Atoi(portText)
			if convErr != nil || port <= 0 || port > 65535 {
				return nil, fmt.Errorf("remote spec %q has an invalid port %q", raw, portText)
			}
			spec.Port = port
		}
		// `ssh://host/srv/app` carries the remote workspace as the path.
		if path := strings.TrimSpace(parsed.Path); path != "" && path != "/" {
			spec.Workspace = path
		}
	case "envd":
		spec.Transport = TransportEnvD
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return nil, fmt.Errorf("remote spec %q has no host", raw)
		}
		spec.Host = parsed.Hostname()
		// envd resolves the executing account from HTTP Basic credentials, so the
		// user here is the OS user envd should run as, not an SSH login name. It is
		// reused rather than duplicated because it means the same thing to the
		// operator: "who runs the work on that box".
		spec.User = parsed.User.Username()
		if portText := parsed.Port(); portText != "" {
			port, convErr := strconv.Atoi(portText)
			if convErr != nil || port <= 0 || port > 65535 {
				return nil, fmt.Errorf("remote spec %q has an invalid port %q", raw, portText)
			}
			spec.Port = port
		} else {
			spec.Port = DefaultEnvDPort
		}
		// `envd://host/srv/app` carries the working directory as the path, matching
		// the ssh:// form.
		if path := strings.TrimSpace(parsed.Path); path != "" && path != "/" {
			spec.Workspace = path
		}
	default:
		return nil, fmt.Errorf("remote spec %q must start with http://, https://, ssh:// or envd://", raw)
	}

	if depthToken != "" {
		depth := SpecDepth(strings.ToLower(strings.TrimSpace(depthToken)))
		switch depth {
		case DepthReadonly, DepthStandard, DepthOps, DepthAll:
			spec.Depth = depth
			spec.DepthExplicit = true
		default:
			return nil, fmt.Errorf("remote spec %q has an unknown depth %q (want all|readonly|standard|ops)", raw, depthToken)
		}
	}

	if parsed.RawQuery != "" {
		query := parsed.Query()
		spec.Workspace = firstNonEmpty(query.Get("workspace"), spec.Workspace)
		spec.Binary = query.Get("binary")
		spec.IdentityFile = query.Get("identity")
		if spec.Transport == TransportEnvD {
			if query.Has("token") {
				return nil, fmt.Errorf("remote spec %q carries an inline token; envd tokens must come from an environment variable (use ?token_env=NAME)", raw)
			}
			spec.EnvDTokenEnv = strings.TrimSpace(query.Get("token_env"))
			spec.CallTimeout = strings.TrimSpace(query.Get("call_timeout"))
			spec.AuditLog = strings.TrimSpace(query.Get("audit_log"))
			spec.CAFile = strings.TrimSpace(query.Get("ca_file"))
			spec.AllowedCommands = append([]string(nil), query["allow_command"]...)
			spec.AllowedCommandPatterns = append([]string(nil), query["allow_command_pattern"]...)
			spec.DeniedCommands = append([]string(nil), query["deny_command"]...)
			spec.DeniedCommandPatterns = append([]string(nil), query["deny_command_pattern"]...)
			if value := strings.TrimSpace(query.Get("allow_shell_metachars")); value != "" {
				allowed, err := strconv.ParseBool(value)
				if err != nil {
					return nil, fmt.Errorf("remote spec %q has an invalid allow_shell_metachars value %q", raw, value)
				}
				spec.AllowShellMetachars = allowed
			}
		}
	}
	return spec, nil
}

// TokenEnv returns the environment variable the bearer token is read from.
//
// It is a constant rather than a spec field because a token must never travel on
// a command line: it would land in the shell history and in the process list.
func (s *Spec) TokenEnv() string { return "AICLI_REMOTE_TOKEN" }

// TargetName is the display name used in descriptions and audit records.
func (s *Spec) TargetName() string {
	if s == nil {
		return "remote"
	}
	// ssh-stdio and envd both identify a target by host; the other transports
	// carry the host inside URL.
	if s.Transport == TransportSSHStdio || s.Transport == TransportEnvD {
		if s.Host != "" {
			return sanitizeServerName(s.Host)
		}
		return "remote"
	}
	if parsed, err := url.Parse(s.URL); err == nil && parsed.Hostname() != "" {
		return sanitizeServerName(parsed.Hostname())
	}
	return "remote"
}

// Expand turns a spec into the same Expanded shape the YAML declaration produces,
// so the downstream MCP config merge works unchanged.
//
// The generated target uses a tool prefix derived from the host name. That prefix
// is not cosmetic: tools.Manager visits MCP tools before local ones and only the
// read family (ls/glob/grep/view) is locally protected, so an unprefixed remote
// `write` would shadow the local one.
//
// `#depth` becomes the target's PROFILE, which is the one place it can honestly
// mean something:
//
//   - ssh-stdio: the profile travels in the base64 config and the server starts
//     with it, so `#readonly` really does expose only the read family;
//   - http: the server is already running with its own profile and the local side
//     cannot change it, so depth is advisory here (Expand reports that as a
//     warning rather than silently implying a restriction was applied).
//
// The per-target capability grant this used to also produce was removed: remote
// tools are gated by the session permission mode exactly like local ones.
func (s *Spec) Expand() (*Expanded, error) {
	if s == nil {
		return nil, fmt.Errorf("remote spec is nil")
	}
	name := s.TargetName()
	prefix := "remote_" + name

	target := Target{
		Transport: s.Transport,
		Remote: RemoteConfig{
			Workspace:        s.Workspace,
			ToolPrefix:       prefix,
			DescriptionStyle: "explicit",
		},
	}
	if s.DepthExplicit {
		target.Remote.Profile = string(s.Depth)
	}

	switch s.Transport {
	case TransportHTTP:
		listen := s.listenAddress()
		target.Remote.Listen = listen
		target.Remote.TokenEnv = s.TokenEnv()
	case TransportSSHStdio:
		target.SSH = SSHConfig{
			Host:         s.Host,
			User:         s.User,
			Port:         s.Port,
			IdentityFile: s.IdentityFile,
		}
		target.Remote.Binary = s.Binary
	case TransportEnvD:
		// envd carries the host, the executing user and the port the same way an
		// SSH target does, so they travel in the SSH block rather than a parallel
		// one. Reusing the fields keeps the config surface small and keeps the
		// "who runs the work, on which box" question answered in one place.
		//
		// Binary names the local bridge to launch, not a program on the target:
		// envd speaks Connect-RPC, not MCP, so it can only be reached through an
		// adapter. Registry.expandOne turns this into a stdio MCP entry.
		target.SSH = SSHConfig{
			Host: s.Host,
			User: s.User,
			Port: s.Port,
		}
		target.Remote.Binary = s.Binary
		target.Remote.CallTimeout = s.CallTimeout
		target.Remote.AuditLog = s.AuditLog
		if len(s.AllowedCommands) > 0 || len(s.AllowedCommandPatterns) > 0 ||
			len(s.DeniedCommands) > 0 || len(s.DeniedCommandPatterns) > 0 ||
			s.AllowShellMetachars {
			target.Remote.Shell = &remotetools.ShellConfig{
				AllowedCommands:        append([]string(nil), s.AllowedCommands...),
				AllowedCommandPatterns: append([]string(nil), s.AllowedCommandPatterns...),
				DeniedCommands:         append([]string(nil), s.DeniedCommands...),
				DeniedCommandPatterns:  append([]string(nil), s.DeniedCommandPatterns...),
				DenyShellMetachars:     !s.AllowShellMetachars,
			}
		}
		if s.CAFile != "" {
			target.Remote.TLS = &TLSFiles{CAFile: s.CAFile}
		}
		// Empty falls back to the bridge's own default so the generated entry
		// names the environment variable explicitly instead of relying on an
		// implicit one.
		target.Remote.TokenEnv = firstNonEmpty(s.EnvDTokenEnv, DefaultEnvDTokenEnv)
	default:
		return nil, fmt.Errorf("unsupported transport %q", s.Transport)
	}

	// Expand itself rejects an empty workspace, and the remote server would refuse
	// to start without one. "." is resolved by the remote side against the login
	// user's home, so `--remote http://host` and `--remote ssh://ops@host` are both
	// usable with no extra flags while still being an explicit, auditable value.
	if strings.TrimSpace(target.Remote.Workspace) == "" {
		target.Remote.Workspace = "."
	}

	return Expand(File{RemoteTargets: map[string]Target{name: target}})
}

// listenAddress extracts the host:port the HTTP target listens on.
func (s *Spec) listenAddress() string {
	parsed, err := url.Parse(s.URL)
	if err != nil || parsed.Host == "" {
		return fmt.Sprintf("127.0.0.1:%d", DefaultSpecPort)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = strconv.Itoa(DefaultSpecPort)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host + ":" + port
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
