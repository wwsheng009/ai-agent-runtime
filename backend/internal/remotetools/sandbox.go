package remotetools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// Sandbox wraps the shared executor.Sandbox for one remote target.
//
// The remote side reuses executor.Sandbox instead of implementing a second path
// checker (review D7): it already provides lexical allow/deny/read-only roots,
// symlink-resolved re-checks, command allow/deny lists and an env whitelist. The
// only remote-specific addition is the shell metacharacter refusal, because a
// remote command string is one more parsing layer than a local argv call.
type Sandbox struct {
	inner           *runtimeexecutor.Sandbox
	config          runtimeexecutor.SandboxConfig
	workspace       *Workspace
	shell           ShellConfig
	allowedPatterns []string
	deniedPatterns  []string
	timeout         time.Duration
}

// NewSandbox materializes the remote sandbox from configuration.
//
// The allowed path set is exactly the workspace jail, so a path that passes the
// jail can never fail the sandbox for a different reason. ReadOnlyPaths is empty
// because the profile already decides whether write tools exist at all; making
// every path read-only would instead break the `standard` profile.
func NewSandbox(cfg *Config, workspace *Workspace) (*Sandbox, error) {
	if cfg == nil || workspace == nil {
		return nil, fmt.Errorf("config and workspace are required")
	}
	sandboxConfig := runtimeexecutor.SandboxConfig{
		Enabled:          true,
		AllowedPaths:     workspace.AllowedRoots(),
		DeniedPaths:      nil,
		ReadOnlyPaths:    nil,
		AllowedCommands:  normalizeCommandNames(cfg.Shell.AllowedCommands),
		DeniedCommands:   normalizeCommandNames(cfg.Shell.DeniedCommands),
		EnvWhitelist:     append([]string(nil), cfg.Shell.EnvAllowlist...),
		MaxExecutionTime: cfg.Limits.CommandTimeout.Duration,
	}
	return &Sandbox{
		inner:           runtimeexecutor.NewSandbox(&sandboxConfig),
		config:          sandboxConfig,
		workspace:       workspace,
		shell:           cfg.Shell,
		allowedPatterns: normalizeCommandPatterns(cfg.Shell.AllowedCommandPatterns),
		deniedPatterns:  normalizeCommandPatterns(cfg.Shell.DeniedCommandPatterns),
		timeout:         cfg.Limits.CommandTimeout.Duration,
	}, nil
}

// CheckPath validates one already-resolved absolute path for an operation.
func (s *Sandbox) CheckPath(op runtimeexecutor.PermissionOp, absPath string) error {
	if s == nil || s.inner == nil {
		return newToolError(CodeInternal, "check the server configuration", "sandbox is not initialized")
	}
	if err := s.inner.CheckPermission(op, absPath); err != nil {
		return newToolError(CodePathDenied, "use a path inside the workspace", "%v", err)
	}
	return nil
}

// CheckCommand applies the command policy to one command string and returns the
// rule that produced the decision (for audit records).
//
// Order matters and mirrors the local engine: deny wins, then an allowlist that
// must be non-empty (fail closed), then the metacharacter refusal, then the
// destructive-command classifier.
func (s *Sandbox) CheckCommand(command string) (string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "policy:empty", newToolError(CodeInvalidArgument, "pass a non-empty command", "command is empty")
	}
	if s == nil {
		return "policy:no-shell", newToolError(CodeCommandDenied, "enable the shell profile", "shell is not configured")
	}

	// Policy matching is argv-based, not whole-command based.
	//
	// executor.Sandbox.ValidateCommand compares filepath.Base(entire command),
	// so "echo hello" never matches an allowlist entry "echo". That is fine for
	// the local engine (which validates a resolved argv[0]) but wrong here: the
	// remote command is a full command line. The argv prefix is what an operator
	// means by "allow echo".
	fields := shellrisk.Fields(trimmed)
	if len(fields) == 0 {
		return "policy:empty", newToolError(CodeInvalidArgument, "pass a non-empty command", "command is empty")
	}
	program := commandBaseName(fields[0])
	normalized := normalizeCommandLine(trimmed)

	// Deny list always wins, including for commands reached through a wrapper.
	for _, denied := range s.config.DeniedCommands {
		if matchesCommandEntry(program, trimmed, denied) {
			rule := "deniedCommands:" + denied
			return rule, newToolError(CodeCommandDenied, "choose an allowed command", "command %q is denied by policy", program)
		}
	}
	// Wildcard deny patterns are evaluated before every allow rule. Denying a
	// broader shape is safe: the failure direction is a refused command.
	for _, pattern := range s.deniedPatterns {
		if commandPatternMatches(normalized, pattern) {
			rule := "deniedCommandPatterns:" + pattern
			return rule, newToolError(
				CodeCommandDenied,
				"choose a different command",
				"command %q is denied by pattern %q", program, pattern,
			)
		}
	}

	// Fail closed when the allowlist is empty. executor.Sandbox treats an empty
	// allowlist as "allow everything", which is the wrong remote default
	// (design §7.4).
	if len(s.config.AllowedCommands) == 0 && len(s.allowedPatterns) == 0 {
		return "policy:fail-closed", newToolError(
			CodeCommandDenied,
			"add the command to shell.allowedCommands or shell.allowedCommandPatterns on the target",
			"remote shell is fail-closed: no allowedCommands/allowedCommandPatterns configured",
		)
	}

	rule := ""
	for _, entry := range s.config.AllowedCommands {
		if matchesCommandEntry(program, trimmed, entry) {
			rule = "allowedCommands:" + entry
			break
		}
	}
	if rule == "" {
		for _, pattern := range s.allowedPatterns {
			if commandPatternMatches(normalized, pattern) {
				rule = "allowedCommandPatterns:" + pattern
				break
			}
		}
	}
	if rule == "" {
		return "policy:not-allowed", newToolError(
			CodeCommandDenied,
			"add it to shell.allowedCommands/allowedCommandPatterns or use a narrower command",
			"command %q is not in the allowlist", program,
		)
	}

	if s.shell.DenyShellMetachars {
		if reason := detectShellMetachars(trimmed); reason != "" {
			return "policy:metachars", newToolError(
				CodeCommandDenied,
				"run a single command without pipes, redirection or substitution",
				"command contains %s", reason,
			)
		}
	}

	// Reuse the shared destructive-command classifier instead of keeping another
	// substring blacklist (review D7).
	if assessment := shellrisk.Assess(trimmed); assessment.Risky() {
		finding, _ := assessment.First()
		return "policy:destructive", newToolError(
			CodeCommandDenied,
			"this command is classified as destructive; run it manually on the target",
			"command classified as %s (target %q)", finding.Risk, finding.Target,
		)
	}
	return rule, nil
}

