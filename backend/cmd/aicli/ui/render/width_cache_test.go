package render

import (
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

// TestWidthCacheEviction asserts the cache stays bounded.
func TestWidthCacheEviction(t *testing.T) {
	m := &widthCacheMemo{byKey: make(map[string]widthCacheEntry), max: 8}
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
}

// TestWidthCacheConcurrent fills and reads the cache from many goroutines to
// exercise the mutex (run with -race).
func TestWidthCacheConcurrent(t *testing.T) {
	memo := &widthCacheMemo{byKey: make(map[string]widthCacheEntry), max: 64}
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
