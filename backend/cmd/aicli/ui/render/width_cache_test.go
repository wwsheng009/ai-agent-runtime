package render

import (
	"container/list"
	"strings"
	"sync"
	"testing"

	"github.com/rivo/uniseg"
)

// TestWidthCacheMemoized asserts the cache returns identical results to the
// uncached computation and actually serves repeated lookups.
func TestWidthCacheMemoized(t *testing.T) {
	inputs := []string{"中文测试", "héllo wörld", "a😀b", "mixed 中文 emoji 🎉", "x\twidth", "\u200bzero-width", strings.Repeat("长", 500)}
	want := map[string]int{}
	for _, in := range inputs {
		want[in] = computeWidthUncached(in)
	}
	// First pass: prime the cache (computeWidthUncached stays independent).
	for _, in := range inputs {
		if got := Width(in); got != want[in] {
			t.Fatalf("primed Width(%q)=%d want %d", in, got, want[in])
		}
	}
	// Second pass: served from cache; must stay identical.
	widthMemo.mu.Lock()
	hitsBefore := widthMemo.hits
	widthMemo.mu.Unlock()
	for _, in := range inputs {
		if got := Width(in); got != want[in] {
			t.Fatalf("cached Width(%q)=%d want %d", in, got, want[in])
		}
	}
	widthMemo.mu.Lock()
	defer widthMemo.mu.Unlock()
	if widthMemo.hits == hitsBefore {
		t.Fatal("expected cache hits on second pass")
	}
}

// TestWidthCacheEviction asserts the cache stays bounded and evicts in LRU
// order: the least-recently-used entry goes first, and a re-read rescues an
// entry from eviction.
func TestWidthCacheEviction(t *testing.T) {
	m := newTestWidthCache(8)
	for i := 0; i < 100; i++ {
		text := strings.Repeat("字", i+1)
		m.store(text, i+1)
		if len(m.byKey) > m.max {
			t.Fatalf("cache grew beyond max: %d > %d", len(m.byKey), m.max)
		}
		// The just-inserted entry must be present.
		if w, ok := m.get(text); !ok || w != i+1 {
			t.Fatalf("fresh entry missing: %q", text)
		}
	}
	// LRU order: after the loop the oldest inserts were evicted long ago; the
	// surviving entries are the last `max` inserts, each touched once at its
	// own insert step, so the front is the oldest of them: "字" x (100-max+1).
	frontWant := strings.Repeat("字", 100-m.max+1)
	m.mu.Lock()
	if got, want := m.lru.Len(), m.max; got != want {
		t.Fatalf("lru len = %d, want %d", got, want)
	}
	first := m.lru.Front()
	if first == nil {
		t.Fatal("expected cached entries")
	}
	if got := first.Value.(*widthCacheEntry).key; got != frontWant {
		t.Fatalf("front key = %q, want oldest survivor %q", got, frontWant)
	}
	m.mu.Unlock()
}

// TestWidthCacheEvictionRescuesRecency asserts a re-read moves the entry to
// the back so it survives a later eviction.
func TestWidthCacheEvictionRescuesRecency(t *testing.T) {
	m := newTestWidthCache(4)
	for i := 0; i < 4; i++ {
		m.store(strings.Repeat("a", i+1), i+1) // a, aa, aaa, aaaa
	}
	// Re-read the oldest ("a") to make it most-recent.
	if _, ok := m.get("a"); !ok {
		t.Fatal("expected hit for a")
	}
	// Inserting a new key should evict "aa" (now the oldest), not "a".
	text := "新"
	m.store(text, 1)
	m.mu.Lock()
	if _, ok := m.byKey["a"]; !ok {
		t.Fatalf("recency re-read was not honored: %q evicted", "a")
	}
	if _, ok := m.byKey["aa"]; ok {
		t.Fatal("expected LRU victim aa to be evicted")
	}
	if _, ok := m.byKey[text]; !ok {
		t.Fatalf("fresh entry missing: %q", text)
	}
	m.mu.Unlock()
}

