package remotetools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/mcpmount"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// DefaultGatewayTokenEnv is the environment variable holding the gateway device
// token. Only the name is stored in config; the value never enters a file or a
// command line.
const DefaultGatewayTokenEnv = "AICLI_GATEWAY_TOKEN"

const mountIdentityFileName = "mount-id"

var instancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// GatewayConfig publishes this server's MCP endpoint(s) through aicli-gateway
// (docs/gateway/16-mcp-mount-tunnel.md). An empty URL disables the integration,
// and the local HTTP transport keeps working exactly as before.
type GatewayConfig struct {
	// URL is the gateway endpoint (wss:// or https://).
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// TokenEnv names the environment variable holding the device token.
	TokenEnv string `yaml:"tokenEnv,omitempty" json:"tokenEnv,omitempty"`
	// TokenFile reads the device token from a 0600 file (preferred over
	// TokenEnv; read once at startup).
	TokenFile string `yaml:"tokenFile,omitempty" json:"tokenFile,omitempty"`
	// UseAICLIConfig reuses the gateway.* section aicli / its TUI already
	// persists (layered config.yaml). Fields set here, and AICLI_GATEWAY_*
	// environment overrides, still win (docs/gateway/16 §8.5).
	UseAICLIConfig *bool `yaml:"useAICLIConfig,omitempty" json:"useAICLIConfig,omitempty"`
	// AICLIConfigFile pins the file to read. Empty walks the same layered
	// search paths aicli uses (project .aicli/config.yaml, then ~/.aicli/...).
	AICLIConfigFile string `yaml:"aicliConfigFile,omitempty" json:"aicliConfigFile,omitempty"`
	// ProxyURL tunnels the agent→gateway WebSocket through an HTTP CONNECT or
	// SOCKS5 proxy. Empty = direct; may also come from aicli's config.
	ProxyURL string `yaml:"proxyUrl,omitempty" json:"proxyUrl,omitempty"`
	// AutoConnect mirrors aicli's preference. Nil means true; an explicit false
	// keeps the node's local HTTP service running without dialing the gateway.
	AutoConnect *bool `yaml:"autoConnect,omitempty" json:"autoConnect,omitempty"`
	// Name is the node name shown in the gateway console.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// PathStyle is "m" (default, /m/{slug}/...) or "root" (/{slug}/...).
	PathStyle string `yaml:"pathStyle,omitempty" json:"pathStyle,omitempty"`
	// MountSelf publishes this server's own HTTP endpoint (default true when a
	// gateway URL is configured).
	MountSelf *bool `yaml:"mountSelf,omitempty" json:"mountSelf,omitempty"`
	// SelfName / SelfSlug override the default identity ("self") used for the
	// self mount. The slug is derived deterministically when left empty.
	SelfName string `yaml:"selfName,omitempty" json:"selfName,omitempty"`
	SelfSlug string `yaml:"selfSlug,omitempty" json:"selfSlug,omitempty"`
	// Instance namespaces this process's mount identity and default labels.
	// When set and IdentityFile/SelfName/Name are empty, they are derived from
	// it: instances/<id>/mount-id, selfName=<id>, node name=<id>. Multiple
	// concurrent processes must use different instance IDs.
	Instance string `yaml:"instance,omitempty" json:"instance,omitempty"`
	// AutoInstance asks normalizeGateway to derive an instance from the
	// workspace when no explicit instance/identity/name was supplied. It is set
	// by the serve command, never decoded from YAML.
	AutoInstance bool `yaml:"-" json:"-"`
	// InstanceAuto records that Instance was derived from the workspace hash.
	InstanceAuto bool `yaml:"-" json:"-"`
	// LegacyIdentity records that this workspace claimed the pre-auto shared
	// mount-id (preserves the existing URL during migration).
	LegacyIdentity bool `yaml:"-" json:"-"`
	// IdentityFile overrides the persisted mount identity path (tests).
	IdentityFile string `yaml:"identityFile,omitempty" json:"identityFile,omitempty"`
	// Mounts declares additional local streamable-HTTP MCP endpoints to publish
	// through the same tunnel (one remote-tools process can expose N of them).
	Mounts []GatewayMountConfig `yaml:"mounts,omitempty" json:"mounts,omitempty"`
}

