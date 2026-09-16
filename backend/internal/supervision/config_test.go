package supervision

import (
	"testing"

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
