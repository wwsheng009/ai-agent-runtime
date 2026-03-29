package policy

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestFilterToolNames_SortsAndFilters(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"read_file", "git_log"}, false)
	filtered := FilterToolNames([]string{"write_file", "git_log", "read_file"}, policy)
	expected := []string{"git_log", "read_file"}
	if !reflect.DeepEqual(filtered, expected) {
		t.Fatalf("unexpected filtered names: %#v", filtered)
	}
}

func TestFilterToolInfos_UsesGovernanceChecks(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	filtered := FilterToolInfos([]skill.ToolInfo{
		{Name: "read_file", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		{Name: "write_file", MCPTrustLevel: "untrusted_remote", ExecutionMode: "remote_mcp"},
	}, policy)
	if len(filtered) != 1 || filtered[0].Name != "read_file" {
		t.Fatalf("unexpected filtered tool infos: %#v", filtered)
	}
}
