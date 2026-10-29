// Command aicli-remote-tools is the target-side half of the aicli remote tools
// design (docs/plan/aicli-remote-tools-ssh-http-design-20261021.md).
//
// It exposes a read-only tool surface (view/ls/glob/grep) as an MCP server over
// one of two transports:
//
//	aicli-remote-tools serve --stdio     # launched per connection by sshd
//	aicli-remote-tools serve --http      # long-lived daemon, loopback by default
//
// Both transports share the same workspace jail, tool schema, error codes and
// audit format; only the process lifecycle differs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// version is injected at build time (-X main.version=...).
var version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "init":
		return runInit(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "tools":
		return runTools(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(os.Stdout, "aicli-remote-tools %s\n", version)
		return 0
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: unknown command %q\n\n", args[0])
		printUsage(os.Stderr)
		return 2
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `aicli-remote-tools %s - remote tool server (MCP over stdio or Streamable HTTP)

Usage:
  aicli-remote-tools init   [options]           # write a ready-to-run config + token
  aicli-remote-tools serve  --stdio [options]   # SSH transport (one process per connection)
  aicli-remote-tools serve  --http  [options]   # daemon transport (loopback by default; implied when omitted)
  aicli-remote-tools doctor [options]           # self-check and effective settings
  aicli-remote-tools tools  [options]           # list the exposed tool names

Required:
  --workspace <path>        Root for relative paths; also the default jail root.

Common options:
  --config <file>           YAML config (flags override file values)
  --allowed-roots <a,b>     Extra absolute roots (workspace is always included)
  --profile <name>          readonly (default) | standard | ops | custom
  --allowed-command-patterns <p1,p2>  Whole-command wildcard allowlist (* and ?)
  --denied-command-patterns  <p1,p2>  Whole-command wildcard denylist (always wins)
  --tool-prefix <prefix>    Prefix every tool name, e.g. remote_dev
  --tools <a,b>             Explicit tool allowlist (narrows any profile)
  --target-name <name>      Label used in descriptions and audit records
  --description-style <s>   compact (default) | explicit
  --max-output-bytes <n>    Model-visible output cap per call (default 262144)
  --max-bytes-per-call <n>  Per-call payload cap (default 2097152)
  --audit <path>            NDJSON audit log (0600)
  --jobs-root <path>        Legacy job_* store root (requires --legacy-jobs)
  --legacy-jobs             Re-enable the legacy remote job_* tools (v1 default: off)
  --artifacts-root <path>   Enable artifact_read for folded captures
  --artifacts-max-bytes <n> Per-artifact size limit (default 67108864)

HTTP options:
  --http-listen <addr>      Listen address (default 127.0.0.1:18765; 127.0.0.1:0 = random)
  --http-path <path>        MCP endpoint path (default /mcp)
  --http-token-env <name>   Env var holding the bearer token (default AICLI_REMOTE_TOKEN)
  --http-token-file <file>  Read the bearer token from a 0600 file (preferred; overrides the env var)
  --http-access-log <file>  NDJSON request log (includes 401s; no Authorization header)
  --tls-cert <file> --tls-key <file>

Gateway mount options (serve --http):
  --gateway-url <url>            aicli-gateway endpoint, e.g. https://aicli.ttai.online
  --gateway-from-aicli           Reuse the gateway.* section aicli/the TUI already persisted
  --gateway-aicli-config <file>  Explicit aicli config file for the above (empty = layered defaults)
  --gateway-token-file <file>    Device token file (0600), preferred over the env var
  --gateway-token-env <name>     Env var holding the device token (default AICLI_GATEWAY_TOKEN)
  --gateway-proxy-url <url>      Proxy for the agent→gateway tunnel (e.g. http://127.0.0.1:10810)
  --instance <id>                Per-instance identity/label (derives instances/<id>/mount-id)
  --gateway-name <name>          Node name shown in the gateway console
  --gateway-self-name <name>     Mount name for this server's own endpoint (default self)
  --gateway-self-slug <slug>     Pin the public path segment
  --gateway-identity-file <file> Persisted mount identity (default ~/.aicli/data/gateway-agent/mount-id)
  --gateway-no-mount-self        Publish only --mount entries
  --mount name=<id>,upstream=<url>[,token_file=<f>][,token_env=<NAME>][,slug=<slug>]
                                 Repeatable: publish another local MCP endpoint

First-run behaviour:
  a bare 'serve' creates ~/.aicli/remote-tools/config.yaml + mcp.token on first
  run and reuses aicli's persisted gateway.* when present.
  --no-auto-init                 skip creating the default config/token
  --no-gateway                   ignore aicli's gateway config (stay local)

envd backend options (serve --stdio --backend envd):
  --addr <host:port|url>    envd endpoint (required)
  --user <name>             OS user envd executes as (required; envd reads it from HTTP Basic credentials)
  --token-env <name>        Env var holding the X-Access-Token value
  --ca-file <pem>           CA bundle verifying a TLS envd endpoint (switches a bare host:port to https://)
  --call-timeout <dur>      Per-call deadline (default 10m)
  --no-envd-files           Read via spawned processes only, never envd's native /files endpoint

The envd backend runs on THIS machine and reaches the target over Connect-RPC:
--workspace is a path on the target (absolute POSIX), the jail is enforced
against paths canonicalized on the target, and only the stdio transport is
available. envd performs no path or command checks of its own.

Examples:
  # Just run it in the directory you want to expose. First run auto-creates
  # ~/.aicli/remote-tools/{config.yaml,mcp.token} and reuses aicli's gateway.
  cd D:\work
  aicli-remote-tools serve

  # Explicit one-time setup (same result, with reviewable output):
  aicli-remote-tools init --workspace D:\work

  # SSH stdio (local side: ssh-client --stdio ...)
  aicli-remote-tools serve --stdio --workspace /srv/app --tool-prefix remote_dev

  # HTTP daemon bound to loopback, reached through an SSH local forward
  AICLI_REMOTE_TOKEN=$(openssl rand -hex 32) \
    aicli-remote-tools serve --http --workspace /srv/app --tool-prefix remote_dev

  # envd target (no server binary on the target; envd runs there)
  aicli-remote-tools serve --stdio --backend envd \
    --addr 10.0.0.9:49983 --user dev --workspace /srv/app --tool-prefix remote_dev
`, version)
}