// GatewayMountConfig is one additional MCP endpoint published through the
// gateway. Upstream is a local URL (loopback is expected).
type GatewayMountConfig struct {
	Name         string   `yaml:"name" json:"name"`
	Slug         string   `yaml:"slug,omitempty" json:"slug,omitempty"`
	Upstream     string   `yaml:"upstream" json:"upstream"`
	TokenEnv     string   `yaml:"tokenEnv,omitempty" json:"tokenEnv,omitempty"`
	TokenFile    string   `yaml:"tokenFile,omitempty" json:"tokenFile,omitempty"`
	AuthType     string   `yaml:"authType,omitempty" json:"authType,omitempty"`
	Header       string   `yaml:"header,omitempty" json:"header,omitempty"`
	Timeout      Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxBodyBytes int64    `yaml:"maxBodyBytes,omitempty" json:"maxBodyBytes,omitempty"`
	MaxConcurren int      `yaml:"maxConcurrency,omitempty" json:"maxConcurrency,omitempty"`
	MaxStreams   int      `yaml:"maxStreams,omitempty" json:"maxStreams,omitempty"`
}

// mountSelfEnabled reports whether the server's own endpoint is published.
func (g GatewayConfig) mountSelfEnabled() bool {
	return g.MountSelf == nil || *g.MountSelf
}

// useAICLIConfig reports whether the aicli-persisted gateway section is a
// fallback source for connection settings.
func (g GatewayConfig) useAICLIConfig() bool {
	return g.UseAICLIConfig != nil && *g.UseAICLIConfig
}

// normalizeGateway validates the gateway block. A disabled block is left alone;
// enabling one requires a token env name and a usable URL.
func (c *Config) normalizeGateway() error {
	gateway := &c.Gateway
	gateway.URL = strings.TrimSpace(gateway.URL)
	gateway.Name = strings.TrimSpace(gateway.Name)
	gateway.SelfName = strings.TrimSpace(gateway.SelfName)
	gateway.SelfSlug = strings.TrimSpace(gateway.SelfSlug)
	gateway.Instance = strings.ToLower(strings.TrimSpace(gateway.Instance))
	gateway.IdentityFile = strings.TrimSpace(gateway.IdentityFile)
	gateway.TokenFile = strings.TrimSpace(gateway.TokenFile)
	gateway.AICLIConfigFile = strings.TrimSpace(gateway.AICLIConfigFile)
	gateway.ProxyURL = strings.TrimSpace(gateway.ProxyURL)
	if gateway.TokenEnv == "" {
		gateway.TokenEnv = DefaultGatewayTokenEnv
	}
	gateway.PathStyle = mcpmount.NormalizePathStyle(gateway.PathStyle)
	if gateway.AutoInstance {
		switch {
		case gateway.URL == "" && !gateway.useAICLIConfig():
			gateway.AutoInstance = false
		case gateway.Instance == "" && gateway.IdentityFile == "" && gateway.SelfName == "":
			if err := gateway.resolveAutoInstance(c.Workspace); err != nil {
				return fmt.Errorf("gateway.instance: %w", err)
			}
		}
	}
	if gateway.Instance != "" {
		if err := ValidateInstance(gateway.Instance); err != nil {
			return fmt.Errorf("gateway.instance: %w", err)
		}
		if gateway.IdentityFile == "" {
			identityPath, err := InstanceIdentityPath(gateway.Instance)
			if err != nil {
				return fmt.Errorf("gateway.instance: %w", err)
			}
			gateway.IdentityFile = identityPath
		}
		if gateway.SelfName == "" {
			gateway.SelfName = gateway.Instance
		}
		if gateway.Name == "" {
			gateway.Name = gateway.Instance
		}
	}
	if gateway.URL == "" && !gateway.useAICLIConfig() {
		if len(gateway.Mounts) > 0 {
			return fmt.Errorf("gateway.url is required when gateway.mounts is set")
		}
		return nil
	}
	if gateway.SelfSlug != "" {
		if err := mcpmount.ValidateSlug(gateway.SelfSlug); err != nil {
			return fmt.Errorf("gateway.selfSlug: %w", err)
		}
	}
	seen := map[string]bool{}
	for i := range gateway.Mounts {
		mount := &gateway.Mounts[i]
		mount.Name = strings.TrimSpace(mount.Name)
		mount.Slug = strings.TrimSpace(mount.Slug)
		mount.Upstream = strings.TrimSpace(mount.Upstream)
		mount.TokenEnv = strings.TrimSpace(mount.TokenEnv)
		mount.TokenFile = strings.TrimSpace(mount.TokenFile)
		mount.AuthType = strings.ToLower(strings.TrimSpace(mount.AuthType))
		mount.Header = strings.TrimSpace(mount.Header)
		if mount.Name == "" {
			return fmt.Errorf("gateway.mounts[%d].name is required", i)
		}
		if seen[mount.Name] {
			return fmt.Errorf("gateway.mounts: duplicate name %q", mount.Name)
		}
		seen[mount.Name] = true
		if mount.Upstream == "" {
			return fmt.Errorf("gateway.mounts[%d].upstream is required", i)
		}
		parsed, err := url.Parse(mount.Upstream)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("gateway.mounts[%d].upstream %q must be an absolute http(s) URL", i, mount.Upstream)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("gateway.mounts[%d].upstream %q must use http or https", i, mount.Upstream)
		}
		if mount.Slug != "" {
			if err := mcpmount.ValidateSlug(mount.Slug); err != nil {
				return fmt.Errorf("gateway.mounts[%d].slug: %w", i, err)
			}
		}
		switch mount.AuthType {
		case "", "bearer", "header", "none":
		default:
			return fmt.Errorf("gateway.mounts[%d].authType %q must be bearer, header or none", i, mount.AuthType)
		}
		if mount.MaxBodyBytes < 0 || mount.MaxConcurren < 0 || mount.MaxStreams < 0 {
			return fmt.Errorf("gateway.mounts[%d]: maxBodyBytes/maxConcurrency/maxStreams must not be negative", i)
		}
	}
	return nil
}

