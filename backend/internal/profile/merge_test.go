package profile

import (
	"reflect"
	"testing"
)

func TestMergeToolPolicies_MergesSandboxListsAndOverridesScalars(t *testing.T) {
	base := ToolPolicySpec{
		Sandbox: map[string]interface{}{
			"allowedPaths":     []interface{}{"/a", "/b"},
			"deniedCommands":   []string{"rm", "sh"},
			"maxExecutionTime": 10,
			"enabled":          true,
		},
	}
	override := ToolPolicySpec{
		Sandbox: map[string]interface{}{
			"allowedPaths":     []string{"/b", "/c"},
			"deniedCommands":   []interface{}{"sh", "bash"},
			"maxExecutionTime": 20,
			"enabled":          false,
		},
	}

	merged := MergeToolPolicies(base, override)
	if merged.Sandbox == nil {
		t.Fatal("expected sandbox to be merged")
	}

	if got := merged.Sandbox["maxExecutionTime"]; got != 20 {
		t.Fatalf("expected maxExecutionTime=20, got %#v", got)
	}
	if got := merged.Sandbox["enabled"]; got != false {
		t.Fatalf("expected enabled=false, got %#v", got)
	}
	if got := merged.Sandbox["allowedPaths"]; !reflect.DeepEqual(got, []string{"/a", "/b", "/c"}) {
		t.Fatalf("unexpected allowedPaths: %#v", got)
	}
	if got := merged.Sandbox["deniedCommands"]; !reflect.DeepEqual(got, []string{"rm", "sh", "bash"}) {
		t.Fatalf("unexpected deniedCommands: %#v", got)
	}
}