// stringListFlag collects a repeatable flag into a list.
type stringListFlag []string

func (s *stringListFlag) String() string { return strings.Join(*s, ",") }

func (s *stringListFlag) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	*s = append(*s, trimmed)
	return nil
}

// serveFlags is the flag set shared by serve/doctor/tools.
type serveFlags struct {
	configPath    string
	configB64     string
	configSource  string
	workspace     string
	allowedRoots  string
	profile       string
	toolPrefix    string
	tools         string
	targetName    string
	descStyle     string
	maxOutput     int
	maxBytes      int
	auditPath     string
	auditPreview  int
	jobsRoot      string
	legacyJobs    bool
	artifactRoot  string
	artifactMax   int
	allowPatterns string
	denyPatterns  string

	backend      string
	envdAddr     string
	envdUser     string
	envdTokenEnv string
	envdCAFile   string
	envdTimeout  string
	envdNoFiles  bool

	allowCommands    stringListFlag
	denyCommands     stringListFlag
	allowCommandPats stringListFlag
	denyCommandPats  stringListFlag
	denyMetachars    bool

	stdio     bool
	http      bool
	listen    string
	httpPath  string
	tokenEnv  string
	tokenFile string
	tlsCert   string
	tlsKey    string
	accessLog string

	noAutoInit bool
	noGateway  bool

	gatewayURL       string
	gatewayTokenEnv  string
	gatewayTokenFile string
	gatewayFromAICLI bool
	gatewayAICLICfg  string
	gatewayProxyURL  string
	instance         string
	gatewayName      string
	gatewayPathStyle string
	gatewaySelfName  string
	gatewaySelfSlug  string
	gatewayIdentity  string
	noMountSelf      bool
	gatewayMounts    gatewayMountFlag

	jsonOut bool
}

// gatewayMountFlag collects repeatable --mount specs:
//
//	--mount name=prod,upstream=http://127.0.0.1:18766/mcp,token_env=AICLI_TOKEN
type gatewayMountFlag []remotetools.GatewayMountConfig

func (f *gatewayMountFlag) String() string {
	parts := make([]string, 0, len(*f))
	for _, mount := range *f {
		parts = append(parts, mount.Name+"="+mount.Upstream)
	}
	return strings.Join(parts, ";")
}

func (f *gatewayMountFlag) Set(value string) error {
	entry := remotetools.GatewayMountConfig{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, raw, ok := strings.Cut(part, "=")
		if !ok {
			return fmt.Errorf("--mount entry %q must be key=value", part)
		}
		key, raw = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(raw)
		switch key {
		case "name":
			entry.Name = raw
		case "slug":
			entry.Slug = raw
		case "upstream":
			entry.Upstream = raw
		case "token_env", "token-env":
			entry.TokenEnv = raw
		case "token_file", "token-file":
			entry.TokenFile = raw
		case "auth", "auth_type", "auth-type":
			entry.AuthType = raw
		case "header":
			entry.Header = raw
		case "timeout":
			parsed, err := time.ParseDuration(raw)
			if err != nil || parsed <= 0 {
				return fmt.Errorf("--mount timeout %q must be a positive duration", raw)
			}
			entry.Timeout = remotetools.Duration{Duration: parsed}
		case "max_body", "max-body":
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				return fmt.Errorf("--mount max_body %q must be a positive integer", raw)
			}
			entry.MaxBodyBytes = parsed
		case "max_concurrency", "max-concurrency":
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				return fmt.Errorf("--mount max_concurrency %q must be a positive integer", raw)
			}
			entry.MaxConcurren = parsed
		default:
			return fmt.Errorf("--mount has unknown key %q", key)
		}
	}
	if entry.Upstream == "" {
		return fmt.Errorf("--mount needs upstream=<url>")
	}
	if entry.Name == "" {
		return fmt.Errorf("--mount needs name=<id>")
	}
	*f = append(*f, entry)
	return nil
}

