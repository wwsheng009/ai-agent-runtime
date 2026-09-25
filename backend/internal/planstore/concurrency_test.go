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
