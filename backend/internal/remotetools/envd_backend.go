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
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Two write scripts, chosen by the tool's own contract. Content is always "$2"
// and the path "$1", so no caller data is ever quoted into the script.
const (
	envdWriteScript      = `set -e; printf '%s' "$2" > "$1"`
	envdWriteMkdirScript = `set -e; d=$(dirname -- "$1"); [ -d "$d" ] || mkdir -p -- "$d"; printf '%s' "$2" > "$1"`
	// envdReadableScript asks the target kernel whether THIS user may open the
	// path. It exists because envd's /files endpoint opens files as the daemon
	// process (commonly root), while the process channel runs as cfg.User: serving
	// /files bytes without this check would let remote_x__view read files that
	// remote_x__shell cannot, which is a privilege difference and also a difference
	// from the local backend, where os.Open is refused by the kernel. Measured on a
	// live 0.2.11: GET /files?path=/etc/shadow returned 200 while the same user got
	// `Permission denied` locally.
	//
	// The answer comes from the kernel rather than from mode bits we reinterpret
	// here, because a Go copy of POSIX ownership, group and ACL rules would be a
	// second, wrong implementation of that decision.
	envdReadableScript = `set -e; if test -r "$1"; then exit 0; else echo "cannot open $1" >&2; exit 1; fi`
	// envdWritableScript asks whether THIS user may put these bytes at this path.
	// It walks up to the nearest existing ancestor first, because writing a new
	// file is a permission question about the directory, not about the file, and
	// apply_patch legitimately creates trees that do not exist yet.
	//
	// Same reason as the read gate: envd opens the destination as the daemon user
	// (measured: root), so without this check remote_x__write could overwrite a
	// file that remote_x__shell could not - the write direction of the same
	// asymmetry, and the worse one.
	envdWritableScript = `set -e; p="$1"; while [ ! -e "$p" ] && [ "$p" != "/" ]; do p=$(dirname -- "$p"); done; if test -w "$p"; then exit 0; else echo "cannot write $p" >&2; exit 1; fi`
)

// envdWriteChannel* name the transport that moved the written bytes.
const (
	envdWriteChannelFiles   = "files"
	envdWriteChannelProcess = "process"
)

