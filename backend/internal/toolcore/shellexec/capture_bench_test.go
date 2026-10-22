package shellexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	benchShellPayloadBytes = 5 * 1024 * 1024
	benchShellCaptureLimit = 300 * 1024
)

// setupShellCaptureFixture writes a ~5 MiB text file and returns a portable
// command that streams it to stdout (design §15.2.1 shell-capture corpus).
func setupShellCaptureFixture(b *testing.B) (command, root string, totalBytes int) {
	b.Helper()
	root = b.TempDir()
	path := filepath.Join(root, "shell-capture.txt")
	var builder strings.Builder
	line := strings.Repeat("x", 63) + "\n"
	for builder.Len() < benchShellPayloadBytes {
		builder.WriteString(line)
	}
	content := builder.String()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	command = "cat \"" + path + "\""
	if runtime.GOOS == "windows" {
		command = "type \"" + path + "\""
	}
	return command, root, len(content)
}

func assertShellCaptureBounded(b *testing.B, result Result, totalBytes int) {
	b.Helper()
	if result.Capture.RetainedBytes > benchShellCaptureLimit {
		b.Fatalf("retained %d bytes > capture limit %d", result.Capture.RetainedBytes, benchShellCaptureLimit)
	}
	if result.Capture.TotalBytes < totalBytes-4096 {
		b.Fatalf("captured total %d bytes, want ~%d", result.Capture.TotalBytes, totalBytes)
	}
	if !result.Capture.Truncated {
		b.Fatalf("5 MiB output with 300 KiB capture must be marked truncated: %+v", result.Capture)
	}
}

// BenchmarkShellCapture5MBNoArtifact measures process spawn + bounded capture
// without the artifact tee.
func BenchmarkShellCapture5MBNoArtifact(b *testing.B) {
	command, _, totalBytes := setupShellCaptureFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(totalBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Run(context.Background(), Options{
			Command:         command,
			CaptureLimit:    benchShellCaptureLimit,
			DisableArtifact: true,
		})
		if err != nil {
			b.Fatalf("run: %v", err)
		}
		assertShellCaptureBounded(b, result, totalBytes)
	}
}

// BenchmarkShellCapture5MBWithArtifact adds the raw-output tee that the
// artifact_read recovery path depends on.
func BenchmarkShellCapture5MBWithArtifact(b *testing.B) {
	command, root, totalBytes := setupShellCaptureFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(totalBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Run(context.Background(), Options{
			Command:       command,
			CaptureLimit:  benchShellCaptureLimit,
			ArtifactRoot:  root,
			ArtifactScope: "bench",
		})
		if err != nil {
			b.Fatalf("run: %v", err)
		}
		assertShellCaptureBounded(b, result, totalBytes)
		if result.ArtifactPath == "" {
			b.Fatal("artifact tee did not produce a path")
		}
		if result.ArtifactError != nil {
			b.Fatalf("artifact tee failed: %v", result.ArtifactError)
		}
	}
}