func newFlagSet(name string, flags *serveFlags) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&flags.configPath, "config", "", "YAML config file")
	fs.StringVar(&flags.configB64, "config-b64", "", "base64-encoded JSON config (overrides --config)")
	fs.BoolVar(&flags.noAutoInit, "no-auto-init", false, "do not create the default config/token on first run")
	fs.BoolVar(&flags.noGateway, "no-gateway", false, "ignore aicli's persisted gateway config (stay local)")
	fs.StringVar(&flags.workspace, "workspace", "", "workspace root (required unless set in config)")
	fs.StringVar(&flags.allowedRoots, "allowed-roots", "", "comma-separated extra absolute roots")
	fs.StringVar(&flags.profile, "profile", "", "readonly|standard|ops|custom")
	fs.StringVar(&flags.toolPrefix, "tool-prefix", "", "prefix for every exposed tool name")
	fs.StringVar(&flags.tools, "tools", "", "comma-separated tool allowlist")
	fs.StringVar(&flags.targetName, "target-name", "", "label for descriptions and audit")
	fs.StringVar(&flags.descStyle, "description-style", "", "compact|explicit")
	fs.IntVar(&flags.maxOutput, "max-output-bytes", 0, "model-visible output cap")
	fs.IntVar(&flags.maxBytes, "max-bytes-per-call", 0, "per-call payload cap")
	fs.StringVar(&flags.auditPath, "audit", "", "NDJSON audit path")
	fs.IntVar(&flags.auditPreview, "audit-command-preview-bytes", 0, "record the first N bytes of shell/job_start commands in the audit log (0=off)")
	fs.StringVar(&flags.jobsRoot, "jobs-root", "", "root for background job records and output")
	fs.BoolVar(&flags.legacyJobs, "legacy-jobs", false, "re-enable the legacy remote job_* tools")
	fs.StringVar(&flags.artifactRoot, "artifacts-root", "", "root for full tool captures")
	fs.IntVar(&flags.artifactMax, "artifacts-max-bytes", 0, "per-artifact size limit")
	fs.StringVar(&flags.allowPatterns, "allowed-command-patterns", "", "comma-separated whole-command wildcard allowlist (* and ?)")
	fs.StringVar(&flags.denyPatterns, "denied-command-patterns", "", "comma-separated whole-command wildcard denylist (* and ?)")

	fs.StringVar(&flags.backend, "backend", "", "execution backend: local (default) | envd (reach a remote envd daemon over Connect-RPC)")
	fs.StringVar(&flags.envdAddr, "addr", "", "envd endpoint: host:port or URL (backend envd)")
	fs.StringVar(&flags.envdUser, "user", "", "OS user envd executes as (backend envd; envd reads it from HTTP Basic credentials)")
	fs.StringVar(&flags.envdTokenEnv, "token-env", "", "env var holding the envd access token (backend envd)")
	fs.StringVar(&flags.envdCAFile, "ca-file", "", "PEM CA bundle verifying a TLS envd endpoint (backend envd)")
	fs.StringVar(&flags.envdTimeout, "call-timeout", "", "per-call deadline as a Go duration, e.g. 30m (backend envd; default 10m)")
	// The native file endpoint is preferred, but it reads as the daemon user; an
	// operator must be able to refuse that difference without a rebuild, and a
	// live test must be able to force the other channel to prove both agree.
	fs.BoolVar(&flags.envdNoFiles, "no-envd-files", false, "read files through spawned processes only; never use envd's native /files endpoint (backend envd)")

	// Command policy. These repeat, so a value may contain spaces ("git status")
	// without a comma-joining ambiguity; the envd expansion emits them in the
	// same shape the standalone bridge accepted.
	fs.Var(&flags.allowCommands, "allow-command", "repeatable exact command/argv-prefix allow (empty allowlist is fail-closed)")
	fs.Var(&flags.denyCommands, "deny-command", "repeatable exact command deny, always wins over the allowlist")
	fs.Var(&flags.allowCommandPats, "allow-command-pattern", "repeatable whole-command wildcard allowlist (* and ?)")
	fs.Var(&flags.denyCommandPats, "deny-command-pattern", "repeatable whole-command wildcard denylist")
	fs.BoolVar(&flags.denyMetachars, "deny-shell-metachars", true, "refuse commands containing shell metacharacters so an allowed binary cannot be chained")

	fs.BoolVar(&flags.stdio, "stdio", false, "serve MCP over stdin/stdout")
	fs.BoolVar(&flags.http, "http", false, "serve MCP over Streamable HTTP")
	fs.StringVar(&flags.listen, "http-listen", "", "HTTP listen address")
	fs.StringVar(&flags.httpPath, "http-path", "", "HTTP endpoint path")
	fs.StringVar(&flags.tokenEnv, "http-token-env", "", "env var holding the bearer token")
	fs.StringVar(&flags.tokenFile, "http-token-file", "", "0600 file holding the bearer token (preferred over --http-token-env)")
	fs.StringVar(&flags.tlsCert, "tls-cert", "", "TLS certificate file")
	fs.StringVar(&flags.tlsKey, "tls-key", "", "TLS private key file")
	fs.StringVar(&flags.accessLog, "http-access-log", "", "NDJSON HTTP access log path")

	fs.StringVar(&flags.gatewayURL, "gateway-url", "", "aicli-gateway endpoint (wss:// or https://)")
	fs.StringVar(&flags.gatewayTokenEnv, "gateway-token-env", "", "env var holding the gateway device token (default AICLI_GATEWAY_TOKEN)")
	fs.StringVar(&flags.gatewayTokenFile, "gateway-token-file", "", "0600 file holding the gateway device token (preferred over --gateway-token-env)")
	fs.BoolVar(&flags.gatewayFromAICLI, "gateway-from-aicli", false, "reuse the gateway.* connection settings aicli/the TUI already persisted")
	fs.StringVar(&flags.gatewayAICLICfg, "gateway-aicli-config", "", "explicit aicli config file for --gateway-from-aicli (empty = layered defaults)")
	fs.StringVar(&flags.gatewayProxyURL, "gateway-proxy-url", "", "proxy for the agent→gateway tunnel, e.g. http://127.0.0.1:10810")
	fs.StringVar(&flags.instance, "instance", "", "per-instance identity/label (derives instances/<id>/mount-id and default names)")
	fs.StringVar(&flags.gatewayName, "gateway-name", "", "node name shown in the gateway console")
	fs.StringVar(&flags.gatewayPathStyle, "gateway-path-style", "", "public path style: m (/m/{slug}/...) or root (/{slug}/...)")
	fs.StringVar(&flags.gatewaySelfName, "gateway-self-name", "", "mount name for this server's own endpoint (default self)")
	fs.StringVar(&flags.gatewaySelfSlug, "gateway-self-slug", "", "pinned public slug for this server's own endpoint")
	fs.StringVar(&flags.gatewayIdentity, "gateway-identity-file", "", "path of the persisted mount identity (default ~/.aicli/data/gateway-agent/mount-id)")
	fs.BoolVar(&flags.noMountSelf, "gateway-no-mount-self", false, "do not publish this server's own endpoint (publish only --mount entries)")
	fs.Var(&flags.gatewayMounts, "mount", "publish an extra local MCP endpoint: name=...,upstream=http://127.0.0.1:PORT/mcp[,token_env=...]")

	fs.BoolVar(&flags.jsonOut, "json", false, "machine-readable output (doctor/tools)")
	return fs
}

