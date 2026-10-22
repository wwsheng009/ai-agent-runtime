// Package remotetargets expands a declarative `remoteTargets` block into the
// MCP server entries and per-target tool policy the local runtime consumes.
//
// The design (docs/plan/aicli-remote-tools-ssh-http-design-20261021.md §13)
// keeps the target declaration out of mcp.yaml on purpose: a target carries SSH
// credentials, a forward port and a capability grant, none of which belong in the
// generic MCP editor (review B6). The expander is the single bridge between that
// declaration and the runtime's existing MCP configuration shape.
package remotetargets

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// Transport kinds.
const (
	TransportSSHStdio = "ssh-stdio"
	TransportSSHHTTP  = "ssh-http"
	TransportHTTP     = "http"
)

// Target is one remote server declaration.
type Target struct {
	// Transport selects how the local runtime reaches the target.
	Transport string       `yaml:"transport" json:"transport"`
	SSH       SSHConfig    `yaml:"ssh,omitempty" json:"ssh,omitempty"`
	Remote    RemoteConfig `yaml:"remote" json:"remote"`
	// Policy records whether a `policy:` block was written. It carries no
	// capability semantics any more: a remote tool is classified by its own
	// attributes and gated by the session's permission mode, exactly like a local
	// tool, so a local per-target grant would be a second, contradicting answer to
	// "may this write happen". The field survives only so the block can be
	// reported as ignored instead of silently dropped.
	Policy deprecatedPolicy `yaml:"policy,omitempty" json:"policy,omitempty"`
}

// SSHConfig describes the SSH hop.
type SSHConfig struct {
	Host           string `yaml:"host" json:"host"`
	User           string `yaml:"user" json:"user"`
	Port           int    `yaml:"port,omitempty" json:"port,omitempty"`
	IdentityFile   string `yaml:"identityFile,omitempty" json:"identityFile,omitempty"`
	KnownHostsFile string `yaml:"knownHostsFile,omitempty" json:"knownHostsFile,omitempty"`
	// LocalForward is the loopback address used by the ssh-http transport.
	LocalForward string `yaml:"localForward,omitempty" json:"localForward,omitempty"`
}

// RemoteConfig describes the target-side server.
type RemoteConfig struct {
	// Binary is the remote executable path.
	Binary string `yaml:"binary" json:"binary"`
	// Workspace / AllowedRoots are the target-side jail.
	Workspace    string   `yaml:"workspace" json:"workspace"`
	AllowedRoots []string `yaml:"allowedRoots,omitempty" json:"allowedRoots,omitempty"`
	// Profile selects the exposed tool family.
	Profile string `yaml:"profile,omitempty" json:"profile,omitempty"`
	// ToolPrefix is prepended to every tool name.
	ToolPrefix string `yaml:"toolPrefix" json:"toolPrefix"`
	// Listen is the daemon address for the http transports.
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty"`
	// TokenEnv names the local environment variable holding the bearer token.
	TokenEnv string `yaml:"tokenEnv,omitempty" json:"tokenEnv,omitempty"`
	// DescriptionStyle is compact|explicit.
	DescriptionStyle string `yaml:"descriptionStyle,omitempty" json:"descriptionStyle,omitempty"`
	// TLS carries the client-certificate material for the http transports.
	// A target behind a proxy that strips Authorization headers authenticates by
	// certificate instead of the bearer token.
	TLS *TLSFiles `yaml:"tls,omitempty" json:"tls,omitempty"`
}

// TLSFiles mirrors mcpconfig.MCPTLSConfig so a target declaration can own its
// certificate paths.
type TLSFiles struct {
	CAFile             string `yaml:"caFile,omitempty" json:"caFile,omitempty"`
	CertFile           string `yaml:"certFile,omitempty" json:"certFile,omitempty"`
	KeyFile            string `yaml:"keyFile,omitempty" json:"keyFile,omitempty"`
	ServerName         string `yaml:"serverName,omitempty" json:"serverName,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify,omitempty" json:"insecureSkipVerify,omitempty"`
}

// deprecatedPolicy remembers that a `policy:` block was written.
//
// The local per-target capability grant was removed: a remote tool is classified
// by its own attributes (write-like / shell-like / background) and gated by the
// session's permission mode, exactly like a local or ordinary MCP tool. Keeping a
// second, hand-written answer to "may this write happen" contradicted that, and
// `subagentTargets` added a third layer no caller could reach.
//
// The shape is still parsed so an existing file loads without error AND the keys
// are reported as ignored. Silently accepting a block that no longer does
// anything is how this file ended up with dead configuration once before.
type deprecatedPolicy struct {
	// Declared is true when the block appeared at all.
	Declared bool
	// Keys lists the field names written inside it, for the warning text.
	Keys []string
}

