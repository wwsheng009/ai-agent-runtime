package remotetools

// envd_backend.go is the envd execution backend: the same MCP server, the same
// profile tiers, the same audit wrapper — with file and process operations
// executed on a remote envd daemon over Connect-RPC instead of the local
// filesystem.
//
// Why a backend and not a transport: envd is a Connect-RPC daemon, not an MCP
// server, and it performs no path checks and no command filtering of its own —
// it runs whatever it is told, as whatever user the credentials name. So the
// server process runs on THIS machine, jails every path against the TARGET, and
// composes each agent-facing tool from envd primitives. That is the shape the
// design called for (docs/architecture/envd-enhance-ai-agent-runtime.md §4.3:
// "实现同一个接口，让 Profile、Limits、audit 全部自动生效") and what the
// standalone aicli-envd-bridge process replaced (see the convergence plan in
// docs/plan/aicli-envd-bridge-merge-plan-20261028.md).
//
// M0 scope is a vertical slice: `view` only. What is being proven here is the
// wiring — profile gating, the target-side jail, the shared audit wrapper and
// the shared output budget — not the whole surface. The remaining tools land in
// M2 and the edit family in M3, each added to envdToolSpecs as data.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Two write scripts, chosen by the tool's own contract. Content is always "$2"
// and the path "$1", so no caller data is ever quoted into the script.
const (
	envdWriteScript      = `set -e; printf '%s' "$2" > "$1"`
	envdWriteMkdirScript = `set -e; d=$(dirname -- "$1"); [ -d "$d" ] || mkdir -p -- "$d"; printf '%s' "$2" > "$1"`
)

// envdBackend holds one envd connection plus the jail applied before every call.
type envdBackend struct {
	cfg       *Config
	client    *envdclient.Client
	workspace string   // target-side absolute workspace
	roots     []string // extra absolute roots, workspace excluded (it is implicit)
	timeout   time.Duration
	// commands is the SHARED command policy (same implementation as ssh/http):
	// envd filters nothing, so every shell/pty command is checked here first.
	commands *CommandPolicy

	// sessions holds live pty sessions; they outlive the tool call that created
	// them and are torn down by Server.Close.
	sessionMu sync.Mutex
	sessions  map[uint32]*envdPtySession
}

// newEnvDBackend dials envd from the normalized configuration.
//
// Dial performs no network I/O, so construction cannot fail merely because the
// target is down; the first real call reports that, described for an operator.
func newEnvDBackend(cfg *Config) (*envdBackend, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Backend != BackendEnvD {
		return nil, fmt.Errorf("envd backend requested but backend is %q", cfg.Backend)
	}
	clientCfg := envdclient.Config{
		Addr: cfg.EnvD.Addr,
		User: cfg.EnvD.User,
	}
	timeout := cfg.EnvD.CallTimeout.Duration
	if timeout <= 0 {
		timeout = envdDefaultCallTimeout
	}
	clientCfg.Timeout = timeout
	if tokenEnv := strings.TrimSpace(cfg.EnvD.TokenEnv); tokenEnv != "" {
		token := strings.TrimSpace(os.Getenv(tokenEnv))
		if token == "" {
			return nil, fmt.Errorf("envd backend: token environment variable %q is not set", tokenEnv)
		}
		clientCfg.AccessToken = token
	}
	if caFile := strings.TrimSpace(cfg.EnvD.CAFile); caFile != "" {
		tlsCfg, err := loadEnvDCA(caFile)
		if err != nil {
			return nil, err
		}
		clientCfg.TLS = tlsCfg
		// A CA file only makes sense against HTTPS: a bare host:port would
		// otherwise connect over plaintext while looking configured.
		if !strings.Contains(clientCfg.Addr, "://") {
			clientCfg.Addr = "https://" + clientCfg.Addr
		}
	}
	client, err := envdclient.Dial(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("envd backend: %w", err)
	}
	commands, err := NewCommandPolicy(cfg.Shell, timeout)
	if err != nil {
		return nil, fmt.Errorf("envd backend: invalid command policy: %w", err)
	}
	if cfg.Profile == ProfileOps && len(cfg.Shell.AllowedCommands)+len(cfg.Shell.AllowedCommandPatterns) == 0 {
		// Fail-closed either way, but say it at startup instead of letting the
		// first shell call discover it.
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: warning: profile ops but no allowed commands configured; shell and pty_start will refuse every command (fail-closed)\n")
	}
	extra := make([]string, 0, len(cfg.AllowedRoots))
	for _, root := range cfg.AllowedRoots {
		cleaned := path.Clean(strings.TrimSpace(root))
		if cleaned != "" && cleaned != cfg.Workspace {
			extra = append(extra, cleaned)
		}
	}
	return &envdBackend{
		cfg:       cfg,
		client:    client,
		workspace: cfg.Workspace,
		roots:     extra,
		timeout:   timeout,
		commands:  commands,
	}, nil
}