// envdReadChannel* name the transport that actually moved the bytes, published
// as result metadata. envdGrep already reports which engine ran because the two
// dialects differ; a read channel is the same kind of fact, and a caller must be
// able to tell "native endpoint" from "fell back to cat" without reading prose.
const (
	envdReadChannelFiles   = "files"
	envdReadChannelProcess = "process"
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

	// The native /files channel is probed once, not per read: a deployment
	// without it (no route, a proxy that strips X-Access-Token, an uninstalled
	// token) should degrade on the first call and say why, rather than pay a
	// failing request for every file. filesNote keeps the reason so the fallback
	// stays visible in the result metadata instead of becoming silent.
	filesMu     sync.Mutex
	filesKnown  bool
	filesUsable bool
	filesNote   string
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
	if cfg.ShellExposed() &&
		len(cfg.Shell.AllowedCommands)+len(cfg.Shell.AllowedCommandPatterns) == 0 {
		// Fail-closed either way, but say it at startup instead of letting the
		// first shell call discover it.
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: warning: shell is visible but no allowlist is configured; shell and pty_start will refuse every command (fail-closed); %s\n",
			ShellPolicyEnableHint())
	}
	if strings.TrimSpace(cfg.Shell.Program) != "" {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: warning: shell.program is ignored for the envd backend; envd executes via /bin/sh on the target\n")
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
// refuseUncarriableWrite reports a payload that the only available channel cannot
// carry, or nil when the write may proceed.
//
// Both rules live here because both are properties of the PROCESS channel, not of
// the tool: argv cannot contain a NUL byte, and argv has a size ceiling. They are
// the operating system's limits, not a policy choice - so they apply only while the
// native endpoint is unavailable. Refusing a UTF-16 payload that `/files` could
// carry byte for byte would be a backend-specific rule masquerading as policy,
// which is the exact drift this merge exists to remove; the local backend has never
// refused those bytes.
//
// It is also called BEFORE the path is resolved, which keeps a write that is
// certain to fail from touching the target at all. That invariant was tested before
// this existed, and re-deriving it per tool is how two limits with two messages
// appear for one setting.
func (be *envdBackend) refuseUncarriableWrite(ctx context.Context, target string, payload []byte) *toolkit.ToolResult {
	usable, _ := be.filesChannelUsable(ctx)
	if usable {
		return nil
	}
	if strings.IndexByte(string(payload), 0) >= 0 {
		return envdFailureMeta("UNSUPPORTED", map[string]interface{}{
			"failure_class": "binary_payload",
			"file_path":     target,
		}, "cannot write %s: content contains NUL bytes (a BOM or UTF-16 encoding), which cannot be passed as a command-line argument, and envd's native file endpoint is unavailable here (%s). Use a UTF-8 payload, enable /files, or write it with shell", target, be.filesChannelNote())
	}
	if len(payload) > envdMaxWriteBytes {
		return envdFailureMeta("UNSUPPORTED", map[string]interface{}{
			"failure_class":  "payload_too_large",
			"file_size":      len(payload),
			"max_file_bytes": envdMaxWriteBytes,
		}, "content is %d bytes, above the %d byte write limit; split the file or use shell", len(payload), envdMaxWriteBytes)
	}
	return nil
}

// storeBytes writes one file. createParents mirrors the SHARED per-tool contract
// rather than this backend's convenience: `write`/`append_write` refuse a missing
// parent (mutatecore ValidateWriteTarget), while `apply_patch` add creates it. A
// single auto-mkdir for every write would make the same tool behave two ways
// depending on the transport, which is the drift this merge exists to remove -
// and it is a live concern here, because envd's own upload handler calls
// EnsureDirs unconditionally.
func (be *envdBackend) storeBytes(ctx context.Context, target string, encoded []byte, createParents bool) (int64, string, *toolkit.ToolResult) {
	// knownParent carries the directory entry when the caller already had to look
	// it up, so the write gate can answer the common case without a process spawn.
	var knownParent *envdclient.Entry
	if refusal := be.refuseUncarriableWrite(ctx, target, encoded); refusal != nil {
		return 0, "", refusal
	}
	if parent := path.Dir(target); !createParents && parent != "/" && parent != be.workspace {
		// Refuse exactly like the local surface: no silent directory creation.
		// This matters more now that the native channel exists, because envd's own
		// upload handler calls EnsureDirs and would happily create the tree.
		if entry, statErr := be.client.Stat(ctx, parent); statErr != nil {
			if !errorsAsConnectCode(statErr, "not_found") {
				return 0, "", envdErrorResult("%s", describeEnvDError(statErr, parent))
			}
			return 0, "", envdToolError(newToolError(CodeNotFound, "create the parent directory first",
				"parent directory does not exist: %s", parent))
		} else {
			knownParent = &entry
		}
	}

	if usable, _ := be.filesChannelUsable(ctx); usable {
		if refusal := be.refuseIfTargetCannotWrite(ctx, target, knownParent); refusal != nil {
			return 0, envdWriteChannelFiles, refusal
		}
		err := be.client.WriteFile(ctx, target, encoded)
		switch {
		case err == nil:
			entry, statErr := be.client.Stat(ctx, target)
			if statErr != nil {
				return int64(len(encoded)), envdWriteChannelFiles, envdErrorResult("write %s landed but size could not be confirmed: %v", target, statErr)
			}
			return entry.SizeBytes(), envdWriteChannelFiles, nil
		case envdclient.IsFilesChannelIssue(err):
			// Remember it once, then fall through to the process channel.
			be.markFilesUnusable(err)
		default:
			return 0, envdWriteChannelFiles, envdErrorResult("%s", describeFilesError(err, target))
		}
	}

	// The process channel carries content as one argv element; the rule lives in
	// refuseUncarriableWrite, which already ran before the path was resolved and
	// again here for callers that come straight to storeBytes.
	script := envdWriteScript
	if createParents {
		script = envdWriteMkdirScript
	}
	_, stderr, code, err := be.run(ctx, "/bin/sh", []string{"-c", script, "aicli-envd-write", target, string(encoded)})
	if err != nil {
		return 0, envdWriteChannelProcess, envdErrorResult("write %s: %v", target, err)
	}
	if code != 0 {
		return 0, envdWriteChannelProcess, envdErrorResult("write %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	entry, statErr := be.client.Stat(ctx, target)
	if statErr != nil {
		return int64(len(encoded)), envdWriteChannelProcess, envdErrorResult("write %s landed but size could not be confirmed: %v", target, statErr)
	}
	return entry.SizeBytes(), envdWriteChannelProcess, nil
}

// refuseIfTargetCannotWrite returns a refusal when the user this backend acts as
// could not create or replace this path, or nil when the write may proceed.
//
// The fast path is deliberately one-sided: it skips the probe only when the answer
// is certainly yes (the target, or its directory, is owned by this user with the
// relevant bits set). Anything uncertain goes to the target kernel, which is the
// only place that knows the real uid, groups and ACLs. Ownership is compared by
// name because envd reports both the owner name and the mode.
func (be *envdBackend) refuseIfTargetCannotWrite(ctx context.Context, target string, knownParent *envdclient.Entry) *toolkit.ToolResult {
	if be == nil || be.client == nil {
		return nil
	}
	user := be.client.User()
	if entry, err := be.client.Stat(ctx, target); err == nil {
		if entry.Owner == user && entry.Mode&0o777&0o200 != 0 {
			return nil
		}
	} else if !errorsAsConnectCode(err, "not_found") {
		return envdErrorResult("%s", describeEnvDError(err, target))
	} else if knownParent == nil {
		// The file is new: creating it is a question about the directory, and one
		// Stat answers it for the overwhelmingly common case (my own workspace),
		// which is what keeps apply_patch from spawning a probe per file.
		if parent, statErr := be.client.Stat(ctx, path.Dir(target)); statErr == nil {
			knownParent = &parent
		} else if !errorsAsConnectCode(statErr, "not_found") {
			return envdErrorResult("%s", describeEnvDError(statErr, path.Dir(target)))
		}
	}
	if knownParent != nil && knownParent.Owner == user &&
		knownParent.Mode&0o777&(0o200|0o100) == (0o200|0o100) {
		// A directory the user owns and may write and traverse: creation is certain.
		return nil
	}
	// Either the path does not exist yet or the cheap proof failed: ask the kernel,
	// which also resolves the "write into a directory" case.
	_, stderr, code, err := be.run(ctx, "/bin/sh", []string{"-c", envdWritableScript, "aicli-envd-writable", target})
	if err != nil {
		return envdErrorResult("cannot confirm write permission for %s: %v", target, err)
	}
	if code == 0 {
		return nil
	}
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = fmt.Sprintf("exit %d", code)
	}
	return envdFailureMeta(CodeInvalidArgument, map[string]interface{}{
		"file_path": target,
	}, "permission denied writing %s: user %s cannot create or replace it (%s)", target, user, detail)
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

// runCapped streams one command and stops at a byte ceiling instead of
// buffering to completion.
//
// Why this exists: Client.Run drains the whole stream and keeps at most
// DefaultMaxBufferedOutput bytes per stream, discarding the rest. For a recursive
// grep on a big tree that means paying for the entire scan (and silently losing
// its tail) when the surface was only ever going to show a bounded window. Here
// the reader stops as soon as the window is full and cancels the context, which
// envd treats as the supported way to end a run: the daemon reaps the child when
// its stream client disappears, so a capped search also stops doing work on the
// target rather than just hiding its output.
//
// truncated reports that decision, and code is then -1 because no end event ever
// arrives: the process was stopped on purpose, which is not a failure and must not
// be reported as one.
func (be *envdBackend) runCapped(ctx context.Context, cmd string, args []string, capBytes int) (stdout, stderr []byte, code int32, truncated bool, err error) {
	if be == nil || be.client == nil {
		return nil, nil, -1, false, errors.New("envd client is not configured")
	}
	runCtx, cancel := context.WithCancel(ctx)
	// Deferred so every exit path releases the derived context: the early-stop
	// branch below cancels explicitly for promptness, and cancel is idempotent.
	defer cancel()
	stream, err := be.client.Start(runCtx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{Cmd: cmd, Args: args, Cwd: be.workspace},
	})
	if err != nil {
		return nil, nil, -1, false, err
	}
	defer func() { _ = stream.Close() }()

	for {
		var event envdclient.ProcessEvent
		if recvErr := stream.Recv(&event); recvErr != nil {
			if truncated {
				// We stopped it; the missing end event is expected.
				return stdout, stderr, -1, true, nil
			}
			if errors.Is(recvErr, io.EOF) {
				return stdout, stderr, -1, false, errors.New("envd process stream ended without an exit event")
			}
			return stdout, stderr, -1, false, recvErr
		}
		switch event.Kind {
		case envdclient.EventData:
			stdout = append(stdout, event.Stdout...)
			stderr = append(stderr, event.Stderr...)
			if capBytes > 0 && len(stdout) >= capBytes {
				// Trim to the ceiling, not merely stop reading: envd delivers output in
				// frames, so one frame can land far past the limit, and a capture ceiling
				// that is exceeded by whatever the last frame happened to carry is not a
				// ceiling. (Measured live: a 512-byte cap returned 4210 bytes until this
				// line existed.)
				stdout = stdout[:capBytes]
				if capBytes > 0 && len(stderr) > capBytes {
					stderr = stderr[:capBytes]
				}
				truncated = true
				cancel()
				_ = stream.Close()
				return stdout, stderr, -1, true, nil
			}
		case envdclient.EventEnd:
			return stdout, stderr, event.End.ExitStatus(), false, nil
		}
	}
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

// envdFailure is a refusal that also carries the machine-readable fields the
// local surface publishes (code plus failure_class and its companions).
//
// The text stays complete on its own: a model reads it, a caller branches on the
// metadata. Without these keys an agent that reacts to, say, an ambiguous edit by
// reading `failure_class` would behave differently per transport, which is the
// same drift the merge exists to remove.
func envdFailure(code, format string, args ...interface{}) *toolkit.ToolResult {
	return envdFailureMeta(code, nil, format, args...)
}

// envdFailureMeta builds a refusal with caller-supplied metadata.
func envdFailureMeta(code string, meta map[string]interface{}, format string, args ...interface{}) *toolkit.ToolResult {
	message := fmt.Sprintf(format, args...)
	result := &toolkit.ToolResult{Success: false, Content: message, Error: errors.New(message)}
	if code != "" {
		if meta == nil {
			meta = map[string]interface{}{}
		}
		meta["error_code"] = code
	}
	if len(meta) > 0 {
		result.Metadata = meta
	}
	return result
}

// envdToolError renders an internal ToolError the way the local surface prints it
// (`CODE: message (next: ...)`) while also publishing the code as metadata, so a
// caller can branch on it instead of pattern-matching prose.
func envdToolError(err error) *toolkit.ToolResult {
	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		return envdFailureMeta(toolErr.Code, nil, "%v", err)
	}
	return envdErrorResult("%v", err)
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
	case ProfileAll:
		return 3
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
			description: base.describe("Read a window of a file on the envd target with stable line numbers and the same truncation metadata the local view publishes (offset, limit, lines_read, eof, is_truncated). Batch (files), symbol and compact are refused rather than ignored."),
			version:     "1.0.0",
			parameters:  envdViewParameters(),
			call:        envdView,
		},
	}, append(append(append(append(envdFileToolSpecs(base), envdEditToolSpecs(base)...), envdPatchSpec(base)), envdProcessToolSpecs(base)...), envdPtyToolSpecs(base)...)...)
}