// DefaultMountIdentityPath returns the persisted mount identity location.
func DefaultMountIdentityPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home directory for mount identity: %w", err)
	}
	return filepath.Join(home, ".aicli", "data", "gateway-agent", mountIdentityFileName), nil
}

// ValidateInstance checks the short per-instance label used to namespace a
// process's mount identity. The shape is deliberately filesystem- and log-safe.
func ValidateInstance(instance string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return fmt.Errorf("instance is empty")
	}
	if !instancePattern.MatchString(instance) {
		return fmt.Errorf("instance %q must be 1-32 chars of lowercase letters, digits or '-'", instance)
	}
	return nil
}

// InstanceIdentityPath returns the per-instance mount identity location. Each
// concurrent serve process must use a different instance ID (or an explicit
// IdentityFile); sharing an identity makes the two processes overwrite each
// other's mount records on every REGISTER.
func InstanceIdentityPath(instance string) (string, error) {
	if err := ValidateInstance(instance); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home directory for instance identity: %w", err)
	}
	return filepath.Join(home, ".aicli", "data", "gateway-agent", "instances", instance, mountIdentityFileName), nil
}

// instanceBinding identifies one auto instance. Both the machine and the
// workspace are part of the key: two computers that happen to use the same
// /home path must not share an instance or a public slug.
type instanceBinding struct {
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
}

// DefaultInstanceID returns a stable auto instance id for a workspace on this
// machine. The hostname makes the label readable; the hash distinguishes
// machines and workspaces even when the hostname is duplicated.
func DefaultInstanceID(workspace string) (string, error) {
	binding, err := currentInstanceBinding(workspace)
	if err != nil {
		return "", err
	}
	return DefaultInstanceIDForBinding(binding), nil
}

func DefaultInstanceIDForBinding(binding instanceBinding) string {
	sum := sha256.Sum256([]byte("aicli-instance-v2\x00" + binding.Machine + "\x00" + binding.Workspace))
	suffix := hex.EncodeToString(sum[:4])
	if label := machineInstanceLabel(); label != "" {
		return "auto-" + label + "-" + suffix
	}
	return "auto-" + suffix
}

func currentInstanceBinding(workspace string) (instanceBinding, error) {
	key, err := workspaceInstanceKey(workspace)
	if err != nil {
		return instanceBinding{}, err
	}
	return instanceBinding{
		Machine:   machineIdentityKey(),
		Workspace: key,
	}, nil
}

func workspaceInstanceKey(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory for instance: %w", err)
		}
		workspace = cwd
	}
	key := filepath.Clean(workspace)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}