// loadEnvDCA builds a TLS config trusting one PEM bundle.
func loadEnvDCA(caFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %q contains no valid certificate", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// describeEnvDError turns the failures an operator actually hits into actionable
// text instead of an opaque Connect code.
func describeEnvDError(err error, target string) string {
	var connectErr *envdclient.ConnectError
	if errors.As(err, &connectErr) {
		switch {
		case connectErr.IsNotFound():
			return fmt.Sprintf("%s does not exist", target)
		case connectErr.IsUnauthenticated():
			return fmt.Sprintf("envd rejected the credentials for %s (%s); check --user and the token environment variable",
				target, connectErr.Message)
		case connectErr.Code == "permission_denied":
			return fmt.Sprintf("permission denied on %s: %s", target, connectErr.Message)
		}
	}
	return err.Error()
}

// envdResolveScript canonicalizes a path on the target.
//
// The lexical jail alone cannot see a symlink: a link inside the workspace can
// point at /etc and envd will follow it. The script resolves the path on the
// target instead. `realpath -m` is preferred because it also canonicalizes
// paths whose final components do not exist yet; the fallback resolves the
// nearest existing ancestor with `readlink -f` and re-attaches the suffix.
//
// The target path is passed as "$1" and never interpolated into the script.
const envdResolveScript = `set -e
target=$1
if command -v realpath >/dev/null 2>&1; then
  if out=$(realpath -m -- "$target" 2>/dev/null); then
    printf '%s\n' "$out"
    exit 0
  fi
fi
suffix=
cur=$target
while [ "$cur" != "/" ] && [ "$cur" != "." ] && [ ! -e "$cur" ] && [ ! -L "$cur" ]; do
  suffix="/$(basename "$cur")$suffix"
  cur=$(dirname "$cur")
done
if [ -L "$cur" ] && [ ! -e "$cur" ]; then
  # A dangling symlink can point outside; readlink -f may refuse it, so fail
  # closed rather than treating the link path as a missing component.
  printf 'cannot resolve dangling symlink %s\n' "$cur" >&2
  exit 1
elif [ -e "$cur" ]; then
  resolved=$(readlink -f "$cur" 2>/dev/null) || {
    printf 'cannot resolve %s\n' "$target" >&2
    exit 1
  }
else
  resolved=$cur
fi
case "$resolved" in
  /) ;;
  */) resolved=${resolved%/} ;;
esac
printf '%s%s\n' "$resolved" "$suffix"
`

// lexicalResolve maps a caller-supplied path into the jail without touching the
// target. It is the single entry point for path policy: everything the model
// asks for is made absolute against the workspace, cleaned, then required to sit
// inside the workspace or one of the allowed roots. Parent traversal is
// rejected after cleaning, so "workspace/../../etc" cannot escape.
func (be *envdBackend) lexicalResolve(raw string) (string, error) {
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
		candidate = path.Clean(path.Join(be.workspace, requested))
	}
	if be.insideJail(candidate) {
		return candidate, nil
	}
	return "", newToolError(CodePathDenied, "use a path inside the configured workspace",
		"path %q is outside the workspace %q and the allowed roots", requested, be.workspace)
}

// insideJail reports whether a canonical path is inside the workspace or one of
// the configured allowed roots.
func (be *envdBackend) insideJail(candidate string) bool {
	candidate = path.Clean(candidate)
	allowed := append([]string{be.workspace}, be.roots...)
	for _, root := range allowed {
		cleanRoot := path.Clean(root)
		if cleanRoot == "" {
			continue
		}
		if candidate == cleanRoot || strings.HasPrefix(candidate, cleanRoot+"/") {
			return true
		}
	}
	return false
}