// loadConfig merges the config file (if any) with the parsed flags.
func loadConfig(flags *serveFlags) (*remotetools.Config, error) {
	var cfg *remotetools.Config
	if strings.TrimSpace(flags.configB64) != "" {
		loaded, err := remotetools.LoadConfigBase64(flags.configB64)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	} else if strings.TrimSpace(flags.configPath) != "" {
		loaded, err := remotetools.LoadConfigFile(flags.configPath)
		if err != nil {
			return nil, err
		}
		cfg = loaded
		flags.configSource = strings.TrimSpace(flags.configPath)
	} else if found := remotetools.FindConfigFile(); found != "" {
		loaded, err := remotetools.LoadConfigFile(found)
		if err != nil {
			return nil, err
		}
		cfg = loaded
		flags.configSource = found
	} else {
		cfg = remotetools.DefaultConfig()
	}

	if flags.workspace != "" {
		cfg.Workspace = flags.workspace
	}
	if flags.allowedRoots != "" {
		cfg.AllowedRoots = splitList(flags.allowedRoots)
	}
	if flags.profile != "" {
		cfg.Profile = remotetools.Profile(strings.ToLower(strings.TrimSpace(flags.profile)))
	}
	if flags.toolPrefix != "" {
		cfg.ToolPrefix = flags.toolPrefix
	}
	if flags.tools != "" {
		cfg.Tools = splitList(flags.tools)
	}
	if flags.targetName != "" {
		cfg.TargetName = flags.targetName
	}
	if flags.descStyle != "" {
		cfg.DescriptionStyle = flags.descStyle
	}
	if flags.maxOutput > 0 {
		cfg.Limits.MaxOutputBytes = flags.maxOutput
	}
	if flags.maxBytes > 0 {
		cfg.Limits.MaxBytesPerCall = flags.maxBytes
	}
	if flags.auditPath != "" {
		cfg.Audit.Path = flags.auditPath
	}
	if flags.auditPreview > 0 {
		cfg.Audit.CommandPreviewBytes = flags.auditPreview
	}
	if flags.jobsRoot != "" {
		cfg.Jobs.Root = flags.jobsRoot
	}
	if flags.legacyJobs {
		cfg.Jobs.LegacyRemoteJobs = true
	}
	if flags.artifactRoot != "" {
		cfg.Artifacts.Root = flags.artifactRoot
	}
	if flags.artifactMax > 0 {
		cfg.Artifacts.MaxBytes = int64(flags.artifactMax)
	}
	if flags.allowPatterns != "" {
		cfg.Shell.AllowedCommandPatterns = splitList(flags.allowPatterns)
	}
	if flags.denyPatterns != "" {
		cfg.Shell.DeniedCommandPatterns = splitList(flags.denyPatterns)
	}
	if flags.listen != "" {
		cfg.HTTP.Listen = flags.listen
	}
	if flags.httpPath != "" {
		cfg.HTTP.Path = flags.httpPath
	}
	if flags.tokenEnv != "" {
		cfg.HTTP.TokenEnv = flags.tokenEnv
	}
	if flags.tokenFile != "" {
		cfg.HTTP.TokenFile = flags.tokenFile
	}
	if flags.tlsCert != "" {
		cfg.HTTP.TLSCert = flags.tlsCert
	}
	if flags.tlsKey != "" {
		cfg.HTTP.TLSKey = flags.tlsKey
	}
	if flags.accessLog != "" {
		cfg.HTTP.AccessLog = flags.accessLog
	}
	if flags.gatewayURL != "" {
		cfg.Gateway.URL = flags.gatewayURL
	}
	if flags.gatewayTokenEnv != "" {
		cfg.Gateway.TokenEnv = flags.gatewayTokenEnv
	}
	if flags.gatewayTokenFile != "" {
		cfg.Gateway.TokenFile = flags.gatewayTokenFile
	}
	if flags.gatewayFromAICLI {
		enabled := true
		cfg.Gateway.UseAICLIConfig = &enabled
	}
	if flags.gatewayAICLICfg != "" {
		cfg.Gateway.AICLIConfigFile = flags.gatewayAICLICfg
	}
	if flags.gatewayProxyURL != "" {
		cfg.Gateway.ProxyURL = flags.gatewayProxyURL
	}
	if flags.gatewayName != "" {
		cfg.Gateway.Name = flags.gatewayName
	}
	if flags.gatewayPathStyle != "" {
		cfg.Gateway.PathStyle = flags.gatewayPathStyle
	}
	if flags.gatewaySelfName != "" {
		cfg.Gateway.SelfName = flags.gatewaySelfName
	}
	if flags.gatewaySelfSlug != "" {
		cfg.Gateway.SelfSlug = flags.gatewaySelfSlug
	}
	if flags.gatewayIdentity != "" {
		cfg.Gateway.IdentityFile = flags.gatewayIdentity
	}
	if raw := strings.TrimSpace(flags.instance); raw != "" {
		// A CLI instance is the shorthand: it owns the identity path and the
		// default mount/node labels unless the operator explicitly overrode a
		// field with its dedicated flag. Clearing config values here prevents a
		// shared config's identityFile/selfName from silently defeating it.
		cfg.Gateway.Instance = raw
		if flags.gatewayIdentity == "" {
			cfg.Gateway.IdentityFile = ""
		}
		if flags.gatewaySelfName == "" {
			cfg.Gateway.SelfName = ""
		}
		if flags.gatewayName == "" {
			cfg.Gateway.Name = ""
		}
	}
	if flags.noMountSelf {
		disabled := false
		cfg.Gateway.MountSelf = &disabled
	}
	if len(flags.gatewayMounts) > 0 {
		cfg.Gateway.Mounts = append(cfg.Gateway.Mounts, flags.gatewayMounts...)
	}
	if raw := strings.TrimSpace(flags.backend); raw != "" {
		cfg.Backend = remotetools.Backend(strings.ToLower(raw))
	}
	if flags.envdAddr != "" {
		cfg.EnvD.Addr = strings.TrimSpace(flags.envdAddr)
	}
	if flags.envdUser != "" {
		cfg.EnvD.User = strings.TrimSpace(flags.envdUser)
	}
	if flags.envdTokenEnv != "" {
		cfg.EnvD.TokenEnv = strings.TrimSpace(flags.envdTokenEnv)
	}
	if flags.envdCAFile != "" {
		cfg.EnvD.CAFile = strings.TrimSpace(flags.envdCAFile)
	}
	if raw := strings.TrimSpace(flags.envdTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid --call-timeout %q (want a positive Go duration such as 30m)", raw)
		}
		cfg.EnvD.CallTimeout = remotetools.Duration{Duration: parsed}
	}
	if flags.envdNoFiles {
		cfg.EnvD.FilesChannelDisabled = true
	}
	cfg.Shell.AllowedCommands = append(cfg.Shell.AllowedCommands, flags.allowCommands...)
	cfg.Shell.DeniedCommands = append(cfg.Shell.DeniedCommands, flags.denyCommands...)
	cfg.Shell.AllowedCommandPatterns = append(cfg.Shell.AllowedCommandPatterns, flags.allowCommandPats...)
	cfg.Shell.DeniedCommandPatterns = append(cfg.Shell.DeniedCommandPatterns, flags.denyCommandPats...)
	cfg.Shell.DenyShellMetachars = flags.denyMetachars
	return cfg, nil
}

