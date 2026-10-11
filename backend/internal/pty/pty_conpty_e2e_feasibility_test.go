//go:build windows

// ConPTY-backed terminal E2E feasibility probe for the aicli TUI render path.
//
// docs/plan/aicli-terminal-e2e-methodology.md §4 records that an earlier
// chat_tty_conpty attempt was abandoned because CreateProcess with the
// PSEUDOCONSOLE attribute failed with 0xC0000142 STATUS_DLL_INIT_FAILED. The
// root causes were found and fixed in internal/pty (2026-10-11): a UTF-16LE
// environment block is required (nil env => DLL init failure) and the
// attribute payload must be the handle value itself. This test proves the L3
// "process-internal interactive e2e" layer can now run the real chat loop
// behind a true pseudoconsole instead of os.Pipe, gaining real isatty
// semantics (colors/progress/line-editing) in headless CI.
package pty_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
)

// TestConPTYE2EFeasibilityIsattyProgram verifies the exact precondition the
// abandoned L3 ConPTY layer depended on: a console program attached through
// the pseudoconsole observes a real terminal (isatty semantics), and its VT
// output is captured through the PTY pipe.
func TestConPTYE2EFeasibilityIsattyProgram(t *testing.T) {
	if !pty.Supported() {
		t.Skip("ConPTY not supported on this host")
	}
	// Launch PowerShell directly (lpApplicationName) instead of wrapping in
	// cmd /c: quoting the whole script through cmd re-echoes it rather than
	// executing. PowerShell reports [Console]::IsOutputRedirected as false
	// when attached to a pseudoconsole — that is the isatty semantics the
	// render path needs (colors/progress bars without a visible window).
	exe := "powershell"
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		exe = pwsh
	}
	p, err := pty.Start(exe, []string{
		"-NoProfile", "-Command",
		"Write-Output ('REDIR=' + [Console]::IsOutputRedirected); Write-Output 'CONPTY_E2E_OK'; exit 0",
	}, pty.WithSize(120, 40))
	if err != nil {
		t.Fatalf("start conpty: %v", err)
	}
	defer p.Close()

	var buf strings.Builder
	tmp := make([]byte, 16*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, readErr := p.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if readErr != nil || strings.Contains(buf.String(), "CONPTY_E2E_OK") {
				return
			}
		}
	}()
	code, waitErr := p.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("wait: %v", waitErr)
	}
	if code != 0 {
		t.Fatalf("exit=%d output=%q", code, buf.String())
	}
	<-done
	out := buf.String()
	if !strings.Contains(out, "CONPTY_E2E_OK") {
		t.Fatalf("marker missing; output=%q", out)
	}
	if !strings.Contains(out, "REDIR=False") {
		t.Fatalf("pseudoconsole did not provide console semantics (IsOutputRedirected=True); output=%q", out)
	}
	t.Logf("conpty isatty semantics verified")
}
