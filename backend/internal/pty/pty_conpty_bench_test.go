//go:build windows

package pty

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// drainUntil reads until marker appears or the deadline passes.
func drainUntil(t testing.TB, p interface {
	Read([]byte) (int, error)
}, marker string, buf *bytes.Buffer, timeout time.Duration) {
	tmp := make([]byte, 32*1024)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := p.Read(tmp)
		if n > 0 && buf != nil {
			buf.Write(tmp[:n])
		}
		if err != nil {
			return
		}
		if marker == "" || bytes.Contains(buf.Bytes(), []byte(marker)) {
			return
		}
	}
}

// BenchmarkPipeSession measures the plain-pipe path: child echoes 64KB from
// its own generator (no giant command line), parent reads until marker.
func BenchmarkPipeSession(b *testing.B) {
	if runtime.GOOS != "windows" {
		b.Skip("windows comparison")
	}
	comspec := os.Getenv("COMSPEC")
	script := `powershell -NoProfile -Command "$s='x'*65536; Write-Output $s; Write-Output BENCH_DONE"`
	for i := 0; i < b.N; i++ {
		p, err := StartPipeForBench(comspec, []string{"/c", script})
		if err != nil {
			b.Fatalf("start pipe: %v", err)
		}
		var buf bytes.Buffer
		drainUntil(b, p, "BENCH_DONE", &buf, 20*time.Second)
		if !strings.Contains(buf.String(), "BENCH_DONE") {
			b.Fatalf("pipe run did not produce marker (got %d bytes)", buf.Len())
		}
		p.Close()
	}
}

// BenchmarkConPTYSession measures the ConPTY path with the same payload.
func BenchmarkConPTYSession(b *testing.B) {
	if runtime.GOOS != "windows" || !Supported() {
		b.Skip("windows ConPTY required")
	}
	comspec := os.Getenv("COMSPEC")
	script := `powershell -NoProfile -Command "$s='x'*65536; Write-Output $s; Write-Output BENCH_DONE"`
	for i := 0; i < b.N; i++ {
		p, err := Start(comspec, []string{"/c", script}, WithSize(120, 400))
		if err != nil {
			b.Fatalf("start conpty: %v", err)
		}
		var buf bytes.Buffer
		drainUntil(b, p, "BENCH_DONE", &buf, 20*time.Second)
		_, _ = p.Wait(context.Background())
		p.Close()
		if !strings.Contains(buf.String(), "BENCH_DONE") {
			b.Fatalf("conpty run did not produce marker (got %d bytes)", buf.Len())
		}
	}
}
