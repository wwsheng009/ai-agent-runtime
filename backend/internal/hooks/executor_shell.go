package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ShellExecutor executes hook commands via the local shell.
type ShellExecutor struct{}

// Execute runs the hook command and parses a decision from stdout.
func (e *ShellExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	if len(hook.Exec.Cmd) == 0 {
		return Decision{}, fmt.Errorf("hook command is empty")
	}
	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, hook.Exec.Cmd[0], hook.Exec.Cmd[1:]...)
	input, _ := json.Marshal(payload)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return Decision{}, fmt.Errorf("hook command timed out")
		}
		return Decision{}, fmt.Errorf("hook command failed: %w", err)
	}
	return parseDecision(output)
}
