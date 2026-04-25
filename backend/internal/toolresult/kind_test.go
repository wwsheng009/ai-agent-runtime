package toolresult

import "testing"

func TestNormalizeSource_IncludesBroker(t *testing.T) {
	if got := NormalizeSource(" BROKER "); got != SourceBroker {
		t.Fatalf("expected %q, got %q", SourceBroker, got)
	}
}

func TestSourceFromMetadata_PrefersTopLevelAndNested(t *testing.T) {
	topLevel := map[string]interface{}{
		SourceKey: SourceBroker,
		"tool_metadata": map[string]interface{}{
			SourceKey: SourceToolkit,
		},
	}
	if got := SourceFromMetadata(topLevel); got != SourceBroker {
		t.Fatalf("expected top-level %q, got %q", SourceBroker, got)
	}

	nestedOnly := map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			SourceKey: SourceMCP,
		},
	}
	if got := SourceFromMetadata(nestedOnly); got != SourceMCP {
		t.Fatalf("expected nested %q, got %q", SourceMCP, got)
	}
}

func TestWithSource_ClonesAndNormalizes(t *testing.T) {
	original := map[string]interface{}{"existing": true}
	cloned := WithSource(original, " toolkit ")
	if got := cloned[SourceKey]; got != SourceToolkit {
		t.Fatalf("expected %q, got %#v", SourceToolkit, got)
	}
	if _, ok := original[SourceKey]; ok {
		t.Fatalf("expected original metadata to remain unchanged, got %#v", original)
	}
}
