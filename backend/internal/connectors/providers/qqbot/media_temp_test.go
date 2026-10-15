package qqbot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMaybeSweepMediaTempDirs 锁定入站媒体临时目录的低频清扫：过期目录删除、
// 新鲜目录保留、其它前缀目录不受影响。
func TestMaybeSweepMediaTempDirs(t *testing.T) {
	provider := New()
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	tempRoot := os.TempDir()
	staleDir, err := os.MkdirTemp(tempRoot, mediaTempDirPrefix)
	if err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staleDir) })
	if err := os.WriteFile(filepath.Join(staleDir, "old.bin"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	staleTime := fixedNow.Add(-mediaTempRetention - time.Hour)
	if err := os.Chtimes(staleDir, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes stale: %v", err)
	}

	freshDir, err := os.MkdirTemp(tempRoot, mediaTempDirPrefix)
	if err != nil {
		t.Fatalf("mkdir fresh: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(freshDir) })
	freshTime := fixedNow.Add(-time.Minute)
	if err := os.Chtimes(freshDir, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}

	otherDir, err := os.MkdirTemp(tempRoot, "qqbot-other-")
	if err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(otherDir) })
	if err := os.Chtimes(otherDir, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes other: %v", err)
	}

	// 计数推进到下一次调用即触发清扫（counter%every == 1）。
	mediaSweepCounter.Store(mediaSweepEvery)
	provider.maybeSweepMediaTempDirs()

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be swept, stat err = %v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh dir should be kept: %v", err)
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Fatalf("non-media dir should be kept: %v", err)
	}
}