// envdViewParameters publishes the local view's own parameter text for the subset
// this surface honours. Taking the descriptions from viewcore.Schema() rather than
// rewriting them is deliberate: two wordings for one parameter is how callers learn
// that `offset` might mean something different per transport, and `path` stays as
// the accepted alias because that spelling is already in use on this surface.
func envdViewParameters() map[string]interface{} {
	source := map[string]interface{}{}
	if properties, ok := viewcore.Schema()["properties"].(map[string]interface{}); ok {
		for _, key := range []string{"file_path", "offset", "limit"} {
			if value, ok := properties[key]; ok {
				source[key] = value
			}
		}
	}
	source["path"] = map[string]interface{}{
		"type":        "string",
		"description": "Alias for file_path.",
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": source,
		"required":   []string{},
	}
}

// envdView reads a file through envd and renders the same line window the local
// and ssh/http surfaces render.
//
// The window is not implemented here: `viewcore.ReadTextFromBytes` is the shared
// implementation (numbering, paging, long-line clamping, encoding, and the
// truncation accounting behind `eof`/`is_truncated`). envd only supplies the bytes,
// which is the one thing a remote target can answer differently. Writing a second
// windower here would be the exact drift this merge exists to remove.
//
// Two channels can move bytes here, and which one answers is published as
// read_channel so a caller can tell them apart rather than guess from prose -
// the same reason envdGrep publishes which engine ran:
//
//   - envd's native GET /files (see internal/envdclient/files.go) is preferred:
//     it is a real HTTP transfer with Range support, so no shell, no argv, no
//     exit-code interpretation and no locale in the middle.
//   - `/bin/cat` through the process channel is the fallback for deployments
//     where that endpoint is not usable, and it is what this surface did before
//     the channel existed.
//
// Either way the path first passes the jail, and only a file the TARGET USER may
// open is served: /files reads as the daemon user, so without that gate the same
// tool would see more through one channel than through the other.
func envdView(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "file_path", "path")
	if raw == "" {
		return envdErrorResult("file_path is required"), nil
	}
	if refusal := envdRefuseUnsupportedViewParams(params); refusal != nil {
		return refusal, nil
	}
	request := viewcore.Request{
		FilePath: raw,
		// The shared int reader already used by pty/process parameters, so this
		// surface does not grow a second idea of how a JSON number arrives.
		Offset: envdIntParam(params, "offset", 0),
		Limit:  envdIntParam(params, "limit", 0),
	}
	// Same normalisation the local surface applies: defaults, the hard cap, and a
	// negative offset meaning "the last N lines".
	request.Offset, request.Limit = viewcore.Normalize(request.Offset, request.Limit)
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
	size := entry.SizeBytes()
	content, stats, channel, note, refusal := be.readViewWindow(ctx, target, entry, request, limit)
	if refusal != nil {
		return refusal, nil
	}
	result := envdTextResult(content)
	// One metadata source for every view surface: paging fields must not be
	// reinvented per transport, or a caller paging a remote file gets a different
	// continuation contract than a caller paging a local one.
	metadata := viewcore.WindowMetadata(request, stats)
	metadata["file_path"] = target
	metadata["file_size"] = size
	metadata["read_channel"] = channel
	if note != "" {
		metadata["read_channel_note"] = note
	}
	result.Metadata = metadata
	viewcore.AttachEfficiencyHints(result.Metadata, &result.Content, request, stats)
	return result, nil
}

