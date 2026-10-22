package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

const benchFileBytes = 1024

// Large benchmarks create 100k files (~100MB) and are opt-in so normal test
// and benchmark runs stay cheap. Set AICLI_TOOLCORE_BENCH_LARGE=1 to run them.
const largeBenchEnv = "AICLI_TOOLCORE_BENCH_LARGE"

func requireLargeBench(b *testing.B) {
	b.Helper()
	if os.Getenv(largeBenchEnv) == "" {
		b.Skipf("set %s=1 to run 100k-file benchmarks", largeBenchEnv)
	}
}

// heapPeakSampler samples the live heap while a search runs. B/op is a
// cumulative allocation counter, not a peak; §15.2.2 asks specifically whether
// peak memory stays bounded as the file count grows, so the benchmark reports
// this separately.
type heapPeakSampler struct {
	samples []metrics.Sample
	stop    chan struct{}
	done    chan struct{}
	peak    uint64
}

func startHeapPeakSampler() *heapPeakSampler {
	sampler := &heapPeakSampler{
		samples: []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}},
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			metrics.Read(sampler.samples)
			if value := sampler.samples[0].Value.Uint64(); value > sampler.peak {
				sampler.peak = value
			}
			select {
			case <-sampler.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return sampler
}

func (s *heapPeakSampler) stopAndPeak() uint64 {
	close(s.stop)
	<-s.done
	return s.peak
}

func benchmarkTree(b *testing.B, files int) string {
	b.Helper()
	root := b.TempDir()
	content := []byte(strings.Repeat("x", benchFileBytes-1) + "\n")
	for i := 0; i < 100; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", i%100))
		path := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	// Warm the OS file cache and any on-access scanner outside the measured
	// window. Otherwise first-touch open latency dominates on Windows.
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		_ = data
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkGlob10kNoMatch(b *testing.B) {
	root := benchmarkTree(b, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Glob(context.Background(), root, "**/*.go", 500)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
}

func BenchmarkGrep10kNoMatch(b *testing.B) {
	root := benchmarkTree(b, 10_000)
	b.ReportAllocs()
	b.SetBytes(10_000 * benchFileBytes)
	sampler := startHeapPeakSampler()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern:      "no-such-needle",
			Path:         root,
			Glob:         "*.txt",
			Limit:        200,
			MaxFileBytes: 1 << 20,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(sampler.stopAndPeak()), "peak_heap_B")
}

func BenchmarkGlob100kNoMatch(b *testing.B) {
	requireLargeBench(b)
	root := benchmarkTree(b, 100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Glob(context.Background(), root, "**/*.go", 500)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
}

func BenchmarkGrep100kNoMatch(b *testing.B) {
	requireLargeBench(b)
	root := benchmarkTree(b, 100_000)
	b.ReportAllocs()
	b.SetBytes(100_000 * benchFileBytes)
	sampler := startHeapPeakSampler()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern:      "no-such-needle",
			Path:         root,
			Glob:         "*.txt",
			Limit:        200,
			MaxFileBytes: 1 << 20,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(sampler.stopAndPeak()), "peak_heap_B")
}
