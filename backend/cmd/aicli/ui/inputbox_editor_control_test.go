package ui

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureEditorStdout swaps os.Stdout for a pipe around fn and returns the
// bytes written to the raw stdout path.
func captureEditorStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return string(data)
}

// TestWriteEditorControlSequenceClaimsViaHook：宿主通过
// OnTerminalControl 认领后，模式序列不得再落到 raw stdout。
func TestWriteEditorControlSequenceClaimsViaHook(t *testing.T) {
	const sequence = "\x1b[?2004h\x1b[?1004h"
	var claimed []string
	hooks := LineEditorHooks{OnTerminalControl: func(sequence string) bool {
		claimed = append(claimed, sequence)
		return true
	}}
	raw := captureEditorStdout(t, func() {
		writeEditorControlSequence(hooks, sequence)
	})
	if raw != "" {
		t.Fatalf("claimed control sequence reached raw stdout: %q", raw)
	}
	if len(claimed) != 1 || claimed[0] != sequence {
		t.Fatalf("claimed = %#v want [%q]", claimed, sequence)
	}
}

// TestWriteEditorControlSequenceFallsBackToRawWriter：未设置 OnTerminalControl
// 或宿主不认领时保留 raw 回退（非 unified 编辑器路径的承重行为）。
func TestWriteEditorControlSequenceFallsBackToRawWriter(t *testing.T) {
	const sequence = "\x1b[?2004l"
	for name, hooks := range map[string]LineEditorHooks{
		"no control hook": {},
		"unclaimed":       {OnTerminalControl: func(string) bool { return false }},
	} {
		t.Run(name, func(t *testing.T) {
			raw := captureEditorStdout(t, func() {
				writeEditorControlSequence(hooks, sequence)
			})
			if !strings.Contains(raw, sequence) {
				t.Fatalf("fallback raw stdout = %q, want contains %q", raw, sequence)
			}
		})
	}
}

// newSecretPromptStdin swaps os.Stdin for a pipe carrying payload and returns
// a restore function. The non-interactive path is exercised (stdout is not a
// TTY under capture), so the secret reader stays line-buffered.
func newSecretPromptStdin(t *testing.T, payload string) func() {
	t.Helper()
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = reader
	if _, err := writer.WriteString(payload); err != nil {
		t.Fatalf("write secret stdin: %v", err)
	}
	return func() {
		os.Stdin = oldStdin
		_ = reader.Close()
		_ = writer.Close()
	}
}

// TestReadTransientSecretPromptWithHooksClaimsTerminalText：宿主通过
// OnTerminalText 认领后，secret 提示标签不得再落到 raw stdout。
func TestReadTransientSecretPromptWithHooksClaimsTerminalText(t *testing.T) {
	restore := newSecretPromptStdin(t, "hunter2\n")
	defer restore()

	var claimed []string
	hooks := LineEditorHooks{OnTerminalText: func(text string) bool {
		claimed = append(claimed, text)
		return true
	}}
	var line string
	raw := captureEditorStdout(t, func() {
		var readErr error
		line, readErr = NewInputBox(nil).ReadTransientSecretPromptWithHooks("Password: ", hooks)
		if readErr != nil {
			t.Fatalf("ReadTransientSecretPromptWithHooks: %v", readErr)
		}
	})
	if line != "hunter2" {
		t.Fatalf("secret line = %q, want hunter2", line)
	}
	if raw != "" {
		t.Fatalf("claimed secret prompt reached raw stdout: %q", raw)
	}
	if len(claimed) != 1 || claimed[0] != "Password: " {
		t.Fatalf("claimed = %#v want [Password: ]", claimed)
	}
}

// TestReadTransientSecretPromptWithHooksFallsBackToRawWriter：未设置
// OnTerminalText（或宿主不认领）时保留 raw 回退（非 unified 承重行为）。
func TestReadTransientSecretPromptWithHooksFallsBackToRawWriter(t *testing.T) {
	restore := newSecretPromptStdin(t, "hunter2\n")
	defer restore()

	var line string
	raw := captureEditorStdout(t, func() {
		var readErr error
		line, readErr = NewInputBox(nil).ReadTransientSecretPrompt("Password: ")
		if readErr != nil {
			t.Fatalf("ReadTransientSecretPrompt: %v", readErr)
		}
	})
	if line != "hunter2" {
		t.Fatalf("secret line = %q, want hunter2", line)
	}
	if !strings.Contains(raw, "Password: ") {
		t.Fatalf("fallback raw stdout = %q, want contains Password: ", raw)
	}
}
