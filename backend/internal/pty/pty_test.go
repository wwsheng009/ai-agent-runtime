package pty

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPTYRoundTrip(t *testing.T) {
	if !Supported() {
		t.Skip("pty not supported on this platform")
	}
	var (
		exe  string
		args []string
	)
	if runtime.GOOS == "windows" {
		exe = os.Getenv("COMSPEC")
		if exe == "" {
			exe = "cmd.exe"
		}
		args = []string{"/c", "echo", "PTY_OK"}
	} else {
		exe = "/bin/sh"
		args = []string{"-c", "echo PTY_OK"}
	}

	p, err := Start(exe, args, WithSize(80, 40))
	require.NoError(t, err)

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		tmp := make([]byte, 4096)
		for {
			n, readErr := p.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, waitErr := p.Wait(ctx)
	require.NoError(t, waitErr)
	require.Equal(t, 0, code, "cmd output so far: %q", buf.String())

	// Drain the tail: conhost keeps the output pipe open briefly after the
	// child exits; ClosePseudoConsole unblocks the reader.
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "PTY_OK") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = p.Close()
	<-done
	require.Contains(t, buf.String(), "PTY_OK")
}
