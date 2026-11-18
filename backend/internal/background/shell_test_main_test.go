package background

import (
	"os"
	"runtime"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// backgroundTestShellConfig pins the shell used by this package's tests.
//
// Several tests mix PowerShell and POSIX command strings. The process-wide
// resolver is now captured at job submission, so a previous test leaving the
// auto-detection chain installed (Git Bash on a developer machine) would change
// those commands' dialect mid-suite. Pin one real shell for reproducible tests.
func backgroundTestShellConfig() runtimeexecutor.ShellConfig {
	if runtime.GOOS == "windows" {
		return runtimeexecutor.ShellConfig{Prefer: []string{runtimeexecutor.ShellIDPowerShell}}
	}
	return runtimeexecutor.ShellConfig{Prefer: []string{runtimeexecutor.ShellIDSh}}
}

func TestMain(m *testing.M) {
	runtimeexecutor.ConfigureShell(backgroundTestShellConfig())
	runtimeexecutor.InvalidateShellResolution()
	code := m.Run()
	runtimeexecutor.ConfigureShell(runtimeexecutor.ShellConfig{})
	runtimeexecutor.InvalidateShellResolution()
	os.Exit(code)
}
