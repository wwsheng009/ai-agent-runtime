// Package consolehost starts native Windows console programs in a real
// Windows Console when the caller currently runs behind a pipe/PTY bridge.
//
// It is intentionally small and does not emulate a terminal. On Windows it
// asks CreateProcessW for CREATE_NEW_CONSOLE and lets conhost provide the
// child's standard console handles.
package consolehost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const FlagName = "--console-host"

var ErrUnsupported = errors.New("console hosting is only supported on Windows")

// BootstrapResult describes the early --console-host handling performed before
// Cobra or application initialization starts.
type BootstrapResult struct {
	// Args is the original argument list with --console-host occurrences
	// removed. The relaunched child must not receive the flag, which also makes
	// the handoff recursion-proof.
	Args []string

	// Launched reports whether a replacement process ran in a new console.
	Launched bool

	// ExitCode is the replacement process exit code when Launched is true.
	ExitCode int
}

// ExtractFlag removes --console-host from args and returns its effective
// boolean value. The last occurrence wins, matching normal flag parsing.
// Parsing stops at "--", so a literal argument named --console-host is kept.
func ExtractFlag(args []string) (enabled bool, remaining []string, err error) {
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}

		switch {
		case arg == FlagName:
			enabled = true
			if i+1 < len(args) {
				if value, parseErr := strconv.ParseBool(strings.TrimSpace(args[i+1])); parseErr == nil {
					enabled = value
					i++
				}
			}
		case strings.HasPrefix(arg, FlagName+"="):
			raw := strings.TrimSpace(strings.TrimPrefix(arg, FlagName+"="))
			value, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				return false, nil, fmt.Errorf("invalid %s value %q: %w", FlagName, raw, parseErr)
			}
			enabled = value
		default:
			remaining = append(remaining, arg)
		}
	}
	return enabled, remaining, nil
}

// BootstrapSelf handles --console-host for the current executable. It is
// designed to run at the very start of main, before configuration, logging, or
// terminal initialization.
func BootstrapSelf(args []string) (BootstrapResult, error) {
	return bootstrapSelf(args, bootstrapRuntime{
		supported:  Supported,
		hasConsole: HasConsole,
		executable: os.Executable,
		run:        RunInNewConsole,
	})
}

type bootstrapRuntime struct {
	supported  func() bool
	hasConsole func() bool
	executable func() (string, error)
	run        func(string, []string) (int, error)
}

func bootstrapSelf(args []string, runtime bootstrapRuntime) (BootstrapResult, error) {
	enabled, remaining, err := ExtractFlag(args)
	result := BootstrapResult{Args: remaining}
	if err != nil || !enabled {
		return result, err
	}
	if runtime.supported == nil || !runtime.supported() {
		return result, ErrUnsupported
	}
	if runtime.hasConsole != nil && runtime.hasConsole() {
		return result, nil
	}

	if runtime.executable == nil {
		return result, errors.New("console executable resolver is unavailable")
	}
	executable, err := runtime.executable()
	if err != nil {
		return result, fmt.Errorf("resolve current executable: %w", err)
	}
	if runtime.run == nil {
		return result, errors.New("new console runner is unavailable")
	}
	exitCode, err := runtime.run(executable, remaining)
	if err != nil {
		return result, err
	}
	result.Launched = true
	result.ExitCode = exitCode
	return result, nil
}

// Supported reports whether this platform can create a native Windows
// Console using the implementation in this package.
func Supported() bool {
	return platformSupported()
}

// HasConsole reports whether both stdin and stdout are native console handles.
// A pipe-backed mintty/Cygwin/MobaXterm process deliberately returns false.
func HasConsole() bool {
	return platformHasConsole()
}

// RunWithConsole runs executable attached to the current native console when
// one is available, otherwise it creates and waits for a new Windows Console.
func RunWithConsole(executable string, args []string) (int, error) {
	if !Supported() {
		return 1, ErrUnsupported
	}
	if strings.TrimSpace(executable) == "" {
		return 1, errors.New("console target executable is empty")
	}
	if HasConsole() {
		return runAttached(executable, args)
	}
	return RunInNewConsole(executable, args)
}

// RunInNewConsole creates executable in a new native Windows Console and waits
// for it to exit. Environment and current directory are inherited by the
// platform CreateProcess implementation.
func RunInNewConsole(executable string, args []string) (int, error) {
	if !Supported() {
		return 1, ErrUnsupported
	}
	if strings.TrimSpace(executable) == "" {
		return 1, errors.New("console target executable is empty")
	}
	exitCode, err := platformRunInNewConsole(executable, args)
	if err != nil {
		return 1, fmt.Errorf("start %q in a new console: %w", executable, err)
	}
	return exitCode, nil
}

func runAttached(executable string, args []string) (int, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, fmt.Errorf("start %q in the current console: %w", executable, err)
}