// parseInstanceBinding accepts both the machine-aware JSON form and the old
// single-workspace marker written before machine keys existed.
func parseInstanceBinding(data []byte) (instanceBinding, bool) {
	var binding instanceBinding
	if err := json.Unmarshal(data, &binding); err == nil &&
		strings.TrimSpace(binding.Machine) != "" &&
		strings.TrimSpace(binding.Workspace) != "" {
		return instanceBinding{
			Machine:   strings.TrimSpace(binding.Machine),
			Workspace: strings.TrimSpace(binding.Workspace),
		}, true
	}
	return instanceBinding{Workspace: strings.TrimSpace(string(data))}, false
}

func writeInstanceBinding(path string, binding instanceBinding) error {
	data, err := json.Marshal(binding)
	if err != nil {
		return fmt.Errorf("encode instance binding: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create instance binding dir: %w", err)
		}
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write instance binding: %w", err)
	}
	return nil
}

// claimLegacyWorkspace atomically gives the pre-auto shared identity to the
// first machine/workspace that starts after the upgrade. Later machines or
// workspaces derive their own identity, so a new service cannot steal the
// existing URL.
func claimLegacyWorkspace(marker string, binding instanceBinding) (bool, error) {
	data, err := json.Marshal(binding)
	if err != nil {
		return false, fmt.Errorf("encode legacy workspace marker: %w", err)
	}
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(marker)
			if writeErr != nil {
				return false, fmt.Errorf("write legacy workspace marker: %w", writeErr)
			}
			return false, fmt.Errorf("close legacy workspace marker: %w", closeErr)
		}
		return true, nil
	}
	if !os.IsExist(err) {
		return false, fmt.Errorf("claim legacy workspace marker: %w", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil {
		return false, fmt.Errorf("read legacy workspace marker: %w", readErr)
	}
	existing, structured := parseInstanceBinding(raw)
	if structured {
		return existing == binding, nil
	}
	// Old plain-text marker: adopt it for this machine if the workspace
	// matches, preserving the existing URL across the upgrade.
	if existing.Workspace != binding.Workspace {
		return false, nil
	}
	if err := writeInstanceBinding(marker, binding); err != nil {
		return false, err
	}
	return true, nil
}

// resolveAutoInstance chooses the legacy identity for the first machine +
// workspace that claims it, and a machine/workspace hash for everything else.
func (g *GatewayConfig) resolveAutoInstance(workspace string) error {
	binding, err := currentInstanceBinding(workspace)
	if err != nil {
		return err
	}
	legacyPath, err := DefaultMountIdentityPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(legacyPath); statErr == nil {
		marker := filepath.Join(filepath.Dir(legacyPath), "legacy-workspace")
		claimed, err := claimLegacyWorkspace(marker, binding)
		if err != nil {
			return err
		}
		if claimed {
			g.LegacyIdentity = true
			g.AutoInstance = false
			return nil
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect legacy mount identity: %w", statErr)
	}
	g.Instance = DefaultInstanceIDForBinding(binding)
	g.InstanceAuto = true
	g.AutoInstance = false
	return nil
}

// ensureInstanceBinding refuses to reuse an auto instance directory that was
// first bound to a different machine or workspace. This is the collision path
// that must tell the operator to pass --instance.
func ensureInstanceBinding(workspace string, cfg GatewayConfig) error {
	if !cfg.InstanceAuto || strings.TrimSpace(cfg.Instance) == "" || strings.TrimSpace(cfg.IdentityFile) == "" {
		return nil
	}
	binding, err := currentInstanceBinding(workspace)
	if err != nil {
		return err
	}
	marker := filepath.Join(filepath.Dir(cfg.IdentityFile), "workspace")
	data, readErr := os.ReadFile(marker)
	if readErr == nil {
		existing, structured := parseInstanceBinding(data)
		if structured {
			if existing != binding {
				return fmt.Errorf("auto instance %q is already bound to another machine/workspace; pass --instance <id> to give this service a distinct instance", cfg.Instance)
			}
			return nil
		}
		if existing.Workspace != binding.Workspace {
			return fmt.Errorf("auto instance %q is already bound to another machine/workspace; pass --instance <id> to give this service a distinct instance", cfg.Instance)
		}
		// Pre-machine-key metadata: upgrade it in place.
		return writeInstanceBinding(marker, binding)
	}
	if !os.IsNotExist(readErr) {
		return fmt.Errorf("read auto instance binding: %w", readErr)
	}
	return writeInstanceBinding(marker, binding)
}

// LoadOrCreateMountIdentity reads the node's persisted mount identity, creating
// it on first use. Unlike the process node id (regenerated on every start), this
// value must survive restarts: it is what makes a mounted URL stable.
func LoadOrCreateMountIdentity(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		resolved, err := DefaultMountIdentityPath()
		if err != nil {
			return "", err
		}
		path = resolved
	}
	data, err := os.ReadFile(path)
	if err == nil {
		identity := strings.TrimSpace(string(data))
		if identity == "" {
			return "", fmt.Errorf("mount identity file %s is empty", path)
		}
		return identity, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read mount identity %s: %w", path, err)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate mount identity: %w", err)
	}
	identity := hex.EncodeToString(raw)
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create mount identity dir: %w", err)
		}
	}
	if err := os.WriteFile(path, []byte(identity+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write mount identity %s: %w", path, err)
	}
	return identity, nil
}

