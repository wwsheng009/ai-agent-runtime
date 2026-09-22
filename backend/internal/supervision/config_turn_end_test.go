package supervision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestTurnEndCheckDefaultsToManualAudit pins the 2026-09-22 manual audit switch
// (docs/plan/supervision-manual-audit-plan-20260922.md §2): the turn-end
// automatic check (durable wake drain + optional digest-only self-check) is off
// unless explicitly enabled, so a zero-valued or unwired host never starts a
// supervision turn on its own at a business turn boundary.
func TestTurnEndCheckDefaultsToManualAudit(t *testing.T) {
	var zero Config
	assert.False(t, zero.TurnEndCheckEnabled(), "zero config must default to manual audit")

	disabled := false
	enabled := true
	assert.False(t, Config{TurnEndCheck: &disabled}.TurnEndCheckEnabled())
	assert.True(t, Config{TurnEndCheck: &enabled}.TurnEndCheckEnabled())
	assert.False(t, DefaultConfig().TurnEndCheckEnabled())

	// WithDefaults 必须保留显式值（含显式关闭），且不能把 nil 变成默认开：
	// 灰度回退靠显式 true，默认路径永远不启动自动核查。
	assert.False(t, Config{TurnEndCheck: &disabled}.WithDefaults().TurnEndCheckEnabled())
	assert.True(t, Config{TurnEndCheck: &enabled}.WithDefaults().TurnEndCheckEnabled())
	assert.False(t, Config{}.WithDefaults().TurnEndCheckEnabled())
}

// TestTurnEndCheckYAMLBinding pins the config key the hosts and the plan refer
// to: supervision.turn_end_check.
func TestTurnEndCheckYAMLBinding(t *testing.T) {
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte("turn_end_check: true\n"), &cfg))
	assert.True(t, cfg.TurnEndCheckEnabled(), "an explicit true must restore the turn-end closure")

	var off Config
	require.NoError(t, yaml.Unmarshal([]byte("turn_end_check: false\n"), &off))
	assert.False(t, off.TurnEndCheckEnabled())

	var partial Config
	require.NoError(t, yaml.Unmarshal([]byte("execution_deadline: 10m\n"), &partial))
	assert.False(t, partial.TurnEndCheckEnabled(),
		"an unrelated supervision block must not enable the automatic check")
}
