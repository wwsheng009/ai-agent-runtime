package hooks

import (
	"testing"
)

// MatchingHooks is the diagnostic counterpart of Dispatch. It must apply the
// same filtering (event, enabled, matcher) or `aicli hooks test` reports hooks
// as matched that would never run.
func TestMatchingHooksAppliesEventEnabledAndMatcher(t *testing.T) {
	disabled := false
	manager := NewManager([]HookConfig{
		{ID: "match", Event: EventPreToolUse, Matcher: "shell", Exec: ExecConfig{Type: "shell", Command: "echo ok"}},
		{ID: "other-tool", Event: EventPreToolUse, Matcher: "Write", Exec: ExecConfig{Type: "shell", Command: "echo ok"}},
		{ID: "other-event", Event: EventPostToolUse, Matcher: "shell", Exec: ExecConfig{Type: "shell", Command: "echo ok"}},
		{ID: "disabled", Event: EventPreToolUse, Matcher: "shell", Enabled: &disabled, Exec: ExecConfig{Type: "shell", Command: "echo ok"}},
	})

	matched := manager.MatchingHooks(EventPreToolUse, map[string]interface{}{"tool_name": "shell"})
	if len(matched) != 1 || matched[0].ID != "match" {
		t.Fatalf("matched = %+v, want exactly [match]", matched)
	}
}

func TestMatchingHooksIsEmptyWithoutPayloadMatch(t *testing.T) {
	manager := NewManager([]HookConfig{
		{ID: "only-write", Event: EventPreToolUse, Matcher: "Write", Exec: ExecConfig{Type: "shell", Command: "echo ok"}},
	})
	if matched := manager.MatchingHooks(EventPreToolUse, map[string]interface{}{"tool_name": "shell"}); len(matched) != 0 {
		t.Fatalf("matched = %+v, want none", matched)
	}
}
