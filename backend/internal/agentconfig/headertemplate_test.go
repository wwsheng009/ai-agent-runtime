package agentconfig

import (
	"reflect"
	"testing"
)

func TestResolveHeaderValueTemplates_ReplacesKnownPlaceholders(t *testing.T) {
	ctx := HeaderTemplateContext{
		SessionID:       "sess_123",
		ParentSessionID: "parent_9",
		UserID:          "user_a",
		ProjectID:       "proj_7",
		Provider:        "opencode.ai",
		Model:           "deepseek-v4-flash",
		Client:          "aicli",
	}
	got := ResolveHeaderValueTemplates(
		"{session_id}|{parent_session_id}|{user_id}|{project_id}|{provider}|{model}|{client}",
		ctx,
	)
	want := "sess_123|parent_9|user_a|proj_7|opencode.ai|deepseek-v4-flash|aicli"
	if got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
}

func TestResolveHeaderValueTemplates_ComposesWithLiteralPrefix(t *testing.T) {
	got := ResolveHeaderValueTemplates("session-{session_id}", HeaderTemplateContext{SessionID: "abc"})
	if got != "session-abc" {
		t.Fatalf("resolved = %q, want %q", got, "session-abc")
	}
}

func TestResolveHeaderValueTemplates_EmptyContextValue(t *testing.T) {
	got := ResolveHeaderValueTemplates("id={session_id}", HeaderTemplateContext{})
	if got != "id=" {
		t.Fatalf("resolved = %q, want %q", got, "id=")
	}
}

func TestResolveHeaderValueTemplates_UnknownPlaceholderPreserved(t *testing.T) {
	ctx := HeaderTemplateContext{SessionID: "abc"}
	got := ResolveHeaderValueTemplates("x={unknown_placeholder} and {session_id}", ctx)
	if got != "x={unknown_placeholder} and abc" {
		t.Fatalf("resolved = %q", got)
	}
}

func TestResolveHeaderValueTemplates_NoBracesUnchanged(t *testing.T) {
	if got := ResolveHeaderValueTemplates("plain value", HeaderTemplateContext{}); got != "plain value" {
		t.Fatalf("resolved = %q", got)
	}
	if got := ResolveHeaderValueTemplates("", HeaderTemplateContext{}); got != "" {
		t.Fatalf("resolved = %q", got)
	}
}

func TestResolveHeaderTemplates_BatchPreservesInput(t *testing.T) {
	input := map[string]string{
		"x-opencode-session": "{session_id}",
		"x-opencode-project": "{project_id}",
		"X-Custom":           "literal",
	}
	resolved := ResolveHeaderTemplates(input, HeaderTemplateContext{
		SessionID: "sess_1",
		ProjectID: "proj_1",
	})
	want := map[string]string{
		"x-opencode-session": "sess_1",
		"x-opencode-project": "proj_1",
		"X-Custom":           "literal",
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved = %+v, want %+v", resolved, want)
	}
	if input["x-opencode-session"] != "{session_id}" {
		t.Fatalf("input map mutated: %+v", input)
	}
}

func TestResolveHeaderTemplates_EmptyInput(t *testing.T) {
	if got := ResolveHeaderTemplates(nil, HeaderTemplateContext{}); got != nil {
		t.Fatalf("nil input = %+v, want nil", got)
	}
	empty := map[string]string{}
	if got := ResolveHeaderTemplates(empty, HeaderTemplateContext{}); len(got) != 0 {
		t.Fatalf("empty input = %+v", got)
	}
}

func TestHeaderTemplatePlaceholderNames_AllSupported(t *testing.T) {
	ctx := HeaderTemplateContext{
		SessionID: "s", ParentSessionID: "p", UserID: "u", ProjectID: "r",
		Provider: "pr", Model: "m", Client: "c",
	}
	for _, placeholder := range HeaderTemplatePlaceholderNames() {
		if got := ResolveHeaderValueTemplates(placeholder, ctx); got == placeholder {
			t.Fatalf("placeholder %s was not resolved", placeholder)
		}
	}
}