// envdViewFetchCeiling bounds how many bytes one view read will pull from the
// target while it searches for the requested line window. It is deliberately the
// same number as the shared core's own in-memory decode bound
// (viewcore.MaxDecodeBytes): a remote read must not be allowed to buffer more
// than the local surface is willing to hold.
const envdViewFetchCeiling int64 = 8 << 20

// readViewWindow serves one line window for a file of any size, and it is the
// only place that decides how many bytes to fetch.
//
// The window itself is still rendered by the shared core, so paging, numbering,
// long-line clamping and the truncation contract are identical to local. What the
// core cannot know is that a prefix we handed it was cut by *us*. Its rule is
// simple and must be respected: `stats.EOF` means "I saw the end of what you gave
// me", and `total_lines`/`is_truncated` are only trustworthy alongside it. Serving
// a partial prefix that the core believes is the whole file would publish
// `eof: true` for a file that has no end in sight - a lie about completeness,
// which is worse than a refusal.
//
// So the invariant is: serve only when the prefix is known to cover the request,
// which is true when (a) we fetched the whole file, or (b) the core stopped early
// on its own (budget or line limit with data still in hand), which is exactly when
// it reports HasMore. Anything else grows the fetch and retries, up to the
// ceiling; past the ceiling it refuses with a message that says what to do next.
//
// A BOM/UTF-16 file is only served whole. The core decodes such files in one
// pass, so cutting the prefix mid-codepoint would change the text, and "identical
// to local" stops being true; refusing is the honest option, and local refuses
// past its own decode bound too.
func (be *envdBackend) readViewWindow(ctx context.Context, target string, entry envdclient.Entry, request viewcore.Request, budget int) (string, viewcore.Stats, string, string, *toolkit.ToolResult) {
	size := entry.SizeBytes()
	want := int64(budget)
	if want > size {
		want = size
	}
	for {
		data, channel, note, refusal := be.readGuarded(ctx, target, entry, want)
		if refusal != nil {
			return "", viewcore.Stats{}, channel, note, refusal
		}
		complete := want >= size
		if !complete && hasByteOrderMark(data) {
			return "", viewcore.Stats{}, channel, note, envdFailure("UNSUPPORTED",
				"%s is %d bytes with a byte-order mark, above the %d byte view limit: an encoded file is only served whole, because decoding a cut prefix would change its text (the local surface applies the same rule past its decode bound). Use shell to convert it",
				target, size, budget)
		}
		content, stats, readErr := viewcore.ReadTextFromBytes(data, request, viewcore.Options{ByteBudget: budget})
		if readErr != nil {
			return "", viewcore.Stats{}, channel, note, envdErrorResult("%s", readErr.Error())
		}
		if complete || stats.HasMore {
			// Whole file, or the core stopped because of its own budget/line limit
			// while bytes remained in hand: either way nothing is being claimed
			// about an end we did not see.
			return content, stats, channel, note, nil
		}
		if want >= envdViewFetchCeiling || want >= size {
			// The prefix ends and the core still believes that is the end of the
			// file, but it is not: the requested window sits beyond what this
			// read is allowed to pull. Say so instead of publishing a false EOF.
			return "", stats, channel, note, envdFailure(CodeOutputLimit,
				"%s is %d bytes: the requested window starts beyond the %d bytes this read may fetch, so no line offset can be resolved here; lower the offset, narrow the limit, or read it with shell (for example sed -n '%dp')",
				target, size, envdViewFetchCeiling, request.Offset+1)
		}
		want *= 2
		if want > size {
			want = size
		}
		if want > envdViewFetchCeiling {
			want = envdViewFetchCeiling
		}
	}
}

