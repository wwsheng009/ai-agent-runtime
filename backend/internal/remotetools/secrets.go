package remotetools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// remoteToolsHomeDir is os.UserHomeDir behind a seam so tests can point the
// default token path at a temporary directory.
var remoteToolsHomeDir = os.UserHomeDir

// DefaultRemoteTokenPath returns ~/.aicli/remote-tools/mcp.token, the path
// `aicli-remote-tools init` writes and `serve` picks up automatically.
func DefaultRemoteTokenPath() (string, error) {
	home, err := remoteToolsHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home directory for the remote-tools token: %w", err)
	}
	return filepath.Join(home, ".aicli", "remote-tools", "mcp.token"), nil
}

// ResolveSecret returns a credential from a file (preferred) or an environment
// variable.
//
// Configuration keeps only the *name* of an environment variable or the path of
// a file; the secret itself never belongs in a world-readable config. A file is
// the recommended shape for long-lived daemons (systemd EnvironmentFile has the
// same effect, but a file is not inherited by child processes).
//
// Fail-closed rules:
//   - a named file that is missing, empty or (on Unix) readable by group/other
//     is a startup error, never a silent downgrade to "no token";
//   - a named environment variable that is unset yields "" so callers can decide
//     whether an unauthenticated loopback service is acceptable.
func ResolveSecret(kind, envName, filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath != "" {
		secret, err := readSecretFile(filePath)
		if err != nil {
			return "", fmt.Errorf("%s: %w", kind, err)
		}
		return secret, nil
	}
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", nil
	}
	return strings.TrimSpace(os.Getenv(envName)), nil
}

// readSecretFile reads a 0600-style secret file and trims trailing whitespace
// (editors and `echo` routinely add a newline).
func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("secret path %s is a directory", path)
	}
	// Windows permission bits are synthetic; only enforce the Unix rule where it
	// actually protects the file.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return "", fmt.Errorf("secret file %s is too permissive (%04o); chmod 600 it", path, mode)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return secret, nil
}
