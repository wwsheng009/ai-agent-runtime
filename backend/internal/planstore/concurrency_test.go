package planstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentSnapshotsKeepEveryRound(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	rec, err := store.Record(RecordOptions{ID: "proj/plan", Title: "Plan"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	const workers, perWorker = 8, 3
	const total = workers * perWorker

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[int]string, total)
		errs = make(chan error, total)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				body := fmt.Sprintf("worker-%d-round-%d", w, i)
				got, err := store.Snapshot(SnapshotOptions{
					ID:       rec.ID,
					Decision: "enter",
					Source:   "model",
					Content:  []byte(body),
					Status:   StatusPending,
				})
				if err != nil {
					errs <- fmt.Errorf("worker %d: %w", w, err)
					return
				}
				mu.Lock()
				if prev, dup := seen[got.Version]; dup {
					errs <- fmt.Errorf("version %d returned twice (%q and %q)", got.Version, prev, body)
					mu.Unlock()
					return
				}
				seen[got.Version] = body
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Snapshot: %v", err)
	}

	final, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%+v, %v, %v)", final, ok, err)
	}
	if final.Version != total {
		t.Fatalf("Version = %d, want %d (lost rounds)", final.Version, total)
	}
	if len(final.Rounds) != total {
		t.Fatalf("len(Rounds) = %d, want %d (lost rounds)", len(final.Rounds), total)
	}
	for i, round := range final.Rounds {
		if round.Version != i+1 {
			t.Fatalf("Rounds[%d].Version = %d, want %d", i, round.Version, i+1)
		}
	}

	for version := 1; version <= total; version++ {
		want, ok := seen[version]
		if !ok {
			t.Fatalf("version %d was never returned by any Snapshot", version)
		}
		data, err := store.ReadVersion(rec.ID, version)
		if err != nil {
			t.Fatalf("ReadVersion(%d): %v", version, err)
		}
		if string(data) != want {
			t.Fatalf("version %d content = %q, want %q", version, string(data), want)
		}
		if _, err := os.Stat(filepath.Join(root, "versions", "proj", fmt.Sprintf("plan-v%d.md", version))); err != nil {
			t.Fatalf("version %d file: %v", version, err)
		}
	}
}

// TestConcurrentStoresOnSameRootDoNotLoseUpdates 覆盖跨实例（跨进程同形）的
// 读-改-写竞争：两个 Store 指向同一个 root，模拟同一台机器上多个 aicli 进程
// （TUI / 内嵌 runtime-server / 并发会话）写同一份 index.json 与 comments 日志。
//
// s.mu 是实例级的：没有共享写锁时，两个实例各自读旧 index 再整体覆盖，后写者
// 会丢掉先写者的轮次，评论同样会被覆盖（本用例在接入共享写锁前必须红）。
func TestConcurrentStoresOnSameRootDoNotLoseUpdates(t *testing.T) {
	root := t.TempDir()
	first := NewStore(root)
	second := NewStore(root)

	rec, err := first.Record(RecordOptions{ID: "proj/plan", Title: "Plan"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	const perStore = 4
	var wg sync.WaitGroup
	errs := make(chan error, 2*perStore)
	run := func(store *Store, tag string) {
		defer wg.Done()
		for i := 0; i < perStore; i++ {
			body := fmt.Sprintf("%s-%d", tag, i)
			if _, err := store.Snapshot(SnapshotOptions{
				ID:       rec.ID,
				Decision: "enter",
				Source:   "model",
				Content:  []byte(body),
				Status:   StatusPending,
			}); err != nil {
				errs <- fmt.Errorf("%s snapshot %d: %w", tag, i, err)
				return
			}
			if _, err := store.AppendComment(rec.ID, ReviewComment{
				Revision:  1,
				StartLine: 1,
				EndLine:   1,
				Body:      body,
				Author:    tag,
			}); err != nil {
				errs <- fmt.Errorf("%s comment %d: %w", tag, i, err)
				return
			}
		}
	}
	wg.Add(2)
	go run(first, "a")
	go run(second, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent stores: %v", err)
	}

	final, ok, err := first.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%+v, %v, %v)", final, ok, err)
	}
	if final.Version != 2*perStore {
		t.Fatalf("Version = %d, want %d (两个实例互相覆盖，轮次丢失)", final.Version, 2*perStore)
	}
	if len(final.Rounds) != 2*perStore {
		t.Fatalf("len(Rounds) = %d, want %d", len(final.Rounds), 2*perStore)
	}

	comments, err := second.Comments(rec.ID)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(comments) != 2*perStore {
		t.Fatalf("len(comments) = %d, want %d (两个实例互相覆盖，评论丢失)", len(comments), 2*perStore)
	}
	seen := map[string]bool{}
	for _, comment := range comments {
		seen[comment.Body] = true
	}
	for tag := range map[string]bool{"a": true, "b": true} {
		for i := 0; i < perStore; i++ {
			if !seen[fmt.Sprintf("%s-%d", tag, i)] {
				t.Fatalf("缺少评论 %s-%d（共 %d 条）", tag, i, len(comments))
			}
		}
	}
}