// MountRegistration is the resolved mount set for this process: the wire
// declarations, the local forwarding table and the route allowlist.
type MountRegistration struct {
	Identity string
	Specs    []wire.MountSpec
	Forwards []gatewayagent.MountForward
	Routes   []string
}

// MountMetadata is the display-only snapshot a node publishes with each mount.
// It answers "what is this endpoint?" without making the gateway dial the node:
// host identity, effective profile/directories and the model-visible tool list.
type MountMetadata struct {
	Hostname  string
	OS        string
	Arch      string
	Version   string
	Instance  string
	Backend   string
	ReadOnly  bool
	ToolCount int
	Tools     []string
	Roots     []string
}

// MountMetadata returns this server's registration snapshot. Tools/roots are
// reported only for the server's own endpoint; additional forwarded mounts have
// an upstream surface this process cannot describe without probing it.
func (s *Server) MountMetadata() MountMetadata {
	if s == nil {
		return MountMetadata{}
	}
	hostname, _ := os.Hostname()
	backend := "local"
	if s.cfg != nil && strings.TrimSpace(string(s.cfg.Backend)) != "" {
		backend = strings.TrimSpace(string(s.cfg.Backend))
	}
	instance := ""
	if s.cfg != nil {
		instance = firstNonEmptyString(s.gatewayCfg.Instance, s.cfg.Gateway.Instance)
	}
	tools := s.ToolNames()
	roots := []string(nil)
	if s.cfg != nil {
		roots = s.cfg.EffectiveAllowedRoots()
	}
	return MountMetadata{
		Hostname:  strings.TrimSpace(hostname),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Version:   s.version,
		Instance:  instance,
		Backend:   backend,
		ReadOnly:  s.cfg != nil && s.cfg.ReadOnly,
		ToolCount: len(tools),
		Tools:     tools,
		Roots:     roots,
	}
}

// BuildMountRegistration resolves the configured mounts against the real bound
// listener address. localAddr is the listener address (host:port); :0 is
// supported because the caller only builds this once the socket exists.
func BuildMountRegistration(cfg *Config, identity, localAddr string) (*MountRegistration, error) {
	return BuildMountRegistrationWithMetadata(cfg, identity, localAddr, MountMetadata{})
}

