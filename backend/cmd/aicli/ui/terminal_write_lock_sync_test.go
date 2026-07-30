package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
)

// captureSyncFrameStdout redirects os.Stdout for the duration of fn and returns
// everything written. WithTerminalWriteLock writes its DEC 2026 brackets to the
// live os.Stdout, so the redirect must be in place before fn runs.
func captureSyncFrameStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}

func TestWithTerminalWriteLock_SynchronizedFramesDisabledByDefault(t *testing.T) {
	// The global defaults off; guarantee it regardless of prior tests.
	SetTerminalSynchronizedFrames(false)
	if TerminalSynchronizedFramesEnabled() {
		t.Fatal("synchronized frames should default to disabled")
	}
	out := captureSyncFrameStdout(t, func() {
		WithTerminalWriteLock(func() { fmt.Print("X") })
	})
	if out != "X" {
		t.Fatalf("disabled framing must not add brackets, got %q", out)
	}
}

func TestWithTerminalWriteLock_SynchronizedFramesWrapBatchAtomically(t *testing.T) {
	SetTerminalSynchronizedFrames(true)
	t.Cleanup(func() { SetTerminalSynchronizedFrames(false) })
	if !TerminalSynchronizedFramesEnabled() {
		t.Fatal("expected synchronized frames enabled")
	}
	out := captureSyncFrameStdout(t, func() {
		WithTerminalWriteLock(func() {
			// A multi-step batch: the whole thing must be wrapped once.
			fmt.Print("row-1\n")
			fmt.Print("row-2\n")
		})
	})
	want := synchronizedUpdateBeginSequence + "row-1\nrow-2\n" + synchronizedUpdateEndSequence
	if out != want {
		t.Fatalf("expected batch wrapped in one 2026 frame\n got %q\nwant %q", out, want)
	}
}

func TestWithTerminalWriteLock_SequentialBatchesEachGetOwnFrame(t *testing.T) {
	SetTerminalSynchronizedFrames(true)
	t.Cleanup(func() { SetTerminalSynchronizedFrames(false) })
	out := captureSyncFrameStdout(t, func() {
		WithTerminalWriteLock(func() { fmt.Print("A") })
		WithTerminalWriteLock(func() { fmt.Print("B") })
	})
	want := synchronizedUpdateBeginSequence + "A" + synchronizedUpdateEndSequence +
		synchronizedUpdateBeginSequence + "B" + synchronizedUpdateEndSequence
	if out != want {
		t.Fatalf("expected balanced per-batch frames\n got %q\nwant %q", out, want)
	}
}

func TestSetTerminalSynchronizedFrames_EnvKillSwitch(t *testing.T) {
	t.Setenv("AICLI_DISABLE_SYNC_UPDATE", "1")
	SetTerminalSynchronizedFrames(true)
	t.Cleanup(func() { SetTerminalSynchronizedFrames(false) })
	if TerminalSynchronizedFramesEnabled() {
		t.Fatal("env kill switch must force synchronized frames off")
	}
	out := captureSyncFrameStdout(t, func() {
		WithTerminalWriteLock(func() { fmt.Print("X") })
	})
	if out != "X" {
		t.Fatalf("kill switch must suppress brackets, got %q", out)
	}
}

func TestTerminalDriver_SynchronizedOutputConservativeOffNonTTY(t *testing.T) {
	// A driver over the test process' pipes is not an interactive TTY, so the
	// conservative default must leave synchronized output off.
	d := NewTerminalDriver(os.Stdin, os.Stdout)
	if d.Capabilities().SynchronizedOutput {
		t.Fatal("non-interactive terminal must not advertise SynchronizedOutput")
	}
}
