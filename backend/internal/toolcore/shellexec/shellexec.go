// Package shellexec implements the shared final-result shell execution core.
//
// It owns process-tree lifecycle, timeouts, environment handoff, combined
// stdout/stderr capture, optional artifact tee and the head/tail model-visible
// window. It deliberately does not implement interactive stdin, PTY, detach or
// background jobs: those remain local-only surfaces (design §11.1 / Phase 4).
//
// The package is surface-independent: callers supply the already-filtered
// environment and may rewrite the launch (for example an OS sandbox wrapper)
// through Prepare. Command authorization stays in the caller's CommandPolicy.
package shellexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
)

const (
	// DefaultCaptureLimit is the bounded raw capture retained before folding.
	DefaultCaptureLimit = runtimeexecutor.DefaultRetainedOutputBytes
	// DefaultModelWindow is the shell tool's model-visible head+tail budget.
	DefaultModelWindow = toolcoreoutput.ShellOutputBudgetBytes
)

// Launch is one resolved process launch.
//
// Env is passed through verbatim. A nil Env inherits the parent environment;
// an empty non-nil slice launches with an empty environment. Callers that need
// an allowlist must pass the filtered slice explicitly.
type Launch struct {
	Command string
	Args    []string
	Env     []string
	Workdir string
	// RawCmdLine is passed verbatim to CreateProcess on Windows. It is needed
	// when Command is cmd.exe and Args contains a complete command line:
	// os/exec's normal argv encoding uses MSVCRT quoting (\" for embedded
	// quotes), which cmd.exe does not understand. Empty means "use Args".
	RawCmdLine string
}

// Options configures one final-result shell execution.
type Options struct {
	// Command is the user/model supplied command line.
	Command string
	// Workdir is the process working directory. Empty means the process cwd.
	Workdir string
	// Env is the already-filtered environment. Nil inherits the parent.
	Env []string
	// Timeout bounds the whole command. Zero means no timeout.
	Timeout time.Duration
	// CaptureLimit bounds the raw retained capture. Zero uses DefaultCaptureLimit;
	// runtimeexecutor.DisableRetainedOutputLimit disables the cap.
	CaptureLimit int
	// ArtifactRoot is the preferred directory for the raw-output tee. Empty uses
	// the executor's default shell-output directory.
	ArtifactRoot string
	// ArtifactScope labels the artifact subdirectory (for example "toolkit").
	ArtifactScope string
	// Mirror receives live output chunks when non-nil.
	Mirror io.Writer
	// DisableArtifact skips the raw-output tee. Remote surfaces archive the
	// complete capture through their own artifact store instead.
	DisableArtifact bool
	// Invocation overrides shell detection. It must return argv (program plus
	// arguments). Nil uses the detected user shell with non-login flags.
	Invocation func(command string) (string, []string)
	// Shell identifies the shell selected by the caller. Zero uses the detected
	// user shell. Invocation still controls argv; Shell only tells the core how
	// to harden a PowerShell payload (UTF-8 prefix) and which shell to report.
	Shell runtimeexecutor.Shell
	// Prepare may rewrite the launch after shell resolution and before Start.
	// It is the hook for OS-level sandbox wrapping and launcher denials.
	Prepare func(ctx context.Context, launch Launch) (Launch, error)
	// QuietNotice controls the no-output diagnostic. Zero uses the executor
	// default; a negative value disables it.
	QuietNotice time.Duration
}

// Result is one completed (or failed-to-start) shell invocation.
//
// RunError is the process result: nil for exit 0, *exec.ExitError for a normal
// non-zero exit, and a launch error when the process could not start. TimedOut
// and Cancelled are control-plane outcomes and take precedence over RunError.
type Result struct {
	// Started is true once the process was launched. A false value with a
	// non-nil error is a launch/policy failure, not a process exit.
	Started       bool
	Capture       runtimeexecutor.CombinedOutputCapture
	ArtifactPath  string
	ArtifactError error
	ExitCode      int
	RunError      error
	TimedOut      bool
	Cancelled     bool
	Duration      time.Duration
	Shell         runtimeexecutor.Shell
	GuardReport   runtimeexecutor.TerminationReport
	WaitDelay     time.Duration
	WaitDelayUsed bool
}

