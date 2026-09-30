package knowledge

import (
	"context"
	"path/filepath"
	"testing"
)

// TestStartIndexJobIDsAreUnique 钉住同刻并发 StartIndexJob 的主键唯一性回归。
//
// 背景（2026-09-29 live 抽样）：原实现 ID = digest(ws, kind, UnixNano)，Windows
// 时间粒度较粗时两次快速调用拿到同一纳秒 → 第二个写者直接拿到
// UNIQUE constraint failed: index_jobs.id。修复 = 追加进程内原子序号
// （jobs.go indexJobSeq）；本测试并发起 8 个 job，断言全部成功且 id 互异。
func TestStartIndexJobIDsAreUnique(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "knowledge.db"), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: filepath.Join(t.TempDir(), "ws")})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	const runs = 8
	start := make(chan struct{})
	errCh := make(chan error, runs)
	idCh := make(chan string, runs)
	for i := 0; i < runs; i++ {
		go func() {
			<-start
			id, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
			if err != nil {
				errCh <- err
				return
			}
			idCh <- id
		}()
	}
	close(start)

	seen := make(map[string]bool, runs)
	for i := 0; i < runs; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("concurrent StartIndexJob must all succeed: %v", err)
		case id := <-idCh:
			if seen[id] {
				t.Fatalf("duplicate index job id: %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != runs {
		t.Fatalf("unique ids = %d, want %d", len(seen), runs)
	}
}
