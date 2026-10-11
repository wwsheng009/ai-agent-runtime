package pty

import (
	"bytes"
	"context"
	"os"
	"runtime"
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
		args = []string{"/c", "echo PTY_OK"}
	} else {
		exe = "/bin/sh"
		args = []string{"-c", "echo PTY_OK"}
	}

	p, err := Start(exe, args, WithSize(80, 40))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		tmp := make([]byte, 4096)
		for {
			n, err := p.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := p.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	// Closing the pseudoconsole unblocks the reader once the child has exited.
	_ = p.Close()
	<-done
	require.Contains(t, buf.String(), "PTY_OK")
}