// hasByteOrderMark reports whether these bytes open with a UTF BOM. The check is
// local on purpose: it mirrors the core's probe without reaching into it, and it
// only has to be conservative enough to route encoded files to the whole-file
// path.
func hasByteOrderMark(data []byte) bool {
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		return true
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return true
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return true
	}
	return false
}

// envdRefuseUnsupportedViewParams refuses the view parameters this surface cannot
// honour. Silently ignoring them is worse than refusing: a batch or symbol request
// that came back as a plain single read would teach the caller that the same
// argument names mean different things depending on transport.
func envdRefuseUnsupportedViewParams(params map[string]interface{}) *toolkit.ToolResult {
	for _, key := range []string{"files", "symbol", "compact"} {
		if _, ok := params[key]; ok {
			return envdFailure("UNSUPPORTED",
				"remote view over envd does not support %q yet; call it once per file with file_path, offset and limit", key)
		}
	}
	return nil
}

// refuseIfTargetCannotRead returns a refusal when the user this backend acts as
// cannot open the path, or nil when it may be served.
//
// World-readable files skip the probe: the answer is already certain, and 0644 is
// the common case in a source tree, so the check costs nothing there. Everything
// else (0640 root:adm inside /var/log, 0600 files of another user) is asked of the
// kernel on the target, which is the only place that knows the real uid, groups and
// ACLs.
func (be *envdBackend) refuseIfTargetCannotRead(ctx context.Context, target string, entry envdclient.Entry) *toolkit.ToolResult {
	if be == nil || be.client == nil {
		return nil
	}
	if entry.Mode&0o777&0o004 != 0 {
		return nil
	}
	_, stderr, code, err := be.run(ctx, "/bin/sh", []string{"-c", envdReadableScript, "aicli-envd-readable", target})
	if err != nil {
		// An inconclusive probe must not become a free pass: report the failure
		// instead of serving bytes we cannot prove are allowed.
		return envdErrorResult("cannot confirm read permission for %s: %v", target, err)
	}
	if code == 0 {
		return nil
	}
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = fmt.Sprintf("exit %d", code)
	}
	// The mode and owner come from the Stat this call already made, so the refusal
	// says why the file is unreachable without another round trip: "permission
	// denied" alone leaves the reader guessing whether it is ownership, a group, or
	// an ACL.
	owner := strings.TrimSpace(entry.Owner)
	if owner == "" {
		owner = "?"
	}
	group := strings.TrimSpace(entry.Group)
	if group == "" {
		group = "?"
	}
	return envdFailureMeta(CodeInvalidArgument, map[string]interface{}{
		"file_path": target,
	}, "permission denied reading %s: user %s cannot open it (%s %s:%s; %s)", target, be.client.User(), entry.Permissions, owner, group, detail)
}