func runServe(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("serve", flags)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if flags.stdio && flags.http {
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: --stdio and --http are mutually exclusive")
		return 2
	}
	if !flags.stdio && !flags.http {
		// Config-first flow: `init` writes an HTTP daemon config, and a bare
		// `serve` should not need a second flag. SSH stdio stays explicit.
		flags.http = true
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: no transport flag; defaulting to --http (use --stdio for SSH stdio)")
	}

	// The remote server keeps its own config loader (YAML/base64/target policy),
	// but the MCP transport loop now lives in one place: remoteServe adapts the
	// resolved config to the same stdio/HTTP runner toolctl uses.
	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	if strings.TrimSpace(flags.instance) == "" &&
		cfg.Gateway.Instance == "" &&
		cfg.Gateway.IdentityFile == "" &&
		cfg.Gateway.SelfName == "" {
		// Default serve behaviour: one identity per workspace. The first
		// workspace may claim the legacy shared identity (normalizeGateway
		// decides); later workspaces get an auto-<hash> instance.
		cfg.Gateway.AutoInstance = true
	}
	if flags.stdio {
		cfg.Transport = "stdio"
	} else {
		cfg.Transport = "http"
		// A bare `serve` sets itself up on first run: default token, config
		// file, and (when present) the gateway connection aicli already knows.
		applyAutoInit(cfg, flags)
	}
	return remoteServe(cfg, flags.stdio)
}