// Run executes one final-result command.
//
// A setup failure (empty command, Prepare denial, guard bind failure) returns
// Result{} plus the error. A started process always returns a populated Result;
// its process error is available as Result.RunError.
func Run(ctx context.Context, opts Options) (Result, error) {
	if strings.TrimSpace(opts.Command) == "" {
		return Result{}, fmt.Errorf("shellexec: command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	shell := runtimeexecutor.CurrentShell()
	if strings.TrimSpace(opts.Shell.Path) != "" || opts.Shell.Type != "" {
		shell = opts.Shell
	}

	var launch Launch
	// Long scripts can travel over stdin instead of argv (configured by
	// shell.scriptDelivery). Empty means "argv carries the command".
	stdinScript := ""
	if opts.Invocation != nil {
		command, args := opts.Invocation(opts.Command)
		launch = Launch{Command: command, Args: append([]string(nil), args...)}
	} else {
		argv, script := shell.DeriveExecInvocation(opts.Command, runtimeexecutor.CurrentScriptDelivery())
		if len(argv) == 0 {
			return Result{}, fmt.Errorf("shellexec: shell resolution returned no argv")
		}
		stdinScript = script
		launch = Launch{Command: argv[0], Args: append([]string(nil), argv[1:]...)}
	}
	launch.Workdir = opts.Workdir
	launch.Env = opts.Env
	// A nil Env means "inherit the parent environment" (Go exec.Cmd semantics).
	// Materialize it first so NeutralizeConsoleStylingEnv keeps every inherited
	// variable and only overrides the styling-related ones, rather than
	// collapsing the environment to just the neutralized baseline.
	if launch.Env == nil {
		launch.Env = os.Environ()
	}
	// shell.env来自 runtime.yaml；放在 neutralisation 之前，让样式类变量
	// 仍由 NeutralizeConsoleStylingEnv 单点拥有。
	launch.Env = runtimeexecutor.ApplyShellEnvOverrides(launch.Env)
	// Neutralize console styling at the source (Codex-style UNIFIED_EXEC_ENV):
	// force NO_COLOR/TERM=dumb/cleared COLORTERM/pager=cat so captured output is
	// unstyled before any Prepare hook (bwrap, sudo, sandbox) can wrap it. The
	// PowerShell UTF-8 payload directive is applied separately below and
	// intentionally left intact.
	launch.Env = runtimeexecutor.NeutralizeConsoleStylingEnv(launch.Env)
	// PowerShell's UTF-8 directive belongs to the shell payload, so it must be
	// applied before a Prepare hook can wrap the launch (bwrap, sudo, ...).
	launch.Args = prefixPowerShellArgs(launch.Args, shell, launch.Command)
	if opts.Prepare != nil {
		prepared, err := opts.Prepare(ctx, launch)
		if err != nil {
			return Result{}, err
		}
		launch = prepared
	}
	if strings.TrimSpace(launch.Command) == "" {
		return Result{}, fmt.Errorf("shellexec: launch command is empty")
	}

	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = launch.Workdir
	cmd.Env = launch.Env
	if stdinScript != "" {
		cmd.Stdin = strings.NewReader(stdinScript)
	}
	applyRawCmdLine(cmd, launch.RawCmdLine)
	if opts.Mirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}

	guard := runtimeexecutor.NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		guard.Close()
		return Result{}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	captureLimit := opts.CaptureLimit
	if captureLimit == 0 {
		captureLimit = DefaultCaptureLimit
	}

	started := time.Now()
	capture, artifactPath, runErr, artifactErr := runtimeexecutor.CaptureCombinedOutputGuarded(runCtx, cmd, guard, runtimeexecutor.GuardedCaptureOptions{
		MaxBytes:        captureLimit,
		Scope:           opts.ArtifactScope,
		Command:         opts.Command,
		PreferredRoot:   opts.ArtifactRoot,
		Mirror:          opts.Mirror,
		DisableArtifact: opts.DisableArtifact,
		QuietNotice:     opts.QuietNotice,
	})

	result := Result{
		Started:       cmd.Process != nil,
		Capture:       capture,
		ArtifactPath:  artifactPath,
		ArtifactError: artifactErr,
		RunError:      runErr,
		Duration:      time.Since(started),
		Shell:         shell,
		GuardReport:   guard.Report(),
		WaitDelay:     guard.WaitDelay(),
		WaitDelayUsed: errors.Is(runErr, exec.ErrWaitDelay),
		ExitCode:      exitCode(runErr),
	}
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		result.TimedOut = true
	case ctx.Err() != nil:
		result.Cancelled = true
	}
	return result, runErr
}

// Fold bounds a captured stream to the model-visible head+tail window. It
// reuses the shared output core so local and remote surfaces cannot drift.
type FoldStats = toolcoreoutput.FoldStats

// Fold returns a head+tail window and the fold arithmetic.
func Fold(text string, budget int) (string, FoldStats, bool) {
	return toolcoreoutput.FoldHeadTail(text, budget)
}

// exitCode mirrors the shell convention: a process killed by signal N reports
// 128+N instead of Go's -1. A launch failure reports -1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exitErr.ExitCode()
	}
	return -1
}

// prefixPowerShellArgs keeps PowerShell output UTF-8 without corrupting an
// -EncodedCommand payload. It only applies when the launched program is
// actually PowerShell; a remote cmd.exe target must not inherit a local pwsh
// detection. program must be argv[0], not the user-supplied command text.
func prefixPowerShellArgs(args []string, shell runtimeexecutor.Shell, program string) []string {
	if len(args) < 2 {
		return args
	}
	if shell.Type != runtimeexecutor.ShellTypePowerShell && shell.Type != runtimeexecutor.ShellTypePwsh {
		return args
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(program))), ".exe")
	if base != "powershell" && base != "pwsh" {
		return args
	}
	if runtimeexecutor.ShellArgsUseEncodedCommand(args) {
		return args
	}
	last := len(args) - 1
	args[last] = runtimeexecutor.PowerShellCommandPrefix + args[last]
	return args
}

// HardenPowerShellArgs applies the UTF-8 output directive to a PowerShell argv.
// program is the executable path/name (argv[0]), not the user command. Background
// jobs build their own exec.Cmd instead of going through Run, so they need the
// same payload hardening without paying Run's synchronous lifecycle.
func HardenPowerShellArgs(args []string, shell runtimeexecutor.Shell, program string) []string {
	return prefixPowerShellArgs(args, shell, program)
}
