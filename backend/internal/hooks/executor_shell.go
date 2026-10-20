package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// ShellExecutor executes hook commands via the local shell.
type ShellExecutor struct{}

// Execute runs the hook command and parses a decision from stdout.
func (e *ShellExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	raw, err := e.ExecuteRaw(ctx, hook, payload)
	if err != nil {
		return Decision{}, err
	}
	if raw.ExitCode != 0 {
		return Decision{}, fmt.Errorf("hook command failed with exit code %d", raw.ExitCode)
	}
	return parseDecision([]byte(raw.Stdout))
}

// ExecuteRaw runs the hook command and returns unparsed output.
func (e *ShellExecutor) ExecuteRaw(ctx context.Context, hook HookConfig, payload map[string]interface{}) (RawResult, error) {
	argv, err := shellCommandArgv(hook.Exec)
	if err != nil {
		return RawResult{}, fmt.Errorf("hook command is empty")
	}
	timeout := hook.EffectiveTimeout()
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(argv[0], argv[1:]...)
	guard := runtimeexecutor.NewProcessGuard()
	if bindErr := guard.Bind(cmd); bindErr != nil {
		guard.Close()
		return RawResult{}, fmt.Errorf("hook command setup failed: %w", bindErr)
	}
	input, _ := json.Marshal(payload)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stop := make(chan struct{})
	go func() {
		select {
		case <-cmdCtx.Done():
			guard.Terminate()
		case <-stop:
		}
	}()
	err = cmd.Run()
	close(stop)
	guard.Close()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return RawResult{}, fmt.Errorf("hook command timed out")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return RawResult{
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: exitErr.ExitCode(),
			}, nil
		}
		return RawResult{}, fmt.Errorf("hook command failed: %w", err)
	}
	return RawResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// shellCommandArgv resolves the native Cmd form or the Codex-compatible
// string command form. String commands run through the platform shell so
// pipelines/redirection behave as users expect from hooks.json.
func shellCommandArgv(execConfig ExecConfig) ([]string, error) {
	if len(execConfig.Cmd) > 0 {
		return execConfig.Cmd, nil
	}
	command := strings.TrimSpace(execConfig.Command)
	if runtime.GOOS == "windows" && strings.TrimSpace(execConfig.CommandWindows) != "" {
		command = strings.TrimSpace(execConfig.CommandWindows)
	}
	if command == "" {
		return nil, fmt.Errorf("hook command is empty")
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/C", command}, nil
	}
	return []string{"/bin/sh", "-c", command}, nil
}
