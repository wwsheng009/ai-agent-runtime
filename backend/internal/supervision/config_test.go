package supervision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestApprovalTerminalGuardDefaultsEnabled pins the gray-release switch
// semantics (docs/plan/supervision-approval-resume-past-deadline-fix-plan.md §8):
// the guard is on unless it is explicitly turned off, so a zero-valued or
// unwired config keeps the fixed behavior.
func TestApprovalTerminalGuardDefaultsEnabled(t *testing.T) {
	var zero Config
	assert.True(t, zero.ApprovalTerminalGuardEnabled(), "zero config must keep the guard on")

	disabled := false
	enabled := true
	assert.False(t, Config{ApprovalTerminalGuard: &disabled}.ApprovalTerminalGuardEnabled())
	assert.True(t, Config{ApprovalTerminalGuard: &enabled}.ApprovalTerminalGuardEnabled())
	assert.True(t, DefaultConfig().ApprovalTerminalGuardEnabled())

	// WithDefaults 必须保留显式值（含显式关闭），不能把关闭吞成“默认开”。
	assert.False(t, Config{ApprovalTerminalGuard: &disabled}.WithDefaults().ApprovalTerminalGuardEnabled())
	assert.True(t, Config{ApprovalTerminalGuard: &enabled}.WithDefaults().ApprovalTerminalGuardEnabled())
}

// TestApprovalTerminalGuardYAMLBinding pins the config key the hosts and the
// plan refer to: supervision.approval_terminal_guard.
func TestApprovalTerminalGuardYAMLBinding(t *testing.T) {
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte("approval_terminal_guard: false\n"), &cfg))
	assert.False(t, cfg.ApprovalTerminalGuardEnabled(), "an explicit false must survive yaml binding")

	var partial Config
	require.NoError(t, yaml.Unmarshal([]byte("execution_deadline: 10m\n"), &partial))
	assert.True(t, partial.ApprovalTerminalGuardEnabled(),
		"an unrelated supervision block must not disable the guard")
}

// AC-P0-4a: every C1-4 config item resolves to its documented default from a
// zero value, so an unwired supervision block keeps the documented behavior
// (Q2–Q5 defaults, I5 bounds, escalate-first on, suspension on).
func TestSuspensionConfig_ZeroValueTakesDefaults(t *testing.T) {
	got := Config{}.WithDefaults()

	cases := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"escalate_first defaults on (Q3/§6.3)", got.EscalateFirstEnabled(), true},
		{"suspension_enabled defaults on (I9/§10)", got.SuspensionAllowed(), true},
		{"stall escalation multiplier (Q3)", got.StallEscalationMultiplier, 2.0},
		{"decision window W = 2x HeartbeatTimeout (Q2)", got.DecisionWindow, 10 * time.Minute},
		{"decision window wall-clock cap = 2W (Q2)", got.DecisionWindowMax, 20 * time.Minute},
		{"extension count bound (I5/Q4)", got.MaxExtensions, 3},
		{"single extension bound (I5/Q4)", got.MaxExtensionPerCall, 1.0},
		{"total extension bound (I5/Q4)", got.MaxExtensionTotal, 4.0},
		{"turn hard cap (Q5/EC-E1)", got.TurnHardCap, 24 * time.Hour},
		{"patrol interval follows heartbeat (#9)", got.PatrolInterval, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.got)
		})
	}

	// DefaultConfig must agree with the resolved zero value (one source of truth).
	d := DefaultConfig()
	assert.True(t, d.EscalateFirstEnabled())
	assert.True(t, d.SuspensionAllowed())
	assert.Equal(t, d.StallEscalationMultiplier, got.StallEscalationMultiplier)
	assert.Equal(t, d.DecisionWindow, got.DecisionWindow)
	assert.Equal(t, d.DecisionWindowMax, got.DecisionWindowMax)
	assert.Equal(t, d.MaxExtensions, got.MaxExtensions)
	assert.Equal(t, d.MaxExtensionPerCall, got.MaxExtensionPerCall)
	assert.Equal(t, d.MaxExtensionTotal, got.MaxExtensionTotal)
	assert.Equal(t, d.TurnHardCap, got.TurnHardCap)
	assert.Equal(t, d.PatrolInterval, got.PatrolInterval)
}

