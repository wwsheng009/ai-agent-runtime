package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareEventPayloadStampsCompactionIdentity(t *testing.T) {
	input := map[string]interface{}{
		"session_id": "s1",
		"phase":      "pre_turn",
	}

	out := PrepareEventPayload(EventPreCompact, input)
	require.Equal(t, "PreCompact", out["hook_event_name"])
	require.Equal(t, "pre_turn", out["trigger"])
	require.Equal(t, "s1", out["session_id"])

	// The caller's payload is reused for telemetry and must stay untouched.
	_, stamped := input["hook_event_name"]
	require.False(t, stamped)
	_, derived := input["trigger"]
	require.False(t, derived)

	// An explicit trigger always wins over the derived fallback.
	explicit := PrepareEventPayload(EventPostCompact, map[string]interface{}{
		"trigger": "manual",
		"phase":   "pre_turn",
	})
	require.Equal(t, "PostCompact", explicit["hook_event_name"])
	require.Equal(t, "manual", explicit["trigger"])

	// Non-compaction events get the identity stamp only.
	stop := PrepareEventPayload(EventStop, map[string]interface{}{"step": 3})
	require.Equal(t, "Stop", stop["hook_event_name"])
	_, hasTrigger := stop["trigger"]
	require.False(t, hasTrigger)
}

// A matcher on a compaction hook must see the derived trigger; this is what
// makes `matcher: pre_turn` usable at all.
func TestCompactionMatcherUsesPreparedTrigger(t *testing.T) {
	hook := HookConfig{
		ID:      "compact",
		Event:   EventPreCompact,
		Matcher: "pre_turn",
		Exec:    ExecConfig{Type: "shell", Command: "echo ok"},
	}
	payload := PrepareEventPayload(EventPreCompact, map[string]interface{}{"phase": "pre_turn"})
	require.True(t, matchesHook(hook, payload))

	unmatched := PrepareEventPayload(EventPreCompact, map[string]interface{}{"phase": "mid_turn"})
	require.False(t, matchesHook(hook, unmatched))
}
