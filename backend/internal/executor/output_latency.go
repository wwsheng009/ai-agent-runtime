package executor

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	lowLatencyPythonUnbuffered = "PYTHONUNBUFFERED"
)

// PrepareCommandForLowLatencyOutput reduces avoidable buffering for interactive
// command output. It is intended for live mirrored command execution only.
func PrepareCommandForLowLatencyOutput(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Env = withEnvOverride(cmd.Env, lowLatencyPythonUnbuffered, "1")
	if runtime.GOOS == "windows" || len(cmd.Args) == 0 {
		return
	}
	stdbufPath, err := exec.LookPath("stdbuf")
	if err != nil || strings.TrimSpace(stdbufPath) == "" {
		return
	}
	originalArgs := append([]string(nil), cmd.Args...)
	cmd.Path = stdbufPath
	cmd.Args = append([]string{stdbufPath, "-oL", "-eL"}, originalArgs...)
}

func withEnvOverride(env []string, key string, value string) []string {
	if strings.TrimSpace(key) == "" {
		return env
	}
	if env == nil {
		env = os.Environ()
	} else {
		env = append([]string(nil), env...)
	}
	prefix := key + "="
	for index, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if strings.EqualFold(name, key) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