// BuildMountRegistrationWithMetadata is BuildMountRegistration plus the
// display-only metadata reported on the self mount.
func BuildMountRegistrationWithMetadata(cfg *Config, identity, localAddr string, metadata MountMetadata) (*MountRegistration, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil, fmt.Errorf("mount identity is empty")
	}
	registration := &MountRegistration{Identity: identity}
	seenSlug := map[string]bool{}
	seenRoute := map[string]bool{}

	add := func(name, slug, upstream, tokenEnv, tokenFile, authType, header string, timeout time.Duration, maxBody int64, maxConcurrency, maxStreams int, self bool) error {
		// Mount defaults are larger than the default-forwarding limits: MCP tool
		// calls legitimately carry big payloads and long-running shells.
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		if maxBody <= 0 {
			maxBody = 8 << 20
		}
		upstream = strings.TrimSpace(upstream)
		parsed, err := url.Parse(upstream)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("mount %q upstream %q must be an absolute http(s) URL", name, upstream)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("mount %q upstream %q must use http or https", name, upstream)
		}
		localPath := parsed.EscapedPath()
		if localPath == "" {
			localPath = "/"
		}
		localPath, err = mcpmount.NormalizeLocalPath(localPath)
		if err != nil {
			return fmt.Errorf("mount %q: %w", name, err)
		}

		name = strings.TrimSpace(name)
		if name == "" {
			name = "mcp"
		}
		slug = strings.TrimSpace(slug)
		if slug == "" {
			slug = mcpmount.DeriveSlug(identity, name)
		}
		if err := mcpmount.ValidateSlug(slug); err != nil {
			return fmt.Errorf("mount %q: %w", name, err)
		}
		if seenSlug[slug] {
			return fmt.Errorf("mount %q: duplicate slug %q", name, slug)
		}
		seenSlug[slug] = true

		tokenEnv = firstNonEmptyString(tokenEnv, cfg.HTTP.TokenEnv)
		tokenFile = firstNonEmptyString(tokenFile, cfg.HTTP.TokenFile)
		authType = strings.ToLower(strings.TrimSpace(authType))
		if authType == "" {
			authType = "bearer"
		}
		// Resolve the local MCP credential once: the agent then injects a value,
		// not a path, so it never re-reads a rotated file mid-stream.
		tokenValue := ""
		if authType != "none" {
			resolved, err := ResolveSecret("mount "+name+" token", tokenEnv, tokenFile)
			if err != nil {
				return err
			}
			tokenValue = resolved
		}
		labels := &wire.MountLabels{
			Profile:    string(cfg.Profile),
			ToolPrefix: cfg.ToolPrefix,
			TargetName: cfg.TargetName,
			Workspace:  cfg.Workspace,
			Hostname:   metadata.Hostname,
			OS:         metadata.OS,
			Arch:       metadata.Arch,
			Version:    metadata.Version,
			Instance:   metadata.Instance,
			Backend:    metadata.Backend,
			ReadOnly:   metadata.ReadOnly,
		}
		if self {
			labels.ToolCount = metadata.ToolCount
			labels.Tools = append([]string(nil), metadata.Tools...)
			labels.Roots = append([]string(nil), metadata.Roots...)
		}
		registration.Specs = append(registration.Specs, wire.MountSpec{
			ID:       name,
			Name:     name,
			Slug:     slug,
			Kind:     "mcp",
			Upstream: upstream,
			Auth:     &wire.MountAuth{Type: authType, TokenEnv: tokenEnv, Header: strings.TrimSpace(header)},
			Limits: &wire.MountLimits{
				MaxBodyBytes:   maxBody,
				TimeoutSec:     int(timeout / time.Second),
				MaxConcurrency: maxConcurrency,
				MaxStreams:     maxStreams,
			},
			Labels: labels,
		})
		registration.Forwards = append(registration.Forwards, gatewayagent.MountForward{
			Slug:         slug,
			BaseURL:      parsed.Scheme + "://" + parsed.Host,
			AuthType:     authType,
			TokenEnv:     tokenEnv,
			TokenValue:   tokenValue,
			Header:       strings.TrimSpace(header),
			Timeout:      timeout,
			MaxBodyBytes: maxBody,
		})
		if !seenRoute[localPath] {
			seenRoute[localPath] = true
			registration.Routes = append(registration.Routes, localPath)
		}
		return nil
	}

	if cfg.Gateway.mountSelfEnabled() {
		upstream, err := selfUpstream(cfg, localAddr)
		if err != nil {
			return nil, err
		}
		if err := add(firstNonEmptyString(cfg.Gateway.SelfName, "self"), cfg.Gateway.SelfSlug, upstream, "", "", "", "", 0, 0, 0, 0, true); err != nil {
			return nil, err
		}
	}
	for _, mount := range cfg.Gateway.Mounts {
		if err := add(mount.Name, mount.Slug, mount.Upstream, mount.TokenEnv, mount.TokenFile, mount.AuthType, mount.Header, mount.Timeout.Duration, mount.MaxBodyBytes, mount.MaxConcurren, mount.MaxStreams, false); err != nil {
			return nil, err
		}
	}
	sort.Strings(registration.Routes)
	return registration, nil
}

