// Command aicli-envd-bridge exposes an envd target to ai-agent-runtime as an MCP
// server over stdio.
//
// Why a bridge exists at all: envd is a Connect-RPC daemon, not an MCP server, so
// it cannot be listed as an MCP entry directly. This process is the adapter. It is
// also the only place where policy can be enforced on the envd path, because envd
// itself performs no path checks and no command filtering — it runs whatever it is
// told to run, as whatever user the credentials name.
//
// The security model is therefore inverted relative to the ssh-stdio transport: on
// that path aicli-remote-tools on the target enforces the jail, while here the jail
// is local and must be applied before every envd call. Each tool below resolves the
// requested path first and refuses anything outside the workspace or the declared
// allowed roots.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// profile decides which tools are exposed, mirroring the aicli-remote-tools tiers.
type profile string

const (
	profileReadonly profile = "readonly"
	profileStandard profile = "standard"
	profileOps      profile = "ops"
)

// allows reports whether the profile exposes a tool.
//
// The tiers are cumulative so an operator can reason about them as a ladder:
// readonly reads, standard adds file mutation, ops adds command execution.
func (p profile) allows(tool string) bool {
	switch p {
	case profileReadonly:
		switch tool {
		case "view", "ls", "glob", "grep", "host_metrics", "watch_dir":
			return true
		}
		return false
	case profileStandard:
		switch tool {
		case "view", "ls", "glob", "grep", "host_metrics", "watch_dir", "write", "mkdir", "remove", "move":
			return true
		}
		return false
	case profileOps:
		// ops is the widest tier, so it accepts everything the bridge knows about.
		if _, ok := toolDefs[tool]; ok {
			return true
		}
		return false
	default:
		return false
	}
}

// bridge holds the resolved configuration and the envd client.
type bridge struct {
	client    *envdclient.Client
	workspace string
	roots     []string
	prefix    string
	profile   profile
	timeout   time.Duration
	audit     *auditLog
	commands  *remotetools.CommandPolicy

	sessionMu sync.Mutex
	sessions  map[uint32]*ptySession
}

// resolve maps a caller-supplied path into the jail.
//
// It is the single choke point for path policy. Everything the model asks for is
// made absolute against the workspace, cleaned, then checked to sit inside the
// workspace or one of the allowed roots. Parent traversal is rejected after
// cleaning, so "workspace/../../etc" cannot escape.
//
// A relative path is joined to the workspace. An absolute path is accepted only if
// it already sits inside an allowed root.
func (b *bridge) resolve(raw string) (string, error) {
	requested := strings.TrimSpace(raw)
	if requested == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsAny(requested, "\x00\r\n") {
		return "", errors.New("path must not contain NUL or newlines")
	}
	var candidate string
	if path.IsAbs(requested) {
		candidate = path.Clean(requested)
	} else {
		candidate = path.Clean(path.Join(b.workspace, requested))
	}

	if b.isInsideJail(candidate) {
		return candidate, nil
	}
	return "", fmt.Errorf("path %q is outside the workspace %q and the allowed roots", requested, b.workspace)
}

