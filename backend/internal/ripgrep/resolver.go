package ripgrep

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const EnvironmentPath = "AICLI_RG_PATH"

const (
	SourceEnvironment = "environment"
	SourceBundled     = "bundled"
	SourceAdjacent    = "adjacent"
	SourcePath        = "path"
	SourceCustom      = "custom"
)

// Resolution identifies the executable selected for toolkit grep/glob.
type Resolution struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

type resolverDeps struct {
	getenv     func(string) string
	executable func() (string, error)
	lookPath   func(string) (string, error)
	stat       func(string) (os.FileInfo, error)
}

// Resolve prefers an explicit override, then a release-bundled executable,
// and finally the host PATH. This keeps every runtime search surface aligned.
func Resolve() (Resolution, error) {
	return resolveWith(resolverDeps{
		getenv:     os.Getenv,
		executable: os.Executable,
		lookPath:   exec.LookPath,
		stat:       os.Stat,
	})
}

// LookPath is compatible with exec.LookPath and gives rg the shared resolver.
func LookPath(name string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized != "rg" && normalized != "rg.exe" && normalized != "ripgrep" {
		return exec.LookPath(name)
	}
	resolution, err := Resolve()
	if err != nil {
		return "", err
	}
	return resolution.Path, nil
}

// SourceForPath classifies a resolved path without changing execution policy.
func SourceForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	resolution, err := Resolve()
	if err == nil && samePath(resolution.Path, path) {
		return resolution.Source
	}
	return SourceCustom
}

func resolveWith(deps resolverDeps) (Resolution, error) {
	if deps.getenv == nil {
		deps.getenv = func(string) string { return "" }
	}
	if deps.executable == nil {
		deps.executable = func() (string, error) { return "", fmt.Errorf("executable path unavailable") }
	}
	if deps.lookPath == nil {
		deps.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	}
	if deps.stat == nil {
		deps.stat = os.Stat
	}

	if explicit := strings.TrimSpace(deps.getenv(EnvironmentPath)); explicit != "" {
		path, err := validateExecutable(explicit, deps.stat)
		if err != nil {
			return Resolution{}, fmt.Errorf("%s points to an unusable ripgrep executable: %w", EnvironmentPath, err)
		}
		return Resolution{Path: path, Source: SourceEnvironment}, nil
	}

	if executablePath, err := deps.executable(); err == nil && strings.TrimSpace(executablePath) != "" {
		dir := filepath.Dir(executablePath)
		for _, candidate := range []Resolution{
			{Path: filepath.Join(dir, "codex-path", executableName()), Source: SourceBundled},
			{Path: filepath.Join(dir, executableName()), Source: SourceAdjacent},
			{Path: filepath.Join(dir, "resources", executableName()), Source: SourceBundled},
		} {
			if path, err := validateExecutable(candidate.Path, deps.stat); err == nil {
				candidate.Path = path
				return candidate, nil
			}
		}
	}

	path, err := deps.lookPath("rg")
	if err != nil || strings.TrimSpace(path) == "" {
		return Resolution{}, fmt.Errorf("ripgrep/rg was not found via %s, bundled codex-path, or PATH", EnvironmentPath)
	}
	path, err = validateExecutable(path, deps.stat)
	if err != nil {
		return Resolution{}, fmt.Errorf("PATH resolved an unusable ripgrep executable: %w", err)
	}
	return Resolution{Path: path, Source: SourcePath}, nil
}

func validateExecutable(path string, stat func(string) (os.FileInfo, error)) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", abs)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", abs)
	}
	return abs, nil
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "rg.exe"
	}
	return "rg"
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}