// canonicalize resolves one absolute path on the target.
//
// It fails closed: a non-zero exit, empty output or a relative result is an
// error, not a reason to fall back to the lexical path.
func (be *envdBackend) canonicalize(ctx context.Context, target string) (string, error) {
	stdout, stderr, code, err := be.run(ctx, "/bin/sh", []string{"-c", envdResolveScript, "aicli-envd-resolve", target})
	if err != nil {
		return "", newToolError(CodeInternal, "check the envd connection", "resolve %s: %v", target, err)
	}
	if code != 0 {
		return "", newToolError(CodeInternal, "check that realpath or readlink exists on the target",
			"resolve %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	resolved := strings.TrimSpace(string(stdout))
	if resolved == "" || !path.IsAbs(resolved) {
		return "", newToolError(CodeInternal, "retry the call", "resolve %s returned %q, want an absolute path", target, resolved)
	}
	if strings.ContainsAny(resolved, "\x00\r\n") {
		return "", newToolError(CodeInternal, "retry the call", "resolve %s returned an unusable path", target)
	}
	return path.Clean(resolved), nil
}

// resolvePath is the choke point every envd file tool must pass through: the
// lexical jail, then target-side canonicalization, then the jail check against
// the canonical path. Callers must use the returned path for the actual
// operation, so a symlink cannot redirect a second lookup.
func (be *envdBackend) resolvePath(ctx context.Context, raw string) (string, error) {
	lexical, err := be.lexicalResolve(raw)
	if err != nil {
		return "", err
	}
	resolved, err := be.canonicalize(ctx, lexical)
	if err != nil {
		return "", err
	}
	if !be.insideJail(resolved) {
		return "", newToolError(CodePathDenied, "use a path inside the configured workspace",
			"path %q resolves to %q, outside the workspace %q and the allowed roots", raw, resolved, be.workspace)
	}
	return resolved, nil
}

// protectedRoot reports whether a path is one of the jail roots themselves.
// Removing or renaming a root is never a file-level operation.
func (be *envdBackend) protectedRoot(candidate string) bool {
	candidate = path.Clean(candidate)
	for _, root := range append([]string{be.workspace}, be.roots...) {
		if candidate == path.Clean(root) {
			return true
		}
	}
	return false
}

// resolveMutationPath prepares a path for unlink or rename.
//
// The final component is deliberately not followed: removing or renaming a
// symlink acts on the link itself. The parent is resolved and checked, because an
// intermediate symlink (workspace/link/file) would otherwise redirect the
// operation outside the jail.
func (be *envdBackend) resolveMutationPath(ctx context.Context, raw string) (string, error) {
	lexical, err := be.lexicalResolve(raw)
	if err != nil {
		return "", err
	}
	if be.protectedRoot(lexical) {
		return "", newToolError(CodePathDenied, "target a path inside the workspace",
			"refusing to mutate protected root %q", lexical)
	}
	parent := path.Dir(lexical)
	name := path.Base(lexical)
	resolvedParent, err := be.canonicalize(ctx, parent)
	if err != nil {
		return "", err
	}
	if !be.insideJail(resolvedParent) {
		return "", newToolError(CodePathDenied, "use a path inside the configured workspace",
			"path %q resolves through %q, outside the workspace %q and the allowed roots", raw, resolvedParent, be.workspace)
	}
	target := path.Join(resolvedParent, name)
	if !be.insideJail(target) {
		return "", newToolError(CodePathDenied, "use a path inside the configured workspace",
			"path %q resolves to %q, outside the workspace %q and the allowed roots", raw, target, be.workspace)
	}
	return target, nil
}

// workspaceRelative renders a canonical target path the way the tool layer
// presents paths: relative to the workspace when it sits under it.
func (be *envdBackend) workspaceRelative(target string) string {
	cleaned := path.Clean(target)
	root := path.Clean(be.workspace)
	if cleaned == root {
		return "."
	}
	if strings.HasPrefix(cleaned, root+"/") {
		return strings.TrimPrefix(cleaned, root+"/")
	}
	return cleaned
}

// storeBytes writes bytes to a jailed path through envd, then confirms the landed
// byte count.
//
// The NUL check is a property of the transport, not a policy choice: envd has no
// WriteFile RPC, so content travels as one argv element and argv cannot carry a
// NUL byte. The refusal names the reason and the alternative rather than letting
// the write land truncated.
//
// createParents mirrors the SHARED per-tool contract rather than this backend's
// convenience: `write`/`append_write` refuse a missing parent (mutatecore
// ValidateWriteTarget), while `apply_patch` add creates it. A single auto-mkdir
// for every write would make the same tool behave two ways depending on the
// transport, which is the drift this merge exists to remove.
func (be *envdBackend) storeBytes(ctx context.Context, target string, encoded []byte, createParents bool) (int64, *toolkit.ToolResult) {
	if strings.IndexByte(string(encoded), 0) >= 0 {
		return 0, envdErrorResult("cannot write %s through envd: content contains NUL bytes (a BOM or UTF-16 encoding); the local backend round-trips those, this one carries content as one argv element. Use a UTF-8 payload or shell for binary content", target)
	}
	if len(encoded) > envdMaxWriteBytes {
		return 0, envdErrorResult("content is %d bytes, above the %d byte write limit; split the file or use shell", len(encoded), envdMaxWriteBytes)
	}
	script := envdWriteScript
	if createParents {
		script = envdWriteMkdirScript
	} else if parent := path.Dir(target); parent != "/" && parent != be.workspace {
		// Refuse exactly like the local surface: no silent directory creation.
		if _, statErr := be.client.Stat(ctx, parent); statErr != nil {
			if !errorsAsConnectCode(statErr, "not_found") {
				return 0, envdErrorResult("%s", describeEnvDError(statErr, parent))
			}
			return 0, envdErrorResult("%v", newToolError(CodeNotFound, "create the parent directory first",
				"parent directory does not exist: %s", parent))
		}
	}
	_, stderr, code, err := be.run(ctx, "/bin/sh", []string{"-c", script, "aicli-envd-write", target, string(encoded)})
	if err != nil {
		return 0, envdErrorResult("write %s: %v", target, err)
	}
	if code != 0 {
		return 0, envdErrorResult("write %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	entry, statErr := be.client.Stat(ctx, target)
	if statErr != nil {
		return int64(len(encoded)), envdErrorResult("write %s landed but size could not be confirmed: %v", target, statErr)
	}
	return entry.SizeBytes(), nil
}

// run executes one argv command on the target with cwd set to the workspace.
func (be *envdBackend) run(ctx context.Context, cmd string, args []string) (stdout, stderr []byte, code int32, err error) {
	if be == nil || be.client == nil {
		return nil, nil, -1, errors.New("envd client is not configured")
	}
	return be.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{Cmd: cmd, Args: args, Cwd: be.workspace},
	})
}

// viewLimit is the payload ceiling for one remote read. It reuses the same
// precedence as the local backend (remoteToolBase.maxViewBytes) so one Limits
// block governs both backends.
func (be *envdBackend) viewLimit() int {
	if be == nil || be.cfg == nil {
		return DefaultMaxBytesPerCall
	}
	if be.cfg.Limits.CaptureLimit > 0 {
		return be.cfg.Limits.CaptureLimit
	}
	if be.cfg.Limits.MaxBytesPerCall > 0 {
		return be.cfg.Limits.MaxBytesPerCall
	}
	return DefaultMaxBytesPerCall
}

// outputBudget mirrors remoteOutput.Budget: the model-visible window after
// folding, never wider than the remote hard cap (Normalize already clamps it).
func (be *envdBackend) outputBudget(string) int {
	if be == nil || be.cfg == nil || be.cfg.Limits.ModelVisibleCap <= 0 {
		return DefaultModelVisibleCap
	}
	return be.cfg.Limits.ModelVisibleCap
}

// envdToolSpec is one envd-backed tool: identity, schema and the execution
// closure. Specs stay data so profile gating stays a single loop, matching the
// candidate-list shape the local backend uses.
type envdToolSpec struct {
	name        string
	description string
	version     string
	parameters  map[string]interface{}
	call        func(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error)
}

// envdTool adapts one spec to the toolkit.Tool interface, so the envd backend
// registers into the same registry, prefix wrapper and audit wrapper as the
// local backend instead of owning a second serving loop.
type envdTool struct {
	spec        envdToolSpec
	backend     *envdBackend
	description string
}

func (t *envdTool) Name() string { return t.spec.name }

func (t *envdTool) Description() string { return t.description }

func (t *envdTool) Version() string {
	if strings.TrimSpace(t.spec.version) == "" {
		return "1.0.0"
	}
	return t.spec.version
}

func (t *envdTool) Parameters() map[string]interface{} { return t.spec.parameters }

func (t *envdTool) CanDirectCall() bool { return true }

// Execute bounds the call by the configured deadline and folds the result into
// the model-visible window with the shared output core, so a remote read cannot
// spend more of the context than an ssh read of the same file.
func (t *envdTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, t.backend.timeout)
	defer cancel()

	result, err := t.spec.call(callCtx, t.backend, params)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &toolkit.ToolResult{}
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["remote"] = true
	result.Metadata["backend"] = string(BackendEnvD)
	result.Metadata["workspace"] = t.backend.workspace
	if result.Success && result.Content != "" && !toolcoreoutput.IsSelfSizedWindowTool(t.spec.name) {
		shaped, metadata, _, _ := toolcoreoutput.ShapeText(t.spec.name, result.Content, result.Metadata, 0, t.backend.outputBudget(t.spec.name))
		result.Content = shaped
		result.Metadata = metadata
	}
	return result, nil
}

// envdTextResult is a successful text result; the output kind stays implicit so
// the shared normalization in toolkit infers it.
func envdTextResult(content string) *toolkit.ToolResult {
	return &toolkit.ToolResult{Success: true, Content: content}
}

// envdErrorResult is a model-visible refusal. It travels as an MCP tool result
// with IsError=true (not a protocol error) so the model can read the reason and
// self-correct, exactly like ToolError does on the local backend.
func envdErrorResult(format string, args ...interface{}) *toolkit.ToolResult {
	message := fmt.Sprintf(format, args...)
	return &toolkit.ToolResult{Success: false, Content: message, Error: errors.New(message)}
}

// envdStringParam reads the first non-empty string among the named keys. The
// envd `view` accepts both the local spelling (file_path) and the bridge
// spelling (path) so a model trained on either surface gets a result instead of
// a schema error.
func envdStringParam(params map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := params[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// envdExtensionTiers pins the tools that exist only on the envd backend to the
// tier they belong to.
//
// This table is the whole of the backend-specific policy: a shared name means
// exactly what cfg.enabledTools() says it means, and an extension joins the same
// readonly subset ⊂ standard ⊂ ops ladder. Keeping it to a name→tier map is what
// prevents a second profile system from growing next to the shared one.
var envdExtensionTiers = map[string]Profile{
	ToolHostMetrics: ProfileReadonly, // capacity read, no side effects
	ToolWatchDir:    ProfileReadonly, // watching is read-only semantics
	ToolMkdir:       ProfileStandard,
	ToolRemove:      ProfileStandard,
	ToolMove:        ProfileStandard,
	// The interactive surface is ops-only: it starts a process and then lets the
	// session run without per-line filtering.
	ToolPtyStart:  ProfileOps,
	ToolPtyWrite:  ProfileOps,
	ToolPtyRead:   ProfileOps,
	ToolPtyResize: ProfileOps,
	ToolPtyClose:  ProfileOps,
}

// profileRank orders the tiers; custom is handled separately because it is an
// explicit list rather than a rung.
func profileRank(p Profile) int {
	switch p {
	case ProfileReadonly:
		return 0
	case ProfileStandard:
		return 1
	case ProfileOps:
		return 2
	default:
		return -1
	}
}

// envdToolEnabled decides whether the envd backend exposes one tool name.
//
// ReadOnly is a hard override on both sources, matching the shared rule that a
// misconfigured ops profile cannot expose a mutation tool when the operator asked
// for read-only.
func envdToolEnabled(cfg *Config, name string) bool {
	if cfg == nil {
		return false
	}
	for _, enabled := range cfg.enabledTools() {
		if enabled == name {
			return true
		}
	}
	tier, ok := envdExtensionTiers[name]
	if !ok || cfg.ReadOnly {
		return false
	}
	if cfg.Profile == ProfileCustom {
		// A custom profile is an exact list; an extension must be named in it.
		for _, listed := range cfg.Tools {
			if strings.TrimSpace(listed) == name {
				return true
			}
		}
		return false
	}
	for _, listed := range cfg.Tools {
		if strings.TrimSpace(listed) == name {
			return profileRank(cfg.Profile) >= profileRank(tier)
		}
	}
	if len(cfg.Tools) > 0 {
		// An explicit tool list narrows every surface, extensions included.
		return false
	}
	return profileRank(cfg.Profile) >= profileRank(tier)
}

// buildEnvDRegistry constructs the tool registry for the envd backend.
//
// Gating goes through envdToolEnabled, so a profile value means the same thing on
// both backends and the envd-only tools join the same ladder as declared tiers.
// A tool the backend has not implemented yet is simply not a candidate. Names are
// rewritten by the same newPrefixedTool wrapper, which is what keeps a remote tool
// from ever colliding with a local builtin.
func buildEnvDRegistry(cfg *Config, workspace *Workspace) (*buildResult, error) {
	if cfg == nil || workspace == nil {
		return nil, fmt.Errorf("config and workspace are required")
	}
	backend, err := newEnvDBackend(cfg)
	if err != nil {
		return nil, err
	}
	base := remoteToolBase{cfg: cfg, workspace: workspace}
	registry := toolkit.NewRegistry()
	for _, spec := range envdToolSpecs(base) {
		if !envdToolEnabled(cfg, spec.name) {
			continue
		}
		tool := &envdTool{spec: spec, backend: backend, description: spec.description}
		if err := registry.Register(newPrefixedTool(tool, cfg.ToolPrefix)); err != nil {
			return nil, fmt.Errorf("register envd %s: %w", spec.name, err)
		}
	}
	return &buildResult{registry: registry, envd: backend}, nil
}

// envdToolSpecs lists what the envd backend can serve today.
//
// M0 was a vertical slice (`view` only) to prove the wiring. M2 appends the
// migrated file surface; the process surface (shell, pty_*), watch_dir and the
// edit family land in later steps, each as data in these tables rather than new
// plumbing.
func envdToolSpecs(base remoteToolBase) []envdToolSpec {
	return append([]envdToolSpec{
		{
			name:        ToolView,
			description: base.describe("Read one file from the envd target as text. Paths resolve inside the workspace and symlinks are canonicalized on the target. There is no line windowing on this surface yet, so a file above the read limit is refused rather than half-read."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "File path on the target, absolute or relative to the workspace.",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Alias for file_path.",
					},
				},
			},
			call: envdView,
		},
	}, append(append(append(append(envdFileToolSpecs(base), envdEditToolSpecs(base)...), envdPatchSpec(base)), envdProcessToolSpecs(base)...), envdPtyToolSpecs(base)...)...)
}

// envdView reads a file through envd.
//
// envd exposes no ReadFile RPC (探测返回 404，见 envd-implementation-report.md
// §2.4），所以读取通过进程完成；路径先过 jail 再用规范路径操作。
func envdView(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "file_path", "path")
	if raw == "" {
		return envdErrorResult("file_path is required"), nil
	}
	target, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	entry, err := be.client.Stat(ctx, target)
	if err != nil {
		return envdErrorResult("%s", describeEnvDError(err, target)), nil
	}
	if entry.IsDir() {
		return envdErrorResult("%s is a directory; use ls to list it", target), nil
	}
	limit := be.viewLimit()
	if size := entry.SizeBytes(); size > int64(limit) {
		return envdErrorResult("%s is %d bytes, above the %d byte view limit; read it with a narrower tool or shell", target, size, limit), nil
	}
	stdout, stderr, code, err := be.run(ctx, "/bin/cat", []string{target})
	if err != nil {
		return envdErrorResult("read %s: %v", target, err), nil
	}
	if code != 0 {
		return envdErrorResult("read %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr))), nil
	}
	return envdTextResult(string(stdout)), nil
}