// AC-P0-4b: explicit values win over the defaults, and the derived items follow
// the *effective* heartbeat timeout instead of the built-in one.
func TestSuspensionConfig_ExplicitValuesWin(t *testing.T) {
	cfg := Config{
		HeartbeatTimeout:          2 * time.Minute,
		StallEscalationMultiplier: 3,
		DecisionWindow:            7 * time.Minute,
		DecisionWindowMax:         9 * time.Minute,
		MaxExtensions:             1,
		MaxExtensionPerCall:       0.5,
		MaxExtensionTotal:         2,
		TurnHardCap:               6 * time.Hour,
		PatrolInterval:            30 * time.Second,
	}
	got := cfg.WithDefaults()

	assert.Equal(t, 3.0, got.StallEscalationMultiplier)
	assert.Equal(t, 7*time.Minute, got.DecisionWindow)
	assert.Equal(t, 9*time.Minute, got.DecisionWindowMax)
	assert.Equal(t, 1, got.MaxExtensions)
	assert.Equal(t, 0.5, got.MaxExtensionPerCall)
	assert.Equal(t, 2.0, got.MaxExtensionTotal)
	assert.Equal(t, 6*time.Hour, got.TurnHardCap)
	assert.Equal(t, 30*time.Second, got.PatrolInterval)

	// Derived defaults track the effective heartbeat: W = 2×2m, cap = 2W,
	// patrol = 2m. A hard-coded 10m/20m/5m would silently desync the ladder.
	derived := Config{HeartbeatTimeout: 2 * time.Minute}.WithDefaults()
	assert.Equal(t, 4*time.Minute, derived.DecisionWindow)
	assert.Equal(t, 8*time.Minute, derived.DecisionWindowMax)
	assert.Equal(t, 2*time.Minute, derived.PatrolInterval)
}

// AC-P0-4c (config layer): escalate_first=false / suspension_enabled=false must
// survive WithDefaults so the §10 rollback path stays reachable, including when
// they arrive through the YAML binding used by hosts. The ladder-level behavior
// of the rollback switch is pinned in execution_supervisor_test.go.
func TestSuspensionConfig_RollbackSwitchesSurviveDefaults(t *testing.T) {
	off := false
	on := true

	got := Config{EscalateFirst: &off, SuspensionEnabled: &off}.WithDefaults()
	assert.False(t, got.EscalateFirstEnabled())
	assert.False(t, got.SuspensionAllowed())
	assert.True(t, Config{EscalateFirst: &on, SuspensionEnabled: &on}.WithDefaults().EscalateFirstEnabled())
	assert.True(t, DefaultConfig().EscalateFirstEnabled())
	assert.True(t, DefaultConfig().SuspensionAllowed())

	var yamlCfg Config
	require.NoError(t, yaml.Unmarshal([]byte("escalate_first: false\nsuspension_enabled: false\n"), &yamlCfg))
	assert.False(t, yamlCfg.WithDefaults().EscalateFirstEnabled(), "an explicit false must survive yaml binding")
	assert.False(t, yamlCfg.WithDefaults().SuspensionAllowed(), "an explicit false must survive yaml binding")

	var unrelated Config
	require.NoError(t, yaml.Unmarshal([]byte("execution_deadline: 10m\n"), &unrelated))
	assert.True(t, unrelated.WithDefaults().EscalateFirstEnabled(),
		"an unrelated supervision block must not disable escalate-first")
	assert.True(t, unrelated.WithDefaults().SuspensionAllowed())
}
