// aicli-console is a small native console launcher for aicli.
//
// From a pipe-backed Windows terminal such as MobaXterm/mintty it creates a
// new conhost window. From cmd, PowerShell, or another real Windows Console it
// keeps the current console. All arguments are forwarded to aicli unchanged.
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

const targetEnvironmentVariable = "AICLI_CONSOLE_TARGET"

func main() {
	os.Exit(runConsoleLauncher(os.Args[1:], os.Stderr))
}

func runConsoleLauncher(args []string, stderr io.Writer) int {
	target, err := resolveAICLIExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "aicli-console: %v\n", err)
		return 1
	}

	exitCode, err := consolehost.RunWithConsole(target, args)
	if err != nil {
		fmt.Fprintf(stderr, "aicli-console: %v\n", err)
		return 1
	}
	return exitCode
}

func resolveAICLIExecutable() (string, error) {
	launcher, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve launcher path: %w", err)
	}
	return resolveAICLIExecutableFrom(
		launcher,
		os.Getenv(targetEnvironmentVariable),
		exec.LookPath,
	)
}

func resolveAICLIExecutableFrom(
	launcher string,
	explicit string,
	lookPath func(string) (string, error),
) (string, error) {
	launcher, err := filepath.Abs(launcher)
	if err != nil {
		return "", fmt.Errorf("make launcher path absolute: %w", err)
	}

	if explicit = strings.TrimSpace(explicit); explicit != "" {
		target, err := requireExecutableFile(explicit)
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
		"cannot find %s beside the launcher or on PATH (set %s to an explicit path)",
		name,
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