// remoteServe is the single MCP transport loop for the remote server. Both
// transports share workspace jail, schema, error codes and audit; only process
// lifecycle differs. Keeping it here (rather than a second copy in toolctl)
// means a transport fix lands once for every remote surface.
func remoteServe(cfg *remotetools.Config, stdio bool) int {
	server, err := remotetools.NewServer(cfg, serverNameFor(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		cancel()
	}()

	if stdio {
		// stdio has no graceful shutdown signal of its own: the MCP server ends
		// when stdin closes, which is exactly the SSH session ending.
		if err := server.RunStdio(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: stdio server failed: %v\n", err)
			return 1
		}
		return 0
	}
	// Gateway mounts are an enhancement: a failure here (missing token, bad
	// identity path) must leave the local HTTP transport working unchanged.
	if err := server.EnableGateway(ctx, cfg.Gateway); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: gateway disabled: %v\n", err)
	}
	if err := server.RunHTTP(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: http server failed: %v\n", err)
		return 1
	}
	return 0
}

// doctorReport is the machine-readable shape of `doctor --json`.
type doctorReport struct {
	OK                     bool     `json:"ok"`
	Version                string   `json:"version"`
	Workspace              string   `json:"workspace"`
	AllowedRoots           []string `json:"allowedRoots"`
	Profile                string   `json:"profile"`
	Tools                  []string `json:"tools"`
	ToolPrefix             string   `json:"toolPrefix,omitempty"`
	TargetName             string   `json:"targetName,omitempty"`
	ServerName             string   `json:"serverName"`
	DescriptionStyle       string   `json:"descriptionStyle"`
	AuditPath              string   `json:"auditPath,omitempty"`
	AuditWritable          bool     `json:"auditWritable"`
	AuditPreviewBytes      int      `json:"auditPreviewBytes,omitempty"`
	JobsRoot               string   `json:"jobsRoot,omitempty"`
	JobsWritable           bool     `json:"jobsWritable"`
	LegacyRemoteJobs       bool     `json:"legacyRemoteJobs"`
	ArtifactsRoot          string   `json:"artifactsRoot,omitempty"`
	ArtifactsWritable      bool     `json:"artifactsWritable"`
	AllowedCommandPatterns []string `json:"allowedCommandPatterns,omitempty"`
	DeniedCommandPatterns  []string `json:"deniedCommandPatterns,omitempty"`
	HTTPListen             string   `json:"httpListen"`
	HTTPTokenSet           bool     `json:"httpTokenSet"`
	HTTPTokenLength        int      `json:"httpTokenLength"`
	HTTPNeedsToken         bool     `json:"httpNeedsToken"`
	HTTPAccessLog          string   `json:"httpAccessLog,omitempty"`
	Problems               []string `json:"problems"`
	Warnings               []string `json:"warnings"`
}

