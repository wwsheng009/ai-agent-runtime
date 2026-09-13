package uniqid

import (
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestTokenStaysUniqueInBurst reproduces the coarse-clock hazard: calls that
// land inside one clock tick used to return the same value, so primary keys and
// idempotency keys collided. The burst is intentionally larger than any
// realistic tick, and the assertion is unconditional uniqueness.
func TestTokenStaysUniqueInBurst(t *testing.T) {
	const calls = 20000
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		token := Token()
		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("Token() returned %q twice after %d calls", token, i)
		}
		seen[token] = struct{}{}
	}
}

// TestNewKeepsPrefixAndOrder checks the contract callers rely on: the caller
// supplied prefix is preserved, and the leading timestamp still orders ids by
// creation time.
func TestNewKeepsPrefixAndOrder(t *testing.T) {
	ids := make([]string, 0, 256)
	for i := 0; i < cap(ids); i++ {
		ids = append(ids, New("wake_"))
	}

	previous := int64(-1)
	for _, id := range ids {
		if !strings.HasPrefix(id, "wake_") {
			t.Fatalf("New() dropped the prefix: %q", id)
		}
		stamp := strings.TrimPrefix(id, "wake_")
		if idx := strings.Index(stamp, "-"); idx >= 0 {
			stamp = stamp[:idx]
		}
		value, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			t.Fatalf("New() timestamp is not parseable in %q: %v", id, err)
		}
		if value < previous {
			t.Fatalf("New() went backwards in time: %d < %d", value, previous)
		}
		previous = value
	}
}

// TestTokenIsUniqueAcrossGoroutines guards the atomic sequence under
// concurrency, which is how the runtime mints most of its ids.
func TestTokenIsUniqueAcrossGoroutines(t *testing.T) {
	const workers = 16
	const perWorker = 500

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]struct{}, workers*perWorker)
	duplicates := make([]string, 0, 4)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				token := Token()
				mu.Lock()
				if _, duplicate := seen[token]; duplicate && len(duplicates) < cap(duplicates) {
					duplicates = append(duplicates, token)
				}
				seen[token] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(duplicates) > 0 {
		t.Fatalf("duplicate tokens minted concurrently: %v", duplicates)
	}
	if got := len(seen); got != workers*perWorker {
		t.Fatalf("expected %d distinct tokens, got %d", workers*perWorker, got)
	}
}

// TestProcessSuffixIsStable pins the property that one process keeps a single
// suffix, which is what makes cross-process ids distinguishable without extra
// lookups.
func TestProcessSuffixIsStable(t *testing.T) {
	if len(process) != 8 {
		t.Fatalf("expected an 8 character process suffix, got %q", process)
	}
	if _, err := hex.DecodeString(process); err != nil {
		t.Fatalf("process suffix %q is not hex: %v", process, err)
	}
	first := tokenSuffix(t, Token())
	second := tokenSuffix(t, Token())
	if first != process || second != process {
		t.Fatalf("tokens must carry the process suffix %q, got %q and %q", process, first, second)
	}
}

func tokenSuffix(t *testing.T, token string) string {
	t.Helper()
	idx := strings.LastIndex(token, "-")
	if idx < 0 {
		t.Fatalf("token %q has no suffix separator", token)
	}
	return token[idx+1:]
}
