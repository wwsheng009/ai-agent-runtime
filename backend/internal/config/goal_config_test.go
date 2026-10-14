package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestGoalMaxConsecutiveContinuationsPassthrough pins the 2026-10-14 knob:
// goal.maxConsecutiveContinuations 是自动续跑安全阀；缺省 0 = 不限制（对齐
// Codex 的 continue_if_idle 语义）。见
// docs/plan/aicli-goal-continuation-codex-alignment-plan-20261014.md。
func TestGoalMaxConsecutiveContinuationsPassthrough(t *testing.T) {
	require.Zero(t, DefaultRuntimeConfig().Goal.MaxConsecutiveContinuations,
		"absent config must stay 0 = unlimited (Codex-aligned)")

	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("goal:\n  maxConsecutiveContinuations: 12\n"), cfg))
	require.Equal(t, 12, cfg.Goal.MaxConsecutiveContinuations)

	// Partial block tolerance: an empty goal section must not disturb the default.
	partial := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("goal: {}\n"), partial))
	require.Zero(t, partial.Goal.MaxConsecutiveContinuations)
}
