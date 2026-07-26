package toolprotocol

import "testing"

func TestNormalizeToolID(t *testing.T) {
	if got := NormalizeToolID("  Shell "); got != "shell" {
		t.Fatalf("NormalizeToolID = %q, want shell", got)
	}
	if !ToolID("").IsEmpty() {
		t.Fatal("empty ToolID should be empty")
	}
	if NormalizeCallID("  call-1  ") != "call-1" {
		t.Fatal("NormalizeCallID trim failed")
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := map[string]Kind{
		"read":    KindRead,
		"SEARCH":  KindSearch,
		"edit":    KindEdit,
		"exec":    KindExec,
		"network": KindNetwork,
		"control": KindControl,
		"nope":    "",
	}
	for input, want := range cases {
		if got := NormalizeKind(input); got != want {
			t.Fatalf("NormalizeKind(%q)=%q want %q", input, got, want)
		}
	}
}

func TestFromDefinitionMetadata(t *testing.T) {
	caps, ok := FromDefinitionMetadata(map[string]interface{}{
		"tool_kind":    "edit",
		"read_only":    false,
		"mutates_fs":   true,
		"requires_net": false,
	})
	if !ok {
		t.Fatal("expected metadata capabilities")
	}
	if caps.Kind != KindEdit || !caps.MutatesFS || caps.ReadOnly {
		t.Fatalf("unexpected caps: %+v", caps)
	}
	if _, ok := FromDefinitionMetadata(nil); ok {
		t.Fatal("nil metadata should not be ok")
	}
}
