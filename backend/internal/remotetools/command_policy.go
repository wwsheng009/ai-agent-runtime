package remotetools

import (
	"path/filepath"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// CommandPolicy is the command-authorization half of a remote sandbox.
//
// It is exported because envd performs no command filtering of its own: the local
// bridge must run the same checks before it forwards a command to envd. Sharing
// this implementation keeps the ssh-stdio/http and envd paths on one policy
// instead of maintaining two allowlist matchers that can drift apart.
type CommandPolicy struct {
	inner           *runtimeexecutor.Sandbox
	allowedCommands []string
	deniedCommands  []string
	allowedPatterns []string
	deniedPatterns  []string
	shell           ShellConfig
	timeout         time.Duration
	inheritEnv      bool
}

// NewCommandPolicy builds a command policy from the same ShellConfig used by
// aicli-remote-tools.
//
// The policy is fail-closed: an empty allowlist denies every command. The
// returned value is safe to call on a nil receiver, which lets a caller treat a
// missing policy as "shell disabled" without a second branch.
func NewCommandPolicy(shell ShellConfig, timeout time.Duration) (*CommandPolicy, error) {
	if err := validateCommandPatterns("shell.allowedCommandPatterns", shell.AllowedCommandPatterns); err != nil {
		return nil, err
	}
	if err := validateCommandPatterns("shell.deniedCommandPatterns", shell.DeniedCommandPatterns); err != nil {
		return nil, err
	}

	envWhitelist := effectiveShellEnvAllowlist(shell.EnvAllowlist, shell.DisableEnvInheritance)
	sandboxConfig := runtimeexecutor.SandboxConfig{
		Enabled:          true,
		AllowedCommands:  normalizeCommandNames(shell.AllowedCommands),
		DeniedCommands:   normalizeCommandNames(shell.DeniedCommands),
		EnvWhitelist:     envWhitelist,
		MaxExecutionTime: timeout,
	}
	return &CommandPolicy{
		inner:           runtimeexecutor.NewSandbox(&sandboxConfig),
		allowedCommands: sandboxConfig.AllowedCommands,
		deniedCommands:  sandboxConfig.DeniedCommands,
		allowedPatterns: normalizeCommandPatterns(shell.AllowedCommandPatterns),
		deniedPatterns:  normalizeCommandPatterns(shell.DeniedCommandPatterns),
		shell:           shell,
		timeout:         timeout,
		inheritEnv:      !shell.DisableEnvInheritance && shell.EnvAllowlist == nil,
	}, nil
}

// CheckCommand applies the command policy to one command string and returns the
// rule that produced the decision (for audit records).
//
// Order matters and mirrors the local engine: deny wins, then an allowlist that
// must be non-empty (fail closed), then the metacharacter refusal, then the
// destructive-command classifier.
func (p *CommandPolicy) CheckCommand(command string) (string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "policy:empty", newToolError(CodeInvalidArgument, "pass a non-empty command", "command is empty")
	}
	if p == nil {
		return "policy:no-shell", newToolError(
			CodeCommandDenied,
			ShellPolicyEnableHint(),
			"shell is not configured on this target",
		)
	}

	// Policy matching is argv-based, not whole-command based.
	//
	// executor.Sandbox.ValidateCommand compares filepath.Base(entire command),
	// so "echo hello" never matches an allowlist entry "echo". That is fine for
	// the local engine (which validates a resolved argv[0]) but wrong here: the
	// remote command is a full command line. The argv prefix is what an operator
	// means by "allow echo".
	fields := policyFields(trimmed)
	if len(fields) == 0 {
		return "policy:empty", newToolError(CodeInvalidArgument, "pass a non-empty command", "command is empty")
	}
	program := commandBaseName(fields[0])
	normalized := normalizeCommandLine(trimmed)

	// Deny list always wins, including for commands reached through a wrapper.
	for _, denied := range p.deniedCommands {
		if matchesCommandEntry(program, trimmed, denied) {
			rule := "deniedCommands:" + denied
			return rule, newToolError(CodeCommandDenied, "choose an allowed command", "command %q is denied by policy", program)
		}
	}
	// Wildcard deny patterns are evaluated before every allow rule. Denying a
	// broader shape is safe: the failure direction is a refused command.
	for _, pattern := range p.deniedPatterns {
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
	if len(p.allowedCommands) == 0 && len(p.allowedPatterns) == 0 {
		return "policy:fail-closed", newToolError(
			CodeCommandDenied,
			ShellPolicyEnableHint(),
			"remote shell is fail-closed: no allowedCommands/allowedCommandPatterns configured",
		)
	}

	rule := ""
	for _, entry := range p.allowedCommands {
		if matchesCommandEntry(program, trimmed, entry) {
			rule = "allowedCommands:" + entry
			break
		}
	}
	if rule == "" {
		for _, pattern := range p.allowedPatterns {
			if commandPatternMatches(normalized, pattern) {
				rule = "allowedCommandPatterns:" + pattern
				break
			}
		}
	}
	if rule == "" {
		return "policy:not-allowed", newToolError(
			CodeCommandDenied,
			ShellPolicyEnableHint(),
			"command %q is not in the allowlist", program,
		)
	}

	if p.shell.DenyShellMetachars {
		if reason := detectShellMetachars(trimmed); reason != "" {
			return "policy:metachars", newToolError(
				CodeCommandDenied,
				"run a single command without pipes, redirection or substitution, or explicitly set shell.denyShellMetachars: false (not recommended)",
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

// FilterEnv returns the environment for one command.
//
// Default mode inherits the complete target process environment (the machine and
// user environment union) after removing credential-like names. An explicit
// allowlist uses exactly the baseline plus the named variables, which lets an
// operator opt in to a credential for a tool that needs it. An explicit empty
// allowlist yields an empty environment.
func (p *CommandPolicy) FilterEnv(env []string) []string {
	if p == nil || p.inner == nil {
		return nil
	}
	if p.inheritEnv {
		return runtimeexecutor.FilterSensitiveEnv(env)
	}
	return p.inner.FilterEnv(env)
}

// Timeout returns the effective command timeout.
func (p *CommandPolicy) Timeout() time.Duration {
	if p == nil || p.timeout <= 0 {
		return 120 * time.Second
	}
	return p.timeout
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
	commandFields := policyFields(command)
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