// TestWidthCacheStoreRefresh asserts re-storing an existing key updates the
// width and does not duplicate entries.
func TestWidthCacheStoreRefresh(t *testing.T) {
	m := newTestWidthCache(4)
	m.store("k", 1)
	m.store("k", 3)
	m.store("x", 2)
	m.store("y", 2)
	m.store("z", 2)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lru.Len() != 4 {
		t.Fatalf("lru len = %d, want 4", m.lru.Len())
	}
	el, ok := m.byKey["k"]
	if !ok {
		t.Fatal("expected refreshed key to survive eviction")
	}
	if w := el.Value.(*widthCacheEntry).w; w != 3 {
		t.Fatalf("refreshed width = %d, want 3", w)
	}
}

func newTestWidthCache(max int) *widthCacheMemo {
	return &widthCacheMemo{
		byKey: make(map[string]*list.Element),
		lru:   list.New(),
		max:   max,
	}
}

func TestWidthCacheKeyByteBudget(t *testing.T) {
	m := newTestWidthCache(16)
	m.maxBytes = 6
	m.store("aa", 2)
	m.store("bbb", 3)
	m.store("cccc", 4)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keyBytes > m.maxBytes {
		t.Fatalf("retained key bytes = %d, max = %d", m.keyBytes, m.maxBytes)
	}
	if len(m.byKey) != 1 {
		t.Fatalf("entries = %d, want only the newest entry after byte eviction", len(m.byKey))
	}
	if _, ok := m.byKey["cccc"]; !ok {
		t.Fatal("newest entry was not retained")
	}
	if m.evictions != 2 {
		t.Fatalf("evictions = %d, want 2", m.evictions)
	}
}

func TestWidthCacheSkipsOversizeKey(t *testing.T) {
	m := newTestWidthCache(4)
	m.maxBytes = 64
	m.maxKeyBytes = 8
	m.store("012345678", 9)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.byKey) != 0 || m.keyBytes != 0 {
		t.Fatalf("oversize key was retained: entries=%d bytes=%d", len(m.byKey), m.keyBytes)
	}
	if m.skipped != 1 {
		t.Fatalf("skipped = %d, want 1", m.skipped)
	}
}

func TestWidthCacheMissMetricCountsLookups(t *testing.T) {
	m := newTestWidthCache(2)
	if _, ok := m.get("missing"); ok {
		t.Fatal("unexpected cache hit")
	}
	m.store("missing", 7)
	if got, ok := m.get("missing"); !ok || got != 7 {
		t.Fatalf("cached lookup = (%d, %t), want (7, true)", got, ok)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.miss != 1 || m.hits != 1 {
		t.Fatalf("metrics hits=%d misses=%d, want 1/1", m.hits, m.miss)
	}
}

// TestWidthCacheConcurrent fills and reads the cache from many goroutines to
// exercise the mutex (run with -race).
func TestWidthCacheConcurrent(t *testing.T) {
	memo := newTestWidthCache(64)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				text := strings.Repeat("并", i%30+1)
				memo.store(text, i)
				if _, ok := memo.get(text); !ok {
					t.Errorf("missing %q", text)
				}
			}
		}()
	}
	wg.Wait()
}

// TestFastASCIIWidth validates the shortcut against the reference.
func TestFastASCIIWidth(t *testing.T) {
	for _, in := range []string{"abc", "hello world", "12345", " ", "~!@#$%^&*()_+", "\tab", "caf\xc3\xa9", "中文", "\x7f", "\x01x", ""} {
		w, ok := fastASCIIWidth(in)
		if ok {
			if w != computeWidthUncached(in) {
				t.Fatalf("fastASCIIWidth(%q)=%d want %d", in, w, computeWidthUncached(in))
			}
			if !isPrintableASCII(in) {
				t.Fatalf("fast path claimed non-printable ASCII %q", in)
			}
		} else if isPrintableASCII(in) && in != "" {
			t.Fatalf("fast path rejected printable ASCII %q", in)
		}
	}
}

// computeWidthUncached mirrors Width's original computation directly against
// uniseg, so the cached path can be verified against an independent reference.
func computeWidthUncached(text string) int {
	if text == "" {
		return 0
	}
	total := 0
	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		total += graphemeWidth(gr.Runes())
	}
	return total
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b >= 0x7f {
			return false
		}
	}
	return true
}