// UnmarshalYAML records the block without interpreting it.
func (p *deprecatedPolicy) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	p.Declared = true
	if value.Kind == yaml.MappingNode {
		// A mapping's children alternate key, value; only the keys are names.
		for i := 0; i+1 < len(value.Content); i += 2 {
			name := strings.TrimSpace(value.Content[i].Value)
			if name != "" {
				p.Keys = append(p.Keys, name)
			}
		}
		return nil
	}
	// Anything else (scalar, sequence) is still "a policy block was written".
	return nil
}

// File is the top-level `remoteTargets` document.
type File struct {
	RemoteTargets map[string]Target `yaml:"remoteTargets" json:"remoteTargets"`
}

// Expanded is the result of expanding one file.
type Expanded struct {
	// Servers are the generated MCP entries, keyed by server name.
	Servers map[string]mcpconfig.MCPConfig
	// Warnings describe lossy or ignored configuration.
	Warnings []string
}

// LoadFile reads and expands a remoteTargets file.
func LoadFile(path string) (*Expanded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read remote targets: %w", err)
	}
	var file File
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse remote targets: %w", err)
	}
	return Expand(file)
}

// Expand validates every target and produces MCP entries.
//
// Validation is fail-closed: an unknown transport, a missing workspace, a
// duplicate server name or an over-long canonical tool name aborts the whole
// expansion rather than silently dropping a target.
func Expand(file File) (*Expanded, error) {
	result := &Expanded{
		Servers: map[string]mcpconfig.MCPConfig{},
	}
	if len(file.RemoteTargets) == 0 {
		return result, nil
	}

	names := make([]string, 0, len(file.RemoteTargets))
	for name := range file.RemoteTargets {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		target := file.RemoteTargets[name]
		serverName := sanitizeServerName(name)
		if serverName == "" {
			return nil, fmt.Errorf("remote target %q has an unusable name", name)
		}
		if _, exists := result.Servers[serverName]; exists {
			return nil, fmt.Errorf("remote target %q collides with another target on server name %q", name, serverName)
		}
		server, warnings, err := expandOne(serverName, target)
		if err != nil {
			return nil, fmt.Errorf("remote target %q: %w", name, err)
		}
		result.Servers[serverName] = server
		if warning := deprecatedPolicyWarning(name, target.Policy); warning != "" {
			warnings = append(warnings, warning)
		}
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result, nil
}

// deprecatedPolicyWarning names a `policy:` block that no longer has any effect.
//
// Reported rather than ignored: a file that still carries allowWrite/subagentTargets
// would otherwise read as if it were enforcing something, and the reader has no way
// to discover that nothing here gates anything any more.
func deprecatedPolicyWarning(name string, policy deprecatedPolicy) string {
	if !policy.Declared {
		return ""
	}
	keys := strings.Join(policy.Keys, ", ")
	if keys == "" {
		keys = "(empty)"
	}
	return fmt.Sprintf("target %q: the `policy:` block (%s) no longer gates anything; "+
		"remote tools are governed by the session permission mode like local ones - "+
		"use --permission-mode, or narrow the tool surface with the target's own profile",
		name, keys)
}

// expandOne turns one target into an MCP server entry.
func expandOne(serverName string, target Target) (mcpconfig.MCPConfig, []string, error) {
	transport := strings.ToLower(strings.TrimSpace(target.Transport))
	workspace := strings.TrimSpace(target.Remote.Workspace)
	if workspace == "" {
		return mcpconfig.MCPConfig{}, nil, fmt.Errorf("remote.workspace is required")
	}
	prefix := strings.TrimSpace(target.Remote.ToolPrefix)
	if prefix == "" {
		prefix = "remote_" + serverName
	}
	if err := validateToolPrefix(prefix); err != nil {
		return mcpconfig.MCPConfig{}, nil, err
	}

	cfg := mcpconfig.MCPConfig{
		Name:             serverName,
		Type:             "stdio",
		TrustLevel:       mcpconfig.MCPTrustLevelTrustedRemote,
		ExecutionScope:   mcpconfig.ExecutionScopeRemote,
		MaxParallelCalls: 4,
		Enabled:          true,
	}
	var warnings []string

	switch transport {
	case TransportSSHStdio:
		if strings.TrimSpace(target.SSH.Host) == "" {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("ssh.host is required")
		}
		binary := strings.TrimSpace(target.Remote.Binary)
		if binary == "" {
			binary = "aicli-remote-tools"
		}
		// The remote server config travels as base64 JSON so a workspace path
		// containing spaces or quotes never enters a shell grammar (review B4).
		remoteConfig := map[string]interface{}{
			"workspace":        workspace,
			"toolPrefix":       prefix,
			"targetName":       serverName,
			"profile":          orDefault(target.Remote.Profile, "readonly"),
			"descriptionStyle": orDefault(target.Remote.DescriptionStyle, "explicit"),
		}
		if len(target.Remote.AllowedRoots) > 0 {
			remoteConfig["allowedRoots"] = target.Remote.AllowedRoots
		}
		encoded, err := encodeRemoteConfig(remoteConfig)
		if err != nil {
			return mcpconfig.MCPConfig{}, nil, err
		}

		args := make([]string, 0, 16)
		args = append(args, "-T")
		if port := target.SSH.Port; port > 0 {
			args = append(args, "-p", fmt.Sprintf("%d", port))
		}
		if identity := strings.TrimSpace(target.SSH.IdentityFile); identity != "" {
			args = append(args, "-i", identity)
		}
		if known := strings.TrimSpace(target.SSH.KnownHostsFile); known != "" {
			args = append(args, "-o", "UserKnownHostsFile="+known)
		}
		// Dead-link detection. `ssh-client` only enables keepalive by default in
		// -N (forward-only) mode; without these options a silently dropped SSH
		// link can hang a stdio MCP call until the OS TCP timeout.
		args = append(args, "-o", "ServerAliveInterval=15")
		args = append(args, "-o", "ServerAliveCountMax=3")
		args = append(args, "--stdio")
		args = append(args, sshTarget(target.SSH))
		args = append(args, "--")
		args = append(args, binary, "serve", "--stdio", "--config-b64", encoded)

		cfg.Type = "stdio"
		cfg.Command = "ssh-client"
		cfg.Args = args
		warnings = append(warnings,
			fmt.Sprintf("target %q: the remote profile %q must be started with the same workspace in the server config",
				serverName, orDefault(target.Remote.Profile, "readonly")))

	case TransportSSHHTTP:
		if strings.TrimSpace(target.SSH.Host) == "" {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("ssh.host is required")
		}
		listen := orDefault(target.Remote.Listen, "127.0.0.1:18765")
		forward := orDefault(target.SSH.LocalForward, listen)
		url := httpURL(forward)
		cfg.Type = "streamable"
		cfg.URL = url
		if tokenEnv := strings.TrimSpace(target.Remote.TokenEnv); tokenEnv != "" {
			cfg.Headers = map[string]string{"Authorization": "Bearer ${" + tokenEnv + "}"}
		}
		warnings = append(warnings,
			fmt.Sprintf("target %q: start `ssh-client -N -L %s %s` before the first call",
				serverName, forward, sshTarget(target.SSH)))

	case TransportHTTP:
		listen := strings.TrimSpace(target.Remote.Listen)
		if listen == "" {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("remote.listen is required for transport http")
		}
		cfg.Type = "streamable"
		cfg.URL = httpURL(listen)
		if tokenEnv := strings.TrimSpace(target.Remote.TokenEnv); tokenEnv != "" {
			cfg.Headers = map[string]string{"Authorization": "Bearer ${" + tokenEnv + "}"}
		}
		if !isLoopbackListen(listen) && strings.TrimSpace(target.Remote.TokenEnv) == "" {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("non-loopback remote.listen requires remote.tokenEnv")
		}
		// An HTTP target is already running with its own profile; the local side
		// cannot narrow it, so a declared profile here would look like a restriction
		// that is not applied. Naming it beats the alternative: `--remote
		// http://host#standard` used to print a depth and stay silent about the fact
		// that `#standard` changes nothing on an already-serving target.
		if profile := strings.TrimSpace(target.Remote.Profile); profile != "" && profile != "readonly" {
			warnings = append(warnings,
				fmt.Sprintf("target %q: profile %q is not applied to an http target; the tool family is fixed by the running server (use `#depth` with ssh-stdio, or set the server's own profile)",
					serverName, profile))
		}

	default:
		return mcpconfig.MCPConfig{}, nil, fmt.Errorf("unknown transport %q (want ssh-stdio|ssh-http|http)", target.Transport)
	}

	// TLS material applies to both http transports. It is validated here rather
	// than at connect time so a missing certificate is a config error.
	if target.Remote.TLS != nil {
		cfg.TLS = &mcpconfig.MCPTLSConfig{
			CAFile:             target.Remote.TLS.CAFile,
			CertFile:           target.Remote.TLS.CertFile,
			KeyFile:            target.Remote.TLS.KeyFile,
			ServerName:         target.Remote.TLS.ServerName,
			InsecureSkipVerify: target.Remote.TLS.InsecureSkipVerify,
		}
		if (strings.TrimSpace(cfg.TLS.CertFile) == "") != (strings.TrimSpace(cfg.TLS.KeyFile) == "") {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("remote.tls.certFile and remote.tls.keyFile must be set together")
		}
		if cfg.TLS.InsecureSkipVerify && !isLoopbackListen(cfg.URL) {
			return mcpconfig.MCPConfig{}, nil, fmt.Errorf("remote.tls.insecureSkipVerify is only allowed for a loopback endpoint")
		}
	}

	return cfg, warnings, nil
}

// encodeRemoteConfig serializes the target-side config for --config-b64.
func encodeRemoteConfig(remote map[string]interface{}) (string, error) {
	encoded, err := json.Marshal(remote)
	if err != nil {
		return "", fmt.Errorf("encode remote config: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

// sshTarget renders the [user@]host part of an ssh-client invocation.
func sshTarget(cfg SSHConfig) string {
	host := strings.TrimSpace(cfg.Host)
	user := strings.TrimSpace(cfg.User)
	if user == "" {
		return host
	}
	return user + "@" + host
}

// httpURL turns a listen or forward address into an MCP endpoint URL.
func httpURL(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		return strings.TrimSuffix(address, "/") + "/mcp"
	}
	if !strings.Contains(address, ":") {
		address += ":18765"
	}
	return "http://" + address + "/mcp"
}

// isLoopbackListen reports whether the listen address is loopback-only.
func isLoopbackListen(address string) bool {
	host := address
	if idx := strings.LastIndex(address, ":"); idx > 0 {
		host = address[:idx]
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// validateToolPrefix rejects prefixes that would break the canonical name budget.
func validateToolPrefix(prefix string) error {
	// mcp__<server>__<prefix>__<tool> must stay inside 64 characters. The longest
	// P3 tool name is job_cancel (10). Reserve the rest for the server name.
	const maxPrefix = 24
	if len(prefix) > maxPrefix {
		return fmt.Errorf("toolPrefix %q is longer than %d characters", prefix, maxPrefix)
	}
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("toolPrefix %q must match [A-Za-z0-9_-]+", prefix)
		}
	}
	return nil
}

// sanitizeServerName lowercases and filters a target name for use as a server id.
func sanitizeServerName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		case r == ' ' || r == '.':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func orDefault(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// ApplyToConfig merges expanded servers into an MCP config.
//
// Expansion is additive: existing entries are preserved, and a generated entry
// that would overwrite a hand-written server is reported as an error so a target
// can never silently shadow a curated MCP server.
func (e *Expanded) ApplyToConfig(cfg *mcpconfig.Config) error {
	if e == nil || cfg == nil {
		return nil
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcpconfig.MCPConfig{}
	}
	names := make([]string, 0, len(e.Servers))
	for name := range e.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, exists := cfg.MCPServers[name]; exists {
			return fmt.Errorf("remote target %q would overwrite an existing MCP server of the same name", name)
		}
		cfg.MCPServers[name] = e.Servers[name]
	}
	return nil
}

// DefaultSearchPaths lists the remoteTargets files consulted in order.
//
// It mirrors the MCP layering: project first (most specific), then the user home.
func DefaultSearchPaths(workspace string) []string {
	paths := make([]string, 0, 2)
	if root := strings.TrimSpace(workspace); root != "" {
		paths = append(paths, filepath.Join(root, ".aicli", "remote-targets.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".aicli", "remote-targets.yaml"))
	}
	return paths
}

// FindConfigFile returns the first existing search path.
func FindConfigFile(workspace string) string {
	for _, path := range DefaultSearchPaths(workspace) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