// readGuarded is the only way a tool core gets file bytes: it applies the
// target-user readability gate and then reads through the preferred channel.
//
// Keeping the gate inside the read - rather than in each caller - is the point.
// `view` remembered to check permission; `edit` and `apply_patch` read the same
// file through the same backend and did not, which is exactly how a privilege
// asymmetry gets reintroduced one tool at a time. A refusal is returned, not an
// error, because it is a described answer for the model rather than a failure.
func (be *envdBackend) readGuarded(ctx context.Context, target string, entry envdclient.Entry, maxBytes int64) ([]byte, string, string, *toolkit.ToolResult) {
	if refusal := be.refuseIfTargetCannotRead(ctx, target, entry); refusal != nil {
		return nil, "", "", refusal
	}
	data, channel, note, err := be.readLimited(ctx, target, maxBytes)
	if err != nil {
		return nil, channel, note, envdErrorResult("%s", err.Error())
	}
	return data, channel, note, nil
}

// readLimited fetches at most maxBytes of one file, preferring the native
// /files channel and falling back to /bin/cat only when that channel is known
// unusable.
//
// A path-level answer from /files (no such file, is a directory) is reported as
// the refusal it is; it does not trigger a retry through the other channel, since
// re-asking the same question differently is how a real error turns into a
// surprising empty result.
func (be *envdBackend) readLimited(ctx context.Context, target string, maxBytes int64) ([]byte, string, string, error) {
	usable, note := be.filesChannelUsable(ctx)
	if usable {
		read, err := be.client.ReadFile(ctx, target, maxBytes)
		switch {
		case err == nil:
			return read.Data, envdReadChannelFiles, "", nil
		case envdclient.IsFilesChannelIssue(err):
			be.markFilesUnusable(err)
			// Fall through to the process channel with the reason attached.
		default:
			return nil, envdReadChannelFiles, "", fmt.Errorf("%s", describeFilesError(err, target))
		}
	}

	stdout, stderr, code, err := be.run(ctx, "/bin/cat", []string{target})
	if err != nil {
		return nil, envdReadChannelProcess, note, fmt.Errorf("read %s: %w", target, err)
	}
	if code != 0 {
		return nil, envdReadChannelProcess, note, fmt.Errorf("read %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	if maxBytes > 0 && int64(len(stdout)) > maxBytes {
		stdout = stdout[:maxBytes]
	}
	return stdout, envdReadChannelProcess, note, nil
}

// filesChannelUsable probes the native endpoint once and reports the cached
// verdict plus the reason it is unusable, if any.
func (be *envdBackend) filesChannelUsable(ctx context.Context) (bool, string) {
	if be == nil || be.client == nil {
		return false, "no envd client"
	}
	if be.cfg != nil && be.cfg.EnvD.FilesChannelDisabled {
		// An explicit refusal is not a failure: report it as the reason, and never
		// probe, so an operator who turned the endpoint off sees no request for it.
		return false, "disabled by configuration"
	}
	be.filesMu.Lock()
	defer be.filesMu.Unlock()
	if be.filesKnown {
		return be.filesUsable, be.filesNote
	}
	err := be.client.ProbeFiles(ctx)
	be.filesKnown = true
	be.filesUsable = err == nil
	if err != nil {
		be.filesNote = err.Error()
		return false, be.filesNote
	}
	return true, ""
}

// markFilesUnusable records that the native endpoint stopped working, so later
// reads go straight to the process channel instead of failing once per file.
func (be *envdBackend) markFilesUnusable(err error) {
	if be == nil {
		return
	}
	be.filesMu.Lock()
	defer be.filesMu.Unlock()
	be.filesKnown = true
	be.filesUsable = false
	be.filesNote = err.Error()
}

// filesChannelNote explains why the native endpoint is not in use, so a refusal
// caused by its absence can name the reason instead of looking like a rule the
// backend invented.
func (be *envdBackend) filesChannelNote() string {
	if be == nil {
		return "no envd client"
	}
	be.filesMu.Lock()
	defer be.filesMu.Unlock()
	if note := strings.TrimSpace(be.filesNote); note != "" {
		return note
	}
	return "not available"
}

// describeFilesError renders one /files failure in this surface's house style:
// what was asked, and what the target answered. The typed error drives control
// flow; its package-qualified text is not something a model should have to read
// to understand a refusal.
func describeFilesError(err error, target string) string {
	var filesErr *envdclient.FilesError
	if errors.As(err, &filesErr) {
		switch filesErr.Code {
		case envdclient.FilesNotFound:
			return fmt.Sprintf("path %s does not exist", target)
		case envdclient.FilesIsDirectory:
			return fmt.Sprintf("%s is a directory; use ls to list it", target)
		case envdclient.FilesUnauthorized:
			return fmt.Sprintf("envd refused the file channel for %s: the access token was not accepted", target)
		case envdclient.FilesChannelMissing:
			return fmt.Sprintf("envd exposes no file channel for %s", target)
		case envdclient.FilesRangeUnsatisfiable:
			return fmt.Sprintf("the requested window is beyond the end of %s", target)
		default:
			return fmt.Sprintf("read %s: envd answered http %d: %s", target, filesErr.Status, filesErr.Message)
		}
	}
	return fmt.Sprintf("read %s: %v", target, err)
}