// selfUpstream builds the loopback URL of this server's own MCP endpoint.
func selfUpstream(cfg *Config, localAddr string) (string, error) {
	addr := strings.TrimSpace(localAddr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("resolve local listener %q: %w", addr, err)
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	scheme := "http"
	if strings.TrimSpace(cfg.HTTP.TLSCert) != "" && strings.TrimSpace(cfg.HTTP.TLSKey) != "" {
		scheme = "https"
	}
	endpointPath := cfg.HTTP.Path
	if !strings.HasPrefix(endpointPath, "/") {
		endpointPath = "/" + endpointPath
	}
	return scheme + "://" + net.JoinHostPort(host, port) + endpointPath, nil
}

// EnableGateway prepares the reverse-tunnel registration. The connection starts
// only once the HTTP listener is bound, so a :0 listen address is advertised
// correctly. A gateway failure never takes the local HTTP transport down.
func (s *Server) EnableGateway(ctx context.Context, cfg GatewayConfig) error {
	if s == nil {
		return fmt.Errorf("server is not initialized")
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.AICLIConfigFile = strings.TrimSpace(cfg.AICLIConfigFile)

	// Reuse what the TUI already persisted (lowest precedence). Explicit
	// fields here and AICLI_GATEWAY_* overrides win.
	aicliSection := agentconfig.GatewayConfig{}
	haveAICLI := false
	if cfg.useAICLIConfig() {
		section, ok, err := agentconfig.LoadGatewaySection(cfg.AICLIConfigFile)
		if err != nil {
			return err
		}
		if ok {
			aicliSection, haveAICLI = section, true
			if cfg.URL == "" {
				cfg.URL = section.URL
			}
			if cfg.Name == "" {
				cfg.Name = section.Name
			}
			if cfg.ProxyURL == "" {
				cfg.ProxyURL = section.ProxyURL
			}
			if cfg.AutoConnect == nil && section.AutoConnect != nil {
				cfg.AutoConnect = section.AutoConnect
			}
		}
	}
	if value := strings.TrimSpace(os.Getenv(gatewayagent.EnvURL)); value != "" {
		cfg.URL = value
	}
	if value := strings.TrimSpace(os.Getenv(gatewayagent.EnvName)); value != "" {
		cfg.Name = value
	}
	if value := strings.TrimSpace(os.Getenv(gatewayagent.EnvProxyURL)); value != "" {
		cfg.ProxyURL = value
	}
	if value := strings.TrimSpace(os.Getenv(gatewayagent.EnvAutoConnect)); value != "" {
		enabled := !strings.EqualFold(value, "false") && value != "0"
		cfg.AutoConnect = &enabled
	}

	if cfg.URL == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.Transport), "stdio") {
		return fmt.Errorf("gateway mounts require the http transport")
	}
	cfg.TokenEnv = firstNonEmptyString(cfg.TokenEnv, DefaultGatewayTokenEnv)
	token, err := ResolveSecret("gateway.tokenFile", cfg.TokenEnv, cfg.TokenFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" && haveAICLI {
		token = strings.TrimSpace(aicliSection.Token)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("gateway token is empty (set gateway.tokenFile, %s, or gateway.useAICLIConfig)", cfg.TokenEnv)
	}
	var instanceLock *os.File
	if strings.TrimSpace(cfg.Instance) != "" ||
		(s.cfg != nil && s.cfg.Gateway.LegacyIdentity) {
		identityPath, err := effectiveIdentityPath(cfg.IdentityFile)
		if err != nil {
			return err
		}
		instanceLock, err = acquireIdentityLock(identityPath)
		if err != nil {
			return err
		}
	}
	locked := instanceLock != nil
	defer func() {
		if locked {
			_ = instanceLock.Close()
		}
	}()
	if err := ensureInstanceBinding(s.cfg.Workspace, cfg); err != nil {
		return err
	}
	if s.cfg.Gateway.LegacyIdentity {
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: legacy shared mount identity retained for this workspace; pass --instance <id> to isolate another workspace")
	}
	identity, err := LoadOrCreateMountIdentity(cfg.IdentityFile)
	if err != nil {
		return err
	}
	if cfg.PathStyle == "" {
		cfg.PathStyle = mcpmount.PathStyleNamespaced
	}
	s.gatewayCfg = cfg
	s.gatewayIdentity = identity
	s.gatewayToken = token
	if instanceLock != nil {
		s.instanceLock = instanceLock
		locked = false // ownership moved to the server
		go func() {
			<-ctx.Done()
			_ = instanceLock.Close()
		}()
	}
	s.gatewayStart = func(localAddr string) error {
		return s.startGatewayAgent(ctx, localAddr)
	}
	return nil
}

