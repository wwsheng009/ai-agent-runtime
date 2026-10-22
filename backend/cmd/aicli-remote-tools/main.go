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
  aicli-remote-tools serve  --stdio [options]   # SSH transport (one process per connection)
  aicli-remote-tools serve  --http  [options]   # daemon transport (loopback by default)
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
  --jobs-root <path>        Enable job_* tools (durable job records)
  --artifacts-root <path>   Enable artifact_read for folded captures
  --artifacts-max-bytes <n> Per-artifact size limit (default 67108864)

HTTP options:
  --http-listen <addr>      Listen address (default 127.0.0.1:18765)
  --http-path <path>        MCP endpoint path (default /mcp)
  --http-token-env <name>   Env var holding the bearer token (default AICLI_REMOTE_TOKEN)
  --http-access-log <file>  NDJSON request log (includes 401s; no Authorization header)
  --tls-cert <file> --tls-key <file>

Examples:
  # SSH stdio (local side: ssh-client --stdio ...)
  aicli-remote-tools serve --stdio --workspace /srv/app --tool-prefix remote_dev

  # HTTP daemon bound to loopback, reached through an SSH local forward
  AICLI_REMOTE_TOKEN=$(openssl rand -hex 32) \
    aicli-remote-tools serve --http --workspace /srv/app --tool-prefix remote_dev
`, version)
}

// serveFlags is the flag set shared by serve/doctor/tools.
type serveFlags struct {
	configPath    string
	configB64     string
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
	artifactRoot  string
	artifactMax   int
	allowPatterns string
	denyPatterns  string

	stdio     bool
	http      bool
	listen    string
	httpPath  string
	tokenEnv  string
	tlsCert   string
	tlsKey    string
	accessLog string

	jsonOut bool
}

func newFlagSet(name string, flags *serveFlags) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&flags.configPath, "config", "", "YAML config file")
	fs.StringVar(&flags.configB64, "config-b64", "", "base64-encoded JSON config (overrides --config)")
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
	fs.StringVar(&flags.artifactRoot, "artifacts-root", "", "root for full tool captures")
	fs.IntVar(&flags.artifactMax, "artifacts-max-bytes", 0, "per-artifact size limit")
	fs.StringVar(&flags.allowPatterns, "allowed-command-patterns", "", "comma-separated whole-command wildcard allowlist (* and ?)")
	fs.StringVar(&flags.denyPatterns, "denied-command-patterns", "", "comma-separated whole-command wildcard denylist (* and ?)")

	fs.BoolVar(&flags.stdio, "stdio", false, "serve MCP over stdin/stdout")
	fs.BoolVar(&flags.http, "http", false, "serve MCP over Streamable HTTP")
	fs.StringVar(&flags.listen, "http-listen", "", "HTTP listen address")
	fs.StringVar(&flags.httpPath, "http-path", "", "HTTP endpoint path")
	fs.StringVar(&flags.tokenEnv, "http-token-env", "", "env var holding the bearer token")
	fs.StringVar(&flags.tlsCert, "tls-cert", "", "TLS certificate file")
	fs.StringVar(&flags.tlsKey, "tls-key", "", "TLS private key file")
	fs.StringVar(&flags.accessLog, "http-access-log", "", "NDJSON HTTP access log path")

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
	if flags.tlsCert != "" {
		cfg.HTTP.TLSCert = flags.tlsCert
	}
	if flags.tlsKey != "" {
		cfg.HTTP.TLSKey = flags.tlsKey
	}
	if flags.accessLog != "" {
		cfg.HTTP.AccessLog = flags.accessLog
	}
	return cfg, nil
}

func runServe(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("serve", flags)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if flags.stdio == flags.http {
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: exactly one of --stdio or --http is required")
		return 2
	}

	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: %v\n", err)
		return 1
	}
	if flags.stdio {
		cfg.Transport = "stdio"
	} else {
		cfg.Transport = "http"
	}
	serverName := serverNameFor(cfg)
	server, err := remotetools.NewServer(cfg, serverName)
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

	if flags.stdio {
		// stdio has no graceful shutdown signal of its own: the MCP server ends
		// when stdin closes, which is exactly the SSH session ending.
		if err := server.RunStdio(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: stdio server failed: %v\n", err)
			return 1
		}
		return 0
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
