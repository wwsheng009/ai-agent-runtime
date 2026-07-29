package markdown

import (
	"strings"
	"testing"
)

func TestStreamCollectorHoldsOpenFence(t *testing.T) {
	var c StreamCollector
	n1 := c.Push("intro\n")
	if n1 != "intro\n" {
		t.Fatalf("n1=%q", n1)
	}
	n2 := c.Push("```go\nfunc() {\n")
	if n2 != "" {
		t.Fatalf("should hold open fence, got %q stable=%q", n2, c.Stable())
	}
	if !strings.Contains(c.Holdback(), "```") {
		t.Fatalf("holdback=%q", c.Holdback())
	}
	n3 := c.Push("}\n```\n")
	if !strings.Contains(n3, "```") {
		t.Fatalf("closing fence should release, got %q", n3)
	}
	if c.Holdback() != "" {
		t.Fatalf("holdback after close: %q", c.Holdback())
	}
}

func TestStreamCollectorHoldsPartialTable(t *testing.T) {
	var c StreamCollector
	_ = c.Push("before\n")
	n := c.Push("| A | B |\n| --- | --- |\n| 1 |")
	if strings.Contains(c.Stable(), "| 1 |") {
		t.Fatalf("partial row leaked into stable: %q", c.Stable())
	}
	if n != "" && strings.Contains(n, "| 1 |") {
		t.Fatalf("newly stable partial: %q", n)
	}
	n2 := c.Push(" 2 |\n")
	all := c.Stable() + c.Holdback()
	if !strings.Contains(all, "| 1 | 2 |") {
		t.Fatalf("raw missing row: stable=%q hold=%q n2=%q", c.Stable(), c.Holdback(), n2)
	}
}

func TestStreamCollectorFinalizeReleasesAll(t *testing.T) {
	var c StreamCollector
	_ = c.Push("#")
	if c.Stable() != "" {
		t.Fatalf("lead should hold: %q", c.Stable())
	}
	rest := c.Finalize()
	if rest != "#" {
		t.Fatalf("finalize=%q", rest)
	}
}

func TestStreamCollectorNoDuplicateStable(t *testing.T) {
	var c StreamCollector
	a := c.Push("Hello world. This is enough plain text to stream.\n")
	b := c.Push("Second line.\n")
	if a == "" || b == "" {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if strings.Contains(b, "Hello") {
		t.Fatalf("duplicate stable emission: %q", b)
	}
}
