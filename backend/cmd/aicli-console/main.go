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

// version 通过构建参数 -X main.version=<v> 注入（例如 Win7 构建脚本）。
var version = "0.1.0"

func main() {
	os.Exit(runConsoleLauncher(os.Args[1:], os.Stdout, os.Stderr))
}

func runConsoleLauncher(args []string, stdout, stderr io.Writer) int {
	// -h / --help / help（"--" 之前的）只打印帮助，不启动 aicli。
	if wantsConsoleLauncherHelp(args) {
		printConsoleLauncherUsage(stdout)
		return 0
	}
	if wantsConsoleLauncherVersion(args) {
		fmt.Fprintf(stdout, "aicli-console version %s\n", version)
		return 0
	}

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

// wantsConsoleLauncherHelp 判断 "--" 之前是否出现帮助请求参数。
// "--" 之后的参数属于 aicli，不会被当成帮助请求。
func wantsConsoleLauncherHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" || arg == "help" {
			return true
		}
	}
	return false
}

// wantsConsoleLauncherVersion 判断 "--" 之前是否出现版本请求参数。
func wantsConsoleLauncherVersion(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-V" || arg == "--version" {
			return true
		}
	}
	return false
}

// printConsoleLauncherUsage 打印启动器的多行帮助。
func printConsoleLauncherUsage(out io.Writer) {
	fmt.Fprint(out, `aicli-console - native console launcher for aicli

Description:
  Starts aicli inside a real Windows Console. From a pipe-backed terminal such
  as MobaXterm/mintty it creates a new conhost window; from cmd, PowerShell or
  another real Windows Console it keeps the current console.
  Launcher-only arguments are consumed here; everything else is forwarded to
  aicli unchanged.

Usage:
  aicli-console [--target PATH] [-- aicli args...]
  aicli-console -h | --help | help

Options:
  --target PATH        aicli executable to launch (overrides AICLI_CONSOLE_TARGET
                       and the aicli.exe found beside this launcher)
  -h, --help, help     show this help and exit
  -V, --version        show the launcher version and exit
  --                   everything after it is forwarded to aicli verbatim

Environment:
  AICLI_CONSOLE_TARGET   default aicli executable when --target is not given

Target resolution order:
  1. --target PATH
  2. $AICLI_CONSOLE_TARGET
  3. aicli.exe beside this launcher
  4. aicli.exe found on PATH

Examples:
  # Launch the interactive chat in a real console
  aicli-console

  # Launch a specific aicli build
  aicli-console --target C:\Tools\aicli-win7.exe

  # Forward arguments to aicli
  aicli-console chat --compat-mode

  # Forward a literal --target to aicli (anything after -- is untouched)
  aicli-console -- chat --target model-target

Exit codes:
  0   aicli exited successfully (also used for --help)
  1   launcher failed: aicli not found or could not be started
  2   invalid launcher arguments (e.g. --target without a path)
  other   the exit code returned by aicli itself
`)
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