// toolDef is one MCP tool description.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// stringProp is a JSON-schema string property.
func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// toolDefs is the tool surface. Descriptions state what each tool does not do, so a
// caller is not misled about the jail.
var toolDefs = map[string]toolDef{
	"view": {
		Name: "view",
		Description: "Read a file from the envd target. Paths resolve against the workspace. " +
			"Returns the file content as text; binary content is reported rather than mangled.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": stringProp("File path, absolute or relative to the workspace")},
			"required":   []string{"path"},
		},
	},
	"ls": {
		Name: "ls",
		Description: "List a directory on the envd target. Returns name, type, size and mode " +
			"for each entry, sorted by name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  stringProp("Directory path, absolute or relative to the workspace"),
				"depth": map[string]any{"type": "integer", "description": "Recursion depth; 1 lists one level"},
			},
			"required": []string{"path"},
		},
	},
	"glob": {
		Name: "glob",
		Description: "Find files under a directory whose base name matches a shell glob pattern. " +
			"Implemented as a remote find, so it does not descend outside the given root.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    stringProp("Directory to search under"),
				"pattern": stringProp("Base-name glob, for example *.go"),
			},
			"required": []string{"path", "pattern"},
		},
	},
	"grep": {
		Name: "grep",
		Description: "Search file contents under a directory. Uses ripgrep when present on the " +
			"target and falls back to grep otherwise; the result says which was used.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    stringProp("Directory or file to search"),
				"pattern": stringProp("Regular expression to match"),
			},
			"required": []string{"path", "pattern"},
		},
	},
	"write": {
		Name: "write",
		Description: "Write content to a file on the envd target, creating parent directories. " +
			"Overwrites an existing file. The path must stay inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    stringProp("Destination file path"),
				"content": stringProp("Full file content to write"),
			},
			"required": []string{"path", "content"},
		},
	},
	"mkdir": {
		Name:        "mkdir",
		Description: "Create a directory on the envd target. An existing directory is reported, not treated as an error.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": stringProp("Directory path to create")},
			"required":   []string{"path"},
		},
	},
	"remove": {
		Name:        "remove",
		Description: "Delete a path on the envd target.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": stringProp("Path to delete")},
			"required":   []string{"path"},
		},
	},
	"move": {
		Name:        "move",
		Description: "Rename or move a path on the envd target. Both endpoints must be inside the jail.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":      stringProp("Existing path"),
				"destination": stringProp("New path"),
			},
			"required": []string{"source", "destination"},
		},
	},
	"shell": {
		Name: "shell",
		Description: "Run a command on the envd target and return its stdout, stderr and exit code. " +
			"The command must match the bridge's allowlist; envd itself adds no filtering. " +
			"Gated to the ops profile only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": stringProp("Shell command, run through /bin/sh -c"),
				"cwd":     stringProp("Working directory; defaults to the workspace and must stay inside it"),
			},
			"required": []string{"command"},
		},
	},
	"host_metrics": {
		Name:        "host_metrics",
		Description: "Report CPU count, memory and disk totals for the envd target.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	"watch_dir": {
		Name: "watch_dir",
		Description: "Watch a directory on the envd target for a bounded window and return change events. " +
			"The stream exists only for this call; call again for another window.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       stringProp("Directory path, absolute or relative to the workspace"),
				"wait_ms":    map[string]any{"type": "integer", "description": "How long to wait for events (default 5000, max 30000)"},
				"max_events": map[string]any{"type": "integer", "description": "Maximum change events to return (default 50, max 500)"},
			},
			"required": []string{"path"},
		},
	},
}

// rpcRequest is one JSON-RPC 2.0 request or notification.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// textContent is one MCP content block.
type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callResult is the MCP tools/call result.
//
// IsError marks a tool-level failure (a denied path, a missing file) so the model
// sees it as a failed tool call rather than a successful one with odd text.
type callResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
	// audit is metadata for the local audit sink. It is unexported, so it never
	// reaches the MCP response.
	audit commandAudit
}

// commandAudit carries the command-policy and exit metadata that the audit sink
// records without duplicating command output.
type commandAudit struct {
	PolicyRule  string
	ExitCode    *int
	OutputBytes int
}

// textResult builds a successful text result.
func textResult(format string, args ...any) callResult {
	return callResult{Content: []textContent{{Type: "text", Text: fmt.Sprintf(format, args...)}}}
}

// errorResult builds a failed text result.
func errorResult(format string, args ...any) callResult {
	return callResult{Content: []textContent{{Type: "text", Text: fmt.Sprintf(format, args...)}}, IsError: true}
}

