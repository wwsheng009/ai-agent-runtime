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

func TestStreamCollectorRequiresMatchingFenceMarkerAndLength(t *testing.T) {
	var c StreamCollector
	_ = c.Push("before\n````go\ncode\n")
	if released := c.Push("```\n"); released != "" {
		t.Fatalf("shorter closing fence released mutable code: %q", released)
	}
	if hold := c.Holdback(); !strings.Contains(hold, "````go") || !strings.Contains(hold, "```\n") {
		t.Fatalf("mismatched fence was not retained: %q", hold)
	}
	if released := c.Push("````\n"); !strings.Contains(released, "code") {
		t.Fatalf("matching fence did not release code: %q", released)
	}
	if hold := c.Holdback(); hold != "" {
		t.Fatalf("matching fence left holdback: %q", hold)
	}
}

func TestStreamCollectorDoesNotTreatIndentedFenceAsContainer(t *testing.T) {
	var c StreamCollector
	input := "    ```\nindented code text\n"
	if stable := c.Push(input); stable != input {
		t.Fatalf("four-space indented fence should follow indented-code parsing, stable=%q holdback=%q", stable, c.Holdback())
	}
}

func TestStreamCollectorTracksFenceInsideBlockquote(t *testing.T) {
	var c StreamCollector
	_ = c.Push("before\n> ````go\n> code\n")
	if released := c.Push("> ```\n"); released != "" {
		t.Fatalf("short quoted fence released mutable code: %q", released)
	}
	if hold := c.Holdback(); !strings.Contains(hold, "> ````go") {
		t.Fatalf("quoted open fence missing from holdback: %q", hold)
	}
	if released := c.Push("> ````\n"); !strings.Contains(released, "> code") {
		t.Fatalf("matching quoted fence did not release code: %q", released)
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
	if strings.Contains(c.Stable(), "| A | B |") {
		t.Fatalf("recognized table should remain mutable until a following block: %q", c.Stable())
	}
	all := c.Stable() + c.Holdback()
	if !strings.Contains(all, "| 1 | 2 |") {
		t.Fatalf("raw missing row: stable=%q hold=%q n2=%q", c.Stable(), c.Holdback(), n2)
	}
}

func TestStreamCollectorHoldsBorderlessTableUntilFollowingBlock(t *testing.T) {
	var c StreamCollector
	_ = c.Push("before\n")
	_ = c.Push("Name | Value\n--- | ---\none | 1\ntwo | 2\n")
	if got := c.Stable(); got != "before\n" {
		t.Fatalf("stable=%q, want only the prefix before the mutable table", got)
	}
	if hold := c.Holdback(); !strings.Contains(hold, "Name | Value") || !strings.Contains(hold, "two | 2") {
		t.Fatalf("borderless table missing from holdback: %q", hold)
	}
	_ = c.Push("\nafter\n")
	if hold := c.Holdback(); hold != "" {
		t.Fatalf("blank line should close and release table, holdback=%q", hold)
	}
	if stable := c.Stable(); !strings.Contains(stable, "after\n") {
		t.Fatalf("released stable content missing following block: %q", stable)
	}
}

func TestStreamCollectorHoldsQuotedTable(t *testing.T) {
	var c StreamCollector
	_ = c.Push("before\n")
	_ = c.Push("> Name | Value\n> --- | ---\n> one | 1\n")
	if got := c.Stable(); got != "before\n" {
		t.Fatalf("quoted table leaked into stable prefix: %q", got)
	}
	if hold := c.Holdback(); !strings.Contains(hold, "> Name | Value") || !strings.Contains(hold, "> one | 1") {
		t.Fatalf("quoted table missing from holdback: %q", hold)
	}
}

func TestTableSeparatorRequiresThreeDashesPerColumn(t *testing.T) {
	if isTableSeparator("A | B") || isTableSeparator("-- | ---") || isTableSeparator("--- | :--:") {
		t.Fatal("invalid delimiter row accepted")
	}
	if !isTableSeparator("--- | :---:") {
		t.Fatal("valid borderless delimiter row rejected")
	}
	if isTableRowCandidate(`plain \| escaped`) {
		t.Fatal("escaped pipe should not create a table candidate")
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