func runDoctor(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("doctor", flags)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	if err := cfg.Normalize(); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}

	report := buildDoctorReport(cfg)
	if flags.jsonOut {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", encodeErr)
			return 1
		}
		fmt.Fprintln(os.Stdout, string(encoded))
	} else {
		printDoctorReport(report)
	}
	if !report.OK {
		return 1
	}
	return 0
}

func buildDoctorReport(cfg *remotetools.Config) doctorReport {
	serverName := serverNameFor(cfg)
	report := doctorReport{
		Version:                version,
		Workspace:              cfg.Workspace,
		AllowedRoots:           cfg.EffectiveAllowedRoots(),
		Profile:                string(cfg.Profile),
		ToolPrefix:             cfg.ToolPrefix,
		TargetName:             cfg.TargetName,
		ServerName:             serverName,
		DescriptionStyle:       cfg.DescriptionStyle,
		AuditPath:              cfg.Audit.Path,
		AuditPreviewBytes:      cfg.Audit.CommandPreviewBytes,
		JobsRoot:               cfg.Jobs.Root,
		LegacyRemoteJobs:       cfg.Jobs.LegacyRemoteJobs,
		ArtifactsRoot:          cfg.Artifacts.Root,
		AllowedCommandPatterns: cfg.Shell.AllowedCommandPatterns,
		DeniedCommandPatterns:  cfg.Shell.DeniedCommandPatterns,
		HTTPListen:             cfg.HTTP.Listen,
		HTTPAccessLog:          cfg.HTTP.AccessLog,
	}
	report.Problems = []string{}
	report.Warnings = []string{}

	if _, err := os.Stat(cfg.Workspace); err != nil {
		report.Problems = append(report.Problems, fmt.Sprintf("workspace is not accessible: %v", err))
	}
	if len(cfg.AllowedRoots) == 1 {
		report.Warnings = append(report.Warnings, "only the workspace root is allowed; add --allowed-roots for extra paths")
	}

	// Building the tool list doubles as a schema-registration check: a tool whose
	// parameters fail canonicalization makes buildRegistry fail here, before
	// anyone depends on the server.
	server, err := remotetools.NewServer(cfg, serverName)
	if err != nil {
		report.Problems = append(report.Problems, err.Error())
	} else {
		report.Tools = server.ToolNames()
		_ = server.Close()
	}

	if cfg.Audit.Path != "" {
		dir := cfg.Audit.Path
		if idx := strings.LastIndexAny(dir, `/\`); idx > 0 {
			dir = dir[:idx]
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("audit directory is not creatable: %v", mkErr))
		} else if probeErr := probeWritable(cfg.Audit.Path); probeErr != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("audit path is not writable: %v", probeErr))
		} else {
			report.AuditWritable = true
		}
	}
	if cfg.Jobs.Root != "" {
		if probeErr := probeWritableDir(cfg.Jobs.Root); probeErr != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("jobs root is not writable: %v", probeErr))
		} else {
			report.JobsWritable = true
		}
	}
	if cfg.Artifacts.Root != "" {
		if probeErr := probeWritableDir(cfg.Artifacts.Root); probeErr != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("artifacts root is not writable: %v", probeErr))
		} else {
			report.ArtifactsWritable = true
		}
	}

	token := strings.TrimSpace(os.Getenv(cfg.HTTP.TokenEnv))
	report.HTTPTokenSet = token != ""
	report.HTTPTokenLength = len(token)
	host := cfg.HTTP.Listen
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = strings.Trim(host[:idx], "[]")
	}
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	report.HTTPNeedsToken = !loopback
	if !loopback {
		if !report.HTTPTokenSet {
			report.Problems = append(report.Problems,
				fmt.Sprintf("non-loopback listener %q requires %s", cfg.HTTP.Listen, cfg.HTTP.TokenEnv))
		} else if len(token) < 32 {
			report.Problems = append(report.Problems,
				fmt.Sprintf("token from %s is shorter than 32 bytes", cfg.HTTP.TokenEnv))
		}
	}
	if cfg.Profile == remotetools.ProfileOps && len(cfg.Shell.AllowedCommands) == 0 {
		report.Warnings = append(report.Warnings,
			"profile ops exposes the shell tool but shell.allowedCommands is empty: every command will be refused (fail-closed)")
	}
	if (cfg.Profile == remotetools.ProfileStandard || cfg.Profile == remotetools.ProfileOps) && cfg.ReadOnly {
		report.Warnings = append(report.Warnings,
			"readOnly=true overrides the profile: only read tools are exposed")
	}
	report.OK = len(report.Problems) == 0
	return report
}

func printDoctorReport(report doctorReport) {
	fmt.Fprintf(os.Stdout, "aicli-remote-tools doctor (%s)\n", report.Version)
	fmt.Fprintf(os.Stdout, "  workspace        : %s\n", report.Workspace)
	fmt.Fprintf(os.Stdout, "  allowed roots    : %s\n", strings.Join(report.AllowedRoots, ", "))
	fmt.Fprintf(os.Stdout, "  profile          : %s\n", report.Profile)
	fmt.Fprintf(os.Stdout, "  server name      : %s\n", report.ServerName)
	fmt.Fprintf(os.Stdout, "  tool prefix      : %s\n", orDash(report.ToolPrefix))
	fmt.Fprintf(os.Stdout, "  description      : %s\n", report.DescriptionStyle)
	fmt.Fprintf(os.Stdout, "  tools (%d)       : %s\n", len(report.Tools), strings.Join(report.Tools, ", "))
	fmt.Fprintf(os.Stdout, "  audit            : %s (writable=%v)\n", orDash(report.AuditPath), report.AuditWritable)
	fmt.Fprintf(os.Stdout, "  audit preview    : %d bytes\n", report.AuditPreviewBytes)
	fmt.Fprintf(os.Stdout, "  jobs root        : %s (writable=%v)\n", orDash(report.JobsRoot), report.JobsWritable)
	fmt.Fprintf(os.Stdout, "  legacy jobs      : %v (local-only v1 capability)\n", report.LegacyRemoteJobs)
	fmt.Fprintf(os.Stdout, "  artifacts root   : %s (writable=%v)\n", orDash(report.ArtifactsRoot), report.ArtifactsWritable)
	fmt.Fprintf(os.Stdout, "  allow patterns   : %s\n", orDash(strings.Join(report.AllowedCommandPatterns, ", ")))
	fmt.Fprintf(os.Stdout, "  deny patterns    : %s\n", orDash(strings.Join(report.DeniedCommandPatterns, ", ")))
	fmt.Fprintf(os.Stdout, "  http listen      : %s (token set=%v len=%d)\n",
		report.HTTPListen, report.HTTPTokenSet, report.HTTPTokenLength)
	fmt.Fprintf(os.Stdout, "  http access log  : %s\n", orDash(report.HTTPAccessLog))
	for _, problem := range report.Problems {
		fmt.Fprintf(os.Stdout, "  PROBLEM: %s\n", problem)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(os.Stdout, "  warning: %s\n", warning)
	}
	if report.OK {
		fmt.Fprintln(os.Stdout, "  result           : ok")
	} else {
		fmt.Fprintf(os.Stdout, "  result           : %d problem(s)\n", len(report.Problems))
	}
}

func runTools(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("tools", flags)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	server, err := remotetools.NewServer(cfg, serverNameFor(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	defer server.Close()

	names := server.ToolNames()
	sort.Strings(names)
	if flags.jsonOut {
		encoded, _ := json.MarshalIndent(map[string]interface{}{
			"workspace": cfg.Workspace,
			"profile":   cfg.Profile,
			"tools":     names,
		}, "", "  ")
		fmt.Fprintln(os.Stdout, string(encoded))
		return 0
	}
	for _, name := range names {
		fmt.Fprintln(os.Stdout, name)
	}
	return 0
}

// serverNameFor derives the MCP server name used for canonical-name budgeting.
//
// The local config normally names the server after the target (e.g. "dev"), so
// the same spelling is used here; a missing prefix falls back to "remote".
func serverNameFor(cfg *remotetools.Config) string {
	if cfg == nil {
		return "remote"
	}
	name := strings.TrimSpace(cfg.TargetName)
	if name == "" {
		name = "remote"
	}
	// Server names share the portable character set with tool names.
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_' || r == '-':
			builder.WriteRune(r)
		case r == ' ' || r == '.':
			builder.WriteRune('-')
		}
	}
	sanitized := strings.Trim(builder.String(), "-")
	if sanitized == "" {
		return "remote"
	}
	return sanitized
}

func probeWritable(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

// probeWritableDir verifies a directory can be created and written.
func probeWritableDir(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("path is empty")
	}
	if err := os.MkdirAll(trimmed, 0o700); err != nil {
		return err
	}
	probe, err := os.CreateTemp(trimmed, ".aicli-probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// ensure the signal type is referenced on all platforms
var _ = time.Second
