package commands

import (
	"testing"

	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

// 无假开关断言：空/空白选择不得产生指纹，否则会触发无意义的管理器重建。
func TestMCPSelectionKey_EmptySelectionYieldsEmptyKey(t *testing.T) {
	cases := []runtimeprofileinput.ResolvedMCPSelection{
		{},
		{UseServers: []string{"", "   "}},
		{ExcludeServers: []string{"\t"}},
		{UseServers: []string{"  "}, ExcludeServers: []string{""}},
	}
	for i, selection := range cases {
		if key := mcpSelectionKey(selection); key != "" {
			t.Fatalf("case %d: expected empty key, got %q", i, key)
		}
	}
}

// 等价选择（大小写/空白/顺序差异）必须归一化为同一指纹，避免重复重建。
func TestMCPSelectionKey_NormalizesCaseWhitespaceAndOrder(t *testing.T) {
	left := runtimeprofileinput.ResolvedMCPSelection{UseServers: []string{" GitHub ", "context7"}}
	right := runtimeprofileinput.ResolvedMCPSelection{UseServers: []string{"context7", "github"}}
	if mcpSelectionKey(left) != mcpSelectionKey(right) {
		t.Fatalf("expected normalized keys to match: %q vs %q", mcpSelectionKey(left), mcpSelectionKey(right))
	}
	if key := mcpSelectionKey(left); key != "context7,github|" {
		t.Fatalf("unexpected key %q", key)
	}
}

// 不同选择必须产生不同指纹，否则切换 profile 后管理器不会重建（假开关）。
func TestMCPSelectionKey_DistinguishesDifferentSelections(t *testing.T) {
	base := mcpSelectionKey(runtimeprofileinput.ResolvedMCPSelection{UseServers: []string{"github"}})
	other := mcpSelectionKey(runtimeprofileinput.ResolvedMCPSelection{UseServers: []string{"github", "context7"}})
	exclude := mcpSelectionKey(runtimeprofileinput.ResolvedMCPSelection{ExcludeServers: []string{"github"}})
	if base == other || base == exclude || other == exclude {
		t.Fatalf("selection keys must differ: %q %q %q", base, other, exclude)
	}
}