func main() {
	var (
		addr        = flag.String("addr", "127.0.0.1:49983", "envd address as host:port")
		user        = flag.String("user", "", "OS user envd executes as (sent as HTTP Basic user)")
		workspace   = flag.String("workspace", "", "workspace directory that bounds all paths")
		tokenEnv    = flag.String("token-env", "AICLI_ENVD_TOKEN", "environment variable holding the envd access token, sent as X-Access-Token")
		allowRoot   = multiFlag{}
		prof        = flag.String("profile", "readonly", "tool profile: readonly|standard|ops")
		prefix      = flag.String("tool-prefix", "", "prefix applied to exposed tool names")
		caFile      = flag.String("ca-file", "", "CA bundle for a TLS envd endpoint")
		callTimeout = flag.Duration("call-timeout", 10*time.Minute, "maximum duration of one tool call; streaming shell runs are bounded by it")
		auditPath   = flag.String("audit-log", "", "append one JSONL record per tool call to this file (contents are never logged)")
		allowCmd    = stringListFlag{}
		allowCmdPat = stringListFlag{}
		denyCmd     = stringListFlag{}
		denyCmdPat  = stringListFlag{}
		denyMeta    = flag.Bool("deny-shell-metachars", true, "reject shell commands containing pipes, redirection, substitution or sequencing")
	)
	flag.Var(&allowRoot, "allow-root", "additional directory outside the workspace that may be accessed; repeatable")
	flag.Var(&allowCmd, "allow-command", "command or argv prefix allowed for shell/pty_start; repeatable; empty denies every command")
	flag.Var(&allowCmdPat, "allow-command-pattern", "whole-command wildcard allowlist entry (* and ?); repeatable")
	flag.Var(&denyCmd, "deny-command", "command or argv prefix always denied; repeatable")
	flag.Var(&denyCmdPat, "deny-command-pattern", "whole-command wildcard denylist entry (* and ?); repeatable")
	flag.Parse()

	if strings.TrimSpace(*user) == "" {
		fmt.Fprintln(os.Stderr, "aicli-envd-bridge: --user is required; envd rejects a request with no user")
		os.Exit(2)
	}
	if strings.TrimSpace(*workspace) == "" {
		fmt.Fprintln(os.Stderr, "aicli-envd-bridge: --workspace is required; it is the path jail")
		os.Exit(2)
	}
	bridgeWorkspace := path.Clean(strings.TrimSpace(*workspace))
	if !path.IsAbs(bridgeWorkspace) {
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: --workspace %q must be an absolute path; envd performs no path checks, so the jail cannot be anchored to a relative directory\n", *workspace)
		os.Exit(2)
	}
	for _, root := range allowRoot {
		if !path.IsAbs(root) {
			fmt.Fprintf(os.Stderr, "aicli-envd-bridge: --allow-root %q must be an absolute path\n", root)
			os.Exit(2)
		}
	}
	if *callTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "aicli-envd-bridge: --call-timeout must be a positive duration")
		os.Exit(2)
	}
	commandPolicy, err := remotetools.NewCommandPolicy(remotetools.ShellConfig{
		AllowedCommands:        allowCmd,
		AllowedCommandPatterns: allowCmdPat,
		DeniedCommands:         denyCmd,
		DeniedCommandPatterns:  denyCmdPat,
		DenyShellMetachars:     *denyMeta,
	}, *callTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: invalid command policy: %v\n", err)
		os.Exit(2)
	}
	bridgeProfile := profile(strings.ToLower(strings.TrimSpace(*prof)))
	switch bridgeProfile {
	case profileReadonly, profileStandard, profileOps:
	default:
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: unknown --profile %q (want readonly|standard|ops)\n", *prof)
		os.Exit(2)
	}
	if bridgeProfile == profileOps && len(allowCmd) == 0 && len(allowCmdPat) == 0 {
		fmt.Fprintln(os.Stderr, "aicli-envd-bridge: warning: profile ops but no --allow-command/--allow-command-pattern; shell and pty_start will refuse every command (fail-closed)")
	}
	audit, err := openOptionalAudit(*auditPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: open audit log: %v\n", err)
		os.Exit(2)
	}
	if audit != nil {
		defer func() { _ = audit.Close() }()
	}

	cfg := envdclient.Config{Addr: *addr, User: *user}
	if *tokenEnv != "" {
		if token := os.Getenv(*tokenEnv); token != "" {
			// The token travels in the X-Access-Token header, which is what envd
			// actually enforces once /init has installed one. The Basic password
			// slot is ignored by envd, so nothing is put there.
			cfg.AccessToken = token
		}
	}
	if *caFile != "" {
		tlsConfig, err := loadTLS(*caFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "aicli-envd-bridge: %v\n", err)
			os.Exit(2)
		}
		cfg.TLS = tlsConfig
	}

	client, err := envdclient.Dial(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: %v\n", err)
		os.Exit(2)
	}

	b := &bridge{
		client:    client,
		workspace: bridgeWorkspace,
		roots:     allowRoot,
		prefix:    *prefix,
		profile:   bridgeProfile,
		timeout:   *callTimeout,
		audit:     audit,
		commands:  commandPolicy,
		sessions:  make(map[uint32]*ptySession),
	}
	defer b.closeAllSessions()

	// A signal must not leave the target running a half-finished command, so the
	// context is cancelled and the loop drains before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := b.serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "aicli-envd-bridge: %v\n", err)
		os.Exit(1)
	}
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

