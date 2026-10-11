package agent

import (
	"sync"
	"testing"
)

func TestProcessNodeIDStableWithinProcess(t *testing.T) {
	first := ProcessNodeID()
	if first == "" {
		t.Fatal("ProcessNodeID returned empty")
	}
	for i := 0; i < 5; i++ {
		if got := ProcessNodeID(); got != first {
			t.Fatalf("id changed between calls: %q vs %q", first, got)
		}
	}
	// Concurrent callers must observe the same id.
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i] = ProcessNodeID()
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != first {
			t.Fatalf("concurrent call observed different id: %q vs %q", first, id)
		}
	}
}

func TestGenerateNodeIDShape(t *testing.T) {
	a := generateNodeID()
	b := generateNodeID()
	if a == b {
		t.Fatalf("two generations must differ: %q", a)
	}
	const prefix = "node-"
	if len(a) != len(prefix)+16 { // 8 bytes hex
		t.Fatalf("id %q has unexpected length", a)
	}
}

// The old shared-file behavior is intentionally gone: two processes on the same
// machine must get different ids. This test pins that contract by generating
// two ids and asserting inequality (the sync.Once cache is per-process, so
// generateNodeID is the observable surface).
func TestNodeIDNotSharedAcrossProcesses(t *testing.T) {
	if generateNodeID() == generateNodeID() {
		t.Fatal("ids must not collide across processes")
	}
}
