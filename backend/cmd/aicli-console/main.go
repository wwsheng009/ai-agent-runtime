// aicli-console is a small native console launcher for aicli.
//
// From a pipe-backed Windows terminal such as MobaXterm/mintty it creates a
// new conhost window. From cmd, PowerShell, or another real Windows Console it
// keeps the current console. Launcher-only --target arguments are consumed;
// all remaining arguments are forwarded to aicli unchanged.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/consolehost"
)

const (
	targetFlagName            = "--target"
	targetEnvironmentVariable = "AICLI_CONSOLE_TARGET"
)

func main() {
	os.Exit(runConsoleLauncher(os.Args[1:], os.Stderr))
}

func runConsoleLauncher(args []string, stderr io.Writer) int {
	explicitTarget, forwardedArgs, err := parseConsoleLauncherArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "aicli-console: %v\n", err)
		return 2
	}

	target, err := resolveAICLIExecutable(explicitTarget)
	if err != nil {
		fmt.Fprintf(stderr, "aicli-console: %v\n", err)
		return 1
	}

	exitCode, err := consolehost.RunWithConsole(target, forwardedArgs)
	if err != nil {
		fmt.Fprintf(stderr, "aicli-console: %v\n", err)
		return 1
	}
	return exitCode
}

// parseConsoleLauncherArgs consumes launcher-only arguments before "--".
// The last --target occurrence wins. A literal --target intended for aicli can
// be placed after "--", which is forwarded together with the remaining args.
func parseConsoleLauncherArgs(args []string) (target string, forwarded []string, err error) {
	forwarded = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			forwarded = append(forwarded, args[i:]...)
			break
		}

		switch {
		case arg == targetFlagName:
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires an executable path", targetFlagName)
			}
			i++
			target = strings.TrimSpace(args[i])
			if target == "" {
				return "", nil, fmt.Errorf("%s requires a non-empty executable path", targetFlagName)
			}
		case strings.HasPrefix(arg, targetFlagName+"="):
			target = strings.TrimSpace(strings.TrimPrefix(arg, targetFlagName+"="))
			if target == "" {
				return "", nil, fmt.Errorf("%s requires a non-empty executable path", targetFlagName)
			}
		default:
			forwarded = append(forwarded, arg)
		}
	}
	return target, forwarded, nil
}

func resolveAICLIExecutable(explicitTarget string) (string, error) {
	launcher, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve launcher path: %w", err)
	}
	return resolveAICLIExecutableFrom(
		launcher,
		explicitTarget,
		os.Getenv(targetEnvironmentVariable),
		exec.LookPath,
	)
}

func resolveAICLIExecutableFrom(
	launcher string,
	explicitTarget string,
	environmentTarget string,
	lookPath func(string) (string, error),
) (string, error) {
	launcher, err := filepath.Abs(launcher)
	if err != nil {
		return "", fmt.Errorf("make launcher path absolute: %w", err)
	}

	if explicitTarget = strings.TrimSpace(explicitTarget); explicitTarget != "" {
		target, err := requireExecutableFile(explicitTarget)
		if err != nil {
			return "", fmt.Errorf("%s: %w", targetFlagName, err)
		}
		if sameExecutablePath(launcher, target) {
			return "", fmt.Errorf("%s points to the launcher itself", targetFlagName)
		}
		return target, nil
	}

	if environmentTarget = strings.TrimSpace(environmentTarget); environmentTarget != "" {
		target, err := requireExecutableFile(environmentTarget)
		if err != nil {
			return "", fmt.Errorf("%s: %w", targetEnvironmentVariable, err)
		}
		if sameExecutablePath(launcher, target) {
			return "", fmt.Errorf("%s points to the launcher itself", targetEnvironmentVariable)
		}
		return target, nil
	}

	name := aicliExecutableName()
	sibling := filepath.Join(filepath.Dir(launcher), name)
	if target, err := requireExecutableFile(sibling); err == nil {
		if sameExecutablePath(launcher, target) {
			return "", fmt.Errorf("resolved aicli target points to the launcher itself")
		}
		return target, nil
	}

	if lookPath != nil {
		if found, err := lookPath(name); err == nil {
			target, fileErr := requireExecutableFile(found)
			if fileErr == nil {
				if sameExecutablePath(launcher, target) {
					return "", fmt.Errorf("resolved aicli target points to the launcher itself")
				}
				return target, nil
			}
		}
	}

	return "", fmt.Errorf(
		"cannot find %s beside the launcher or on PATH (use %s PATH or set %s)",
		name,
		targetFlagName,
		targetEnvironmentVariable,
	)
}

func requireExecutableFile(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make target path absolute: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("target %q is unavailable: %w", absolute, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("target %q is a directory", absolute)
	}
	return absolute, nil
}

func sameExecutablePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func aicliExecutableName() string {
	if runtime.GOOS == "windows" {
		return "aicli.exe"
	}
	return "aicli"
}
