package background

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkReadOutputFromLog 覆盖任务输出读取的分配量。
// 优化前按 limit（默认 1MB）全额分配，优化后按文件剩余可读字节定容。
func BenchmarkReadOutputFromLog(b *testing.B) {
	dir := b.TempDir()
	logPath := filepath.Join(dir, "job.log")
	// 典型轮询时点：日志文件只有少量内容。
	if err := os.WriteFile(logPath, []byte("line one\nline two\nline three\n"), 0o644); err != nil {
		b.Fatalf("write log: %v", err)
	}
	m := &Manager{config: DefaultConfig()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.readOutputFromLog(logPath, "job-1", StatusRunning, nil, 0, 0); err != nil {
			b.Fatalf("readOutputFromLog: %v", err)
		}
	}
}

func BenchmarkReadOutputFromLog_Large(b *testing.B) {
	dir := b.TempDir()
	logPath := filepath.Join(dir, "job.log")
	big := make([]byte, 900*1024)
	for i := range big {
		big[i] = byte('a' + (i % 26))
	}
	if err := os.WriteFile(logPath, big, 0o644); err != nil {
		b.Fatalf("write log: %v", err)
	}
	m := &Manager{config: DefaultConfig()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.readOutputFromLog(logPath, "job-2", StatusRunning, nil, 0, 0); err != nil {
			b.Fatalf("readOutputFromLog: %v", err)
		}
	}
}