// commandBaseName returns the lower-case base name of an argv[0], dropping a
// Windows executable extension.
func commandBaseName(argv0 string) string {
	name := strings.ToLower(strings.TrimSpace(argv0))
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimSuffix(name, ext)
			break
		}
	}
	return name
}

// matchesCommandEntry reports whether one policy entry matches the command.
//
// Entries may be:
//   - a bare program name ("echo") - matches the argv[0] base name;
//   - an argv prefix ("git status") - matches the command tokens in order;
//   - "*" - matches everything (an explicit opt-out).
func matchesCommandEntry(program, command, entry string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" {
		return false
	}
	if entry == "*" {
		return true
	}
	if entry == program {
		return true
	}
	entryFields := shellrisk.Fields(entry)
	if len(entryFields) <= 1 {
		return false
	}
	commandFields := shellrisk.Fields(command)
	if len(commandFields) < len(entryFields) {
		return false
	}
	for index, expected := range entryFields {
		if strings.ToLower(commandFields[index]) != strings.ToLower(expected) {
			return false
		}
	}
	return true
}

// FilterEnv returns the environment for one command.
//
// An empty whitelist yields an empty environment (not the inherited one): a remote
// command should not silently inherit credentials from the server process.
func (s *Sandbox) FilterEnv(env []string) []string {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.FilterEnv(env)
}

// Timeout returns the effective command timeout.
func (s *Sandbox) Timeout() time.Duration {
	if s == nil || s.timeout <= 0 {
		return 120 * time.Second
	}
	return s.timeout
}

// normalizeCommandNames trims and lowercases command names.
func normalizeCommandNames(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, item := range input {
		trimmed := strings.ToLower(strings.TrimSpace(item))
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// shellMetacharPattern matches the constructs that turn one allowed command into
// an arbitrary command: pipes, redirection, sequencing, command substitution and
// background execution.
//
// Quoted occurrences are stripped before matching so an argument that legitimately
// contains `|` (e.g. a regex) is not rejected.
var metacharCandidates = []struct {
	pattern string
	reason  string
}{
	{";", "a command separator (;)"},
	{"&&", "a command separator (&&)"},
	{"||", "a command separator (||)"},
	{"|", "a pipe (|)"},
	{"`", "command substitution (`)"},
	{"$(", "command substitution ($(...))"},
	{">", "output redirection (>)"},
	{"<", "input redirection (<)"},
	{"\n", "a newline"},
}

// detectShellMetachars reports the first metacharacter construct found in the
// unquoted portion of the command.
func detectShellMetachars(command string) string {
	unquoted := stripQuotedSpans(command)
	for _, candidate := range metacharCandidates {
		if strings.Contains(unquoted, candidate.pattern) {
			return candidate.reason
		}
	}
	return ""
}

// stripQuotedSpans removes single- and double-quoted spans, preserving length
// with spaces so offsets stay meaningful for future diagnostics.
func stripQuotedSpans(command string) string {
	var builder strings.Builder
	builder.Grow(len(command))
	var quote rune
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			builder.WriteRune(' ')
		case r == '\'' || r == '"':
			quote = r
			builder.WriteRune(' ')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// validateWriteTarget rejects targets that cannot be written as a regular file:
// directories, device nodes and FIFOs.
func validateWriteTarget(absPath string) error {
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// New file: the parent must exist and be a directory so a typo does
			// not silently create a deep tree outside the intended place.
			parent := filepath.Dir(absPath)
			parentInfo, parentErr := os.Stat(parent)
			if parentErr != nil {
				return newToolError(CodeNotFound, "create the parent directory first", "parent directory does not exist: %s", parent)
			}
			if !parentInfo.IsDir() {
				return newToolError(CodeInvalidArgument, "the parent path is not a directory", "parent is not a directory: %s", parent)
			}
			return nil
		}
		return newToolError(CodeInvalidArgument, "check permissions on the target", "cannot stat %s: %v", absPath, err)
	}
	if info.IsDir() {
		return newToolError(CodeInvalidArgument, "pass a file path", "target is a directory: %s", absPath)
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeNamedPipe != 0, mode&os.ModeSocket != 0, mode&os.ModeDevice != 0, mode&os.ModeCharDevice != 0:
		return newToolError(CodeInvalidArgument, "pass a regular file", "target is a special file: %s", absPath)
	}
	return nil
}

// mustRegexp compiles a pattern known to be valid at package scope.
func mustRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
