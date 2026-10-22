package remotetargets

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
)

// DefaultSpecPort is the port aicli-remote-tools serves on by default.
const DefaultSpecPort = 18765

// Spec is the parsed form of the one-argument remote target syntax.
//
// It exists because the full remoteTargets + mcp.yaml pair is the right shape for
// a fleet of servers but far too much ceremony for "point this session at that
// box". A spec is the whole declaration in one string:
//
//	http://10.0.0.9:18765            HTTP, token from the environment
//	http://10.0.0.9:18765#ops        same, with the ops capability grant
//	ssh://ops@10.0.0.9               SSH stdio, read-only
//	ssh://ops@10.0.0.9:2222#standard SSH stdio on a non-default port
type Spec struct {
	// Transport is TransportHTTP or TransportSSHStdio.
	Transport string
	// Depth is the capability grant.
	Depth SpecDepth

	// HTTP fields.
	URL string

	// SSH fields.
	Host   string
	User   string
	Port   int
	// Workspace is the remote directory the target serves. Empty means "let the
	// remote server decide" (its own --workspace / config default).
	Workspace string
	// Binary is the remote aicli-remote-tools path. Empty means "appears on PATH
	// as aicli-remote-tools".
	Binary string
	// IdentityFile is the SSH private key. Empty falls back to ssh-agent/defaults.
	IdentityFile string
}

// ParseSpec parses the compact remote target syntax.
//
// Grammar:
//
//	<scheme>://[user@]host[:port][/path][#depth][?workspace=...&binary=...&identity=...]
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

	spec := &Spec{Depth: DepthReadonly}
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
	default:
		return nil, fmt.Errorf("remote spec %q must start with http://, https:// or ssh://", raw)
	}

	if depthToken != "" {
		depth := SpecDepth(strings.ToLower(strings.TrimSpace(depthToken)))
		switch depth {
		case DepthReadonly, DepthStandard, DepthOps:
			spec.Depth = depth
		default:
			return nil, fmt.Errorf("remote spec %q has an unknown depth %q (want readonly|standard|ops)", raw, depthToken)
		}
	}

	if parsed.RawQuery != "" {
		query := parsed.Query()
		spec.Workspace = firstNonEmpty(query.Get("workspace"), spec.Workspace)
		spec.Binary = query.Get("binary")
		spec.IdentityFile = query.Get("identity")
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
	if s.Transport == TransportSSHStdio {
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
			Workspace:   s.Workspace,
			ToolPrefix:  prefix,
			Profile:     string(s.Depth),
			DescriptionStyle: "explicit",
		},
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
