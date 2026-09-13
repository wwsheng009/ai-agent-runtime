package subagentbatch

import (
	"strings"
	"testing"
)

// TestDefaultBatchStoreDSNsStayUnique reproduces the coarse-clock hazard for the
// default in-memory control plane: two batch stores created inside one clock
// tick used to resolve to the same cache=shared database, so batches claimed by
// one store were visible to (and could be terminalized by) the other.
func TestDefaultBatchStoreDSNsStayUnique(t *testing.T) {
	const stores = 16

	seen := make(map[string]struct{}, stores)
	for i := 0; i < stores; i++ {
		dsn, path, err := resolveBatchDSN(&StoreConfig{})
		if err != nil {
			t.Fatalf("resolveBatchDSN: %v", err)
		}
		if path != "" {
			t.Fatalf("expected an in-memory DSN, got path %q", path)
		}
		if !strings.Contains(dsn, "mode=memory") {
			t.Fatalf("expected an in-memory DSN, got %q", dsn)
		}
		if _, duplicate := seen[dsn]; duplicate {
			t.Fatalf("batch store DSN %q was handed out twice", dsn)
		}
		seen[dsn] = struct{}{}
	}
}

// TestExplicitBatchMemoryDSNIsShared pins the documented override: callers that
// pass a named in-memory DSN still share one database across stores.
func TestExplicitBatchMemoryDSNIsShared(t *testing.T) {
	const shared = "file:subagentbatch-explicit?mode=memory&cache=shared"

	first, _, err := resolveBatchDSN(&StoreConfig{DSN: shared})
	if err != nil {
		t.Fatalf("resolveBatchDSN: %v", err)
	}
	second, _, err := resolveBatchDSN(&StoreConfig{DSN: shared})
	if err != nil {
		t.Fatalf("resolveBatchDSN: %v", err)
	}
	if first != second {
		t.Fatalf("explicit shared DSN must resolve identically: %q != %q", first, second)
	}
	if !strings.Contains(first, "file:subagentbatch-explicit") {
		t.Fatalf("explicit DSN was rewritten to %q", first)
	}
}
