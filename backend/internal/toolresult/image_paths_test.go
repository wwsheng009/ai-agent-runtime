package toolresult

import (
	"reflect"
	"testing"
)

// TestImagePassthroughPathsRequireTheFlag: the additive image_paths list is part
// of the same contract as image_path — a result that does not declare
// passthrough must not have its list attached.
func TestImagePassthroughPathsRequireTheFlag(t *testing.T) {
	withFlag := map[string]interface{}{
		MetadataImagePassthroughKey: true,
		MetadataImagePathsKey:       []string{"/tmp/a.png", "/tmp/b.png", "/tmp/a.png"},
	}
	if got := ImagePassthroughPathsFromMetadata(withFlag); !reflect.DeepEqual(got, []string{"/tmp/a.png", "/tmp/b.png"}) {
		t.Fatalf("expected de-duplicated declared order, got %#v", got)
	}

	withoutFlag := map[string]interface{}{
		MetadataImagePathsKey: []interface{}{"/tmp/c.png"},
	}
	if got := ImagePassthroughPathsFromMetadata(withoutFlag); len(got) != 0 {
		t.Fatalf("paths without the passthrough flag must be ignored, got %#v", got)
	}

	nested := map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			MetadataImagePassthroughKey: true,
			MetadataImagePathsKey:       []interface{}{" /tmp/d.png ", ""},
		},
	}
	if got := ImagePassthroughPathsFromMetadata(nested); !reflect.DeepEqual(got, []string{"/tmp/d.png"}) {
		t.Fatalf("expected the nested scope to be honored and trimmed, got %#v", got)
	}
}
