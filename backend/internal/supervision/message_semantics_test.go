package supervision

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// P0-3a/M5 + P0-3b/M6 开关默认值（docs/plan/
// supervision-parent-child-control-optimization-plan-20260917.md §5/§7.4）。

func TestMessageSemanticsDefaultsKeepV1Behavior(t *testing.T) {
	var cfg Config
	require.False(t, cfg.MessageSemanticsV2Enabled(), "message_semantics_v2 must default to false")
	require.True(t, cfg.TriggerTurnAutoEnabled(), "trigger_turn_auto must default to true")
	require.False(t, cfg.TriggerTurnDrainEnabled(), "the drain requires v2 semantics")
	require.False(t, Config{}.WithDefaults().MessageSemanticsV2Enabled())
	require.True(t, Config{}.WithDefaults().TriggerTurnAutoEnabled())
}

func TestTriggerTurnAutoOnlyAppliesUnderV2(t *testing.T) {
	disabled := false
	require.False(t, (Config{TriggerTurnAuto: &disabled}).TriggerTurnAutoEnabled())
	require.False(t, (Config{TriggerTurnAuto: &disabled}).TriggerTurnDrainEnabled())

	enabled := true
	require.True(t, (Config{MessageSemanticsV2: true, TriggerTurnAuto: &enabled}).TriggerTurnDrainEnabled())
	require.True(t, (Config{MessageSemanticsV2: true, TriggerTurnAuto: &disabled}).MessageSemanticsV2Enabled())
	require.False(t, (Config{MessageSemanticsV2: true, TriggerTurnAuto: &disabled}).TriggerTurnDrainEnabled())
}

func TestWithDefaultsCarriesMessageSemanticsSwitches(t *testing.T) {
	disabled := false
	cfg := Config{MessageSemanticsV2: true, TriggerTurnAuto: &disabled}.WithDefaults()
	require.True(t, cfg.MessageSemanticsV2)
	require.NotNil(t, cfg.TriggerTurnAuto)
	require.False(t, *cfg.TriggerTurnAuto)
}