// String implements flag.Value.
func (m *multiFlag) String() string { return strings.Join(*m, ",") }

// Set implements flag.Value.
func (m *multiFlag) Set(value string) error {
	if strings.TrimSpace(value) != "" {
		*m = append(*m, path.Clean(value))
	}
	return nil
}

// stringListFlag collects repeatable strings verbatim.
//
// It is separate from multiFlag because command patterns must not be passed
// through path.Clean: "git  status" and "host-*.sh" would be rewritten.
type stringListFlag []string

func (s *stringListFlag) String() string { return strings.Join(*s, ",") }

func (s *stringListFlag) Set(value string) error {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*s = append(*s, trimmed)
	}
	return nil
}

// serve runs the MCP stdio loop: newline-delimited JSON-RPC in, responses out.
func (b *bridge) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 1<<20)
	writer := bufio.NewWriter(out)
	defer func() { _ = writer.Flush() }()

	encoder := json.NewEncoder(writer)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				var req rpcRequest
				if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
					// A malformed line has no id, so it cannot be answered
					// meaningfully; report on stderr and keep the stream alive.
					fmt.Fprintf(os.Stderr, "aicli-envd-bridge: malformed request: %v\n", err)
				} else {
					resp, send := b.handle(ctx, req)
					if send {
						if err := encoder.Encode(resp); err != nil {
							return fmt.Errorf("write response: %w", err)
						}
						if err := writer.Flush(); err != nil {
							return fmt.Errorf("flush response: %w", err)
						}
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return io.EOF
			}
			return fmt.Errorf("read request: %w", readErr)
		}
	}
}