// startGatewayAgent dials the gateway, registers the mount set and keeps the
// tunnel alive until ctx ends.
func (s *Server) startGatewayAgent(ctx context.Context, localAddr string) error {
	metadata := s.MountMetadata()
	registration, err := BuildMountRegistrationWithMetadata(s.cfg, s.gatewayIdentity, localAddr, metadata)
	if err != nil {
		return err
	}
	if len(registration.Specs) == 0 {
		return fmt.Errorf("no mounts configured")
	}
	gatewayCfg := gatewayagent.Config{
		Enabled:     true,
		URL:         s.gatewayCfg.URL,
		Token:       s.gatewayToken,
		Name:        firstNonEmptyString(s.gatewayCfg.Name, s.cfg.TargetName, "remote-tools"),
		ProxyURL:    s.gatewayCfg.ProxyURL,
		AutoConnect: s.gatewayCfg.AutoConnect == nil || *s.gatewayCfg.AutoConnect,
	}
	nodeID := gatewayagent.ProcessNodeID()
	workspaceName := ""
	if s.cfg != nil {
		workspaceName = filepath.Base(filepath.Clean(s.cfg.Workspace))
	}
	register := func() wire.Register {
		return wire.Register{
			NodeID:       nodeID,
			NodeStable:   registration.Identity,
			Kind:         "remote-tools",
			Name:         gatewayCfg.Name,
			DeviceName:   metadata.Hostname,
			Version:      metadata.Version,
			OS:           metadata.OS,
			Arch:         metadata.Arch,
			Capabilities: []string{"mcp-mount"},
			Routes:       registration.Routes,
			Mounts:       registration.Specs,
			Workspace: &wire.WorkspaceInfo{
				Path: s.cfg.Workspace,
				Name: workspaceName,
			},
		}
	}
	// Lifecycle diagnostics go to stderr; tests may override them through
	// gatewayOptions.
	options := append([]gatewayagent.Option{gatewayagent.WithLogger(s.gatewayLogf())}, s.gatewayOptions...)
	controller := gatewayagent.NewController(gatewayCfg, register, options...)
	controller.SetForward(gatewayagent.ForwardConfig{
		Mounts: registration.Forwards,
		Getenv: os.Getenv,
	})
	s.gatewayClose = func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = controller.Disconnect(stopCtx)
	}
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = controller.Disconnect(stopCtx)
	}()
	if !gatewayCfg.AutoConnect {
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: gateway auto-connect disabled; local HTTP transport unaffected")
		return nil
	}
	go func() {
		// Retry the first dial with the reconnect backoff ladder: the gateway
		// (or the local network) may not be reachable yet at startup, and a
		// one-shot Connect would leave this node offline until a restart.
		if err := controller.ConnectWithRetry(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: gateway connect failed: %v\n", err)
			return
		}
		for _, mount := range controller.Status().Mounts {
			switch {
			case mount.Rejected:
				hint := ""
				if mount.Reason == "mount_slug_conflict" {
					hint = " (this instance/slug is already in use; start this service with --instance <id>)"
				}
				fmt.Fprintf(os.Stderr, "aicli-remote-tools: mount %s REJECTED: %s%s\n", mount.Slug, mount.Reason, hint)
			case mount.PublicURL != "":
				fmt.Fprintf(os.Stderr, "aicli-remote-tools: mount %s -> %s\n", mount.Slug, mount.PublicURL)
			default:
				fmt.Fprintf(os.Stderr, "aicli-remote-tools: mount %s registered\n", mount.Slug)
			}
		}
	}()
	return nil
}

// gatewayLogf writes gateway lifecycle diagnostics to stderr with the same
// prefix as the rest of the serve output. The agent package reports connection
// loss, retry attempts and reconnects through it, so a mount that shows offline
// in the console also leaves a local trace instead of being silent.
func (s *Server) gatewayLogf() func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: "+format+"\n", args...)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
