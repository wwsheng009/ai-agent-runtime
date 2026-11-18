package tools

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestDefaultCommandExecuterUsesContextShellRuntime(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses cmd.exe as the distinct context shell")
	}
	ctx := toolctx.WithShellRuntime(context.Background(), &runtimeexecutor.ShellRuntime{
		Shell: runtimeexecutor.Shell{
			Path:    "cmd.exe",
			Type:    runtimeexecutor.ShellTypeCmd,
			ID:      runtimeexecutor.ShellIDCmd,
			Dialect: runtimeexecutor.ShellDialectCmd,
		},
		Delivery: runtimeexecutor.ScriptDeliveryStdin,
	})
	result, err := (&DefaultCommandExecuter{}).Execute(ctx, "echo context-shell", 10*time.Second)
	require.NoError(t, err)
	require.Equal(t, string(runtimeexecutor.ShellTypeCmd), result.ShellType)
	require.True(t, strings.Contains(strings.ToLower(result.ShellPath), "cmd"), result.ShellPath)
	require.Contains(t, result.Output, "context-shell")
}

func TestContextShellRuntimeIsolatesConcurrentExecutions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses cmd.exe and Windows PowerShell")
	}
	powershellPath, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("Windows PowerShell is not installed")
	}
	cmdRuntime := &runtimeexecutor.ShellRuntime{
		Shell: runtimeexecutor.Shell{
			Path:    "cmd.exe",
			Type:    runtimeexecutor.ShellTypeCmd,
			ID:      runtimeexecutor.ShellIDCmd,
			Dialect: runtimeexecutor.ShellDialectCmd,
		},
		Delivery: runtimeexecutor.ScriptDeliveryStdin,
	}
	psRuntime := &runtimeexecutor.ShellRuntime{
		Shell: runtimeexecutor.Shell{
			Path:    powershellPath,
			Type:    runtimeexecutor.ShellTypePowerShell,
			ID:      runtimeexecutor.ShellIDPowerShell,
			Dialect: runtimeexecutor.ShellDialectPowerShell,
		},
		Delivery: runtimeexecutor.ScriptDeliveryStdin,
	}

	type execution struct {
		runtime *runtimeexecutor.ShellRuntime
		command string
		marker  string
	}
	cases := []execution{
		{runtime: cmdRuntime, command: "echo cmd-marker", marker: "cmd-marker"},
		{runtime: psRuntime, command: "Write-Output ps-marker", marker: "ps-marker"},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for iteration := 0; iteration < 4; iteration++ {
		for _, tc := range cases {
			tc := tc
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx := toolctx.WithShellRuntime(context.Background(), tc.runtime)
				result, err := (&DefaultCommandExecuter{}).Execute(ctx, tc.command, 15*time.Second)
				if err != nil {
					errCh <- err
					return
				}
				if !strings.Contains(result.Output, tc.marker) {
					errCh <- &shellIsolationError{got: result.Output, want: tc.marker}
					return
				}
				if result.ShellType != string(tc.runtime.Shell.Type) {
					errCh <- &shellIsolationError{got: result.ShellType, want: string(tc.runtime.Shell.Type)}
				}
			}()
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

type shellIsolationError struct {
	got  string
	want string
}

func (e *shellIsolationError) Error() string {
	return "shell isolation mismatch: got " + e.got + ", want " + e.want
}