// handle dispatches one request.
//
// The second return value is false for notifications, which must not be answered.
func (b *bridge) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	isNotification := len(req.ID) == 0

	respond := func(result any, rpcErr *rpcError) (rpcResponse, bool) {
		if isNotification {
			return rpcResponse{}, false
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}, true
	}

	switch req.Method {
	case "initialize":
		return respond(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "aicli-envd-bridge", "version": "0.1.0"},
		}, nil)

	case "notifications/initialized", "initialized":
		return respond(nil, nil)

	case "ping":
		return respond(map[string]any{}, nil)

	case "tools/list":
		return respond(map[string]any{"tools": b.toolList()}, nil)

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return respond(nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()})
		}
		result := b.callTool(ctx, params.Name, params.Arguments)
		return respond(result, nil)

	default:
		if isNotification {
			return rpcResponse{}, false
		}
		return respond(nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// toolList returns the tools the profile exposes, with the prefix applied.
func (b *bridge) toolList() []toolDef {
	names := make([]string, 0, len(toolDefs))
	for name := range toolDefs {
		if b.profile.allows(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]toolDef, 0, len(names))
	for _, name := range names {
		def := toolDefs[name]
		def.Name = b.prefixed(name)
		out = append(out, def)
	}
	return out
}

// prefixed applies the configured tool prefix.
//
// The separator is "__", matching aicli-remote-tools: a target prefix such as
// "remote_10-0-0-9" plus "view" becomes "remote_10-0-0-9__view", so both remote
// transports expose the same spelling and tools.Manager sees one convention.
func (b *bridge) prefixed(name string) string {
	if b.prefix == "" {
		return name
	}
	return b.prefix + "__" + name
}

// unprefixed strips the prefix from a requested tool name.
//
// A bare tool name is also accepted: the profile check still applies, and a
// caller typing a name by hand should not have to reproduce the prefix.
func (b *bridge) unprefixed(name string) string {
	if b.prefix == "" {
		return name
	}
	if rest := strings.TrimPrefix(name, b.prefix+"__"); rest != name {
		return rest
	}
	return name
}

// callTool validates the profile, then dispatches to the implementation.
func (b *bridge) callTool(ctx context.Context, name string, args json.RawMessage) (out callResult) {
	base := b.unprefixed(name)
	started := time.Now()
	if b.audit != nil {
		// Record metadata only. The deferred call runs after the switch assigns
		// the result, so the ok flag reflects what the model was told.
		defer func() {
			entry := auditEntry{
				Tool:          base,
				Profile:       string(b.profile),
				OK:            !out.IsError,
				DurationMS:    time.Since(started).Milliseconds(),
				ArgumentBytes: len(args),
				PolicyRule:    out.audit.PolicyRule,
				ExitCode:      out.audit.ExitCode,
				OutputBytes:   out.audit.OutputBytes,
			}
			if b.client != nil {
				entry.Target = b.client.Addr()
				entry.User = b.client.User()
			}
			if err := b.audit.record(entry); err != nil {
				fmt.Fprintf(os.Stderr, "aicli-envd-bridge: audit log: %v\n", err)
			}
		}()
	}

	def, known := toolDefs[base]
	if !known {
		return errorResult("unknown tool %q", name)
	}
	if !b.profile.allows(base) {
		return errorResult("tool %q is not available under the %s profile", base, b.profile)
	}
	_ = def

	// Every call gets its own deadline so a wedged target cannot block the agent
	// loop forever. The value is configurable (--call-timeout) because real
	// operations such as migrations routinely outlive the old 60s default;
	// streaming shell runs are bounded by it too, since an unbounded call is
	// indistinguishable from a hang.
	callCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	switch base {
	case "view":
		return b.toolView(callCtx, args)
	case "ls":
		return b.toolLs(callCtx, args)
	case "glob":
		return b.toolGlob(callCtx, args)
	case "grep":
		return b.toolGrep(callCtx, args)
	case "write":
		return b.toolWrite(callCtx, args)
	case "mkdir":
		return b.toolMkdir(callCtx, args)
	case "remove":
		return b.toolRemove(callCtx, args)
	case "move":
		return b.toolMove(callCtx, args)
	case "shell":
		return b.toolShell(callCtx, args)
	case "host_metrics":
		return b.toolMetrics(callCtx)
	case "watch_dir":
		return b.toolWatchDir(callCtx, args)
	case "pty_start":
		return b.toolPtyStart(callCtx, args)
	case "pty_write":
		return b.toolPtyWrite(callCtx, args)
	case "pty_read":
		return b.toolPtyRead(callCtx, args)
	case "pty_resize":
		return b.toolPtyResize(callCtx, args)
	case "pty_close":
		return b.toolPtyClose(callCtx, args)
	default:
		return errorResult("tool %q is not implemented", base)
	}
}

// decodeArgs unmarshals tool arguments.
func decodeArgs(args json.RawMessage, target any) error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return json.Unmarshal(args, target)
}
