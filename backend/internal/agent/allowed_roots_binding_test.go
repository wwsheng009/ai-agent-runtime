package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestApprovedToolCallContextBindsAllowedRoots(t *testing.T) {
	agent := &Agent{config: &Config{Options: map[string]interface{}{
		"workspace_path": "/workspace",
		"allowed_roots":  []string{"/ext-a", " /ext-b ", "", "/ext-a"},
	}}}
	ctx := approvedToolCallContext(context.Background(), agent)
	if got := toolctx.WorkspaceRoot(ctx); got != "/workspace" {
		t.Fatalf("WorkspaceRoot = %q", got)
	}
	want := []string{"/ext-a", "/ext-b"}
	if got := toolctx.AllowedRoots(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedRoots = %#v, want %#v", got, want)
	}
}

func TestToolCallContextBindsAllowedRoots(t *testing.T) {
	agent := &Agent{config: &Config{Options: map[string]interface{}{
		"workspace_path":        "/workspace",
		"additionalDirectories": []interface{}{"/ext-c", 42, "/ext-d", " /ext-c "},
	}}}
	ctx := toolCallContext(context.Background(), []types.ToolCall{}, "", nil, agent, "session-1", 0)
	want := []string{"/ext-c", "/ext-d"}
	if got := toolctx.AllowedRoots(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedRoots = %#v, want %#v", got, want)
	}

	// allowed_roots takes precedence over the ACP-style keys.
	preferred := &Agent{config: &Config{Options: map[string]interface{}{
		"allowed_roots":         "/ext-e",
		"additionalDirectories": []string{"/ext-f"},
	}}}
	preferredCtx := approvedToolCallContext(context.Background(), preferred)
	if got := toolctx.AllowedRoots(preferredCtx); !reflect.DeepEqual(got, []string{"/ext-e"}) {
		t.Fatalf("AllowedRoots = %#v, want allowed_roots to win", got)
	}
}

func TestOptionStringList(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]interface{}
		want []string
	}{
		{"empty", nil, nil},
		{"string list", map[string]interface{}{"k": []string{"a", "b"}}, []string{"a", "b"}},
		{"mixed list", map[string]interface{}{"k": []interface{}{"a", 1, "b"}}, []string{"a", "b"}},
		{"separated string", map[string]interface{}{"k": "a, b;c"}, []string{"a", "b", "c"}},
		{"dedupe and trim", map[string]interface{}{"k": []string{" a ", "a", ""}}, []string{"a"}},
		{"wrong type", map[string]interface{}{"k": 7}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := optionStringList(tc.opts, "k"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("optionStringList = %#v, want %#v", got, tc.want)
			}
		})
	}
}
