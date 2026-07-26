package planmode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

type memoryContext struct {
	values map[string]interface{}
}

func (m *memoryContext) GetContext(key string) (interface{}, bool) {
	if m == nil || m.values == nil {
		return nil, false
	}
	value, ok := m.values[key]
	return value, ok
}

func (m *memoryContext) SetContext(key string, value interface{}) {
	if m.values == nil {
		m.values = map[string]interface{}{}
	}
	m.values[key] = value
}

func TestEnterExitRoundTrip(t *testing.T) {
	t.Parallel()

	store := &memoryContext{}
	entered := Enter(string(runtimepolicy.ModeDefault), "docs/plan.md")
	assert.Equal(t, StatusActive, entered.Status)
	assert.Equal(t, "docs/plan.md", entered.PlanPath)
	assert.Equal(t, []string{"docs/plan.md"}, entered.WriteAllowPaths)
	assert.Equal(t, string(runtimepolicy.ModeDefault), entered.PreviousMode)
	Save(store, entered)

	loaded := Load(store)
	assert.True(t, IsActive(loaded))
	assert.Equal(t, string(runtimepolicy.ModePlan), EffectivePermissionMode(loaded))

	exited, err := Exit(loaded, ExitApprove, "looks good")
	require.NoError(t, err)
	assert.Equal(t, StatusExited, exited.Status)
	assert.Equal(t, ExitApprove, exited.ExitDecision)
	assert.Equal(t, "looks good", exited.Notes)
	assert.Equal(t, string(runtimepolicy.ModeDefault), ResumeModeAfterExit(exited))
	Save(store, exited)

	reloaded := Load(store)
	assert.False(t, IsActive(reloaded))
	assert.Equal(t, ExitApprove, reloaded.ExitDecision)
}

func TestExitRequestChangesKeepsPlanMode(t *testing.T) {
	t.Parallel()
	state := Enter(string(runtimepolicy.ModeAcceptEdits), "")
	exited, err := Exit(state, ExitRequestChanges, "add risks")
	require.NoError(t, err)
	assert.Equal(t, string(runtimepolicy.ModePlan), ResumeModeAfterExit(exited))
}

func TestNormalizeExitDecision(t *testing.T) {
	t.Parallel()
	decision, err := NormalizeExitDecision("approved")
	require.NoError(t, err)
	assert.Equal(t, ExitApprove, decision)

	decision, err = NormalizeExitDecision("request-changes")
	require.NoError(t, err)
	assert.Equal(t, ExitRequestChanges, decision)

	_, err = NormalizeExitDecision("nope")
	require.Error(t, err)
}

func TestApplyToEngine(t *testing.T) {
	t.Parallel()
	engine := &runtimepolicy.Engine{}
	state := Enter("default", "plan.md")
	ApplyToEngine(engine, state)
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)
	assert.Equal(t, []string{"plan.md"}, engine.PlanWriteAllowPaths)

	// inactive does not clobber engine
	engine.Mode = runtimepolicy.ModeDefault
	engine.PlanWriteAllowPaths = []string{"other.md"}
	ApplyToEngine(engine, State{Status: StatusInactive})
	assert.Equal(t, runtimepolicy.ModeDefault, engine.Mode)
	assert.Equal(t, []string{"other.md"}, engine.PlanWriteAllowPaths)
}

func TestLoadFromMap(t *testing.T) {
	t.Parallel()
	store := &memoryContext{values: map[string]interface{}{
		ContextKey: map[string]interface{}{
			"status":            "active",
			"plan_path":         "plan.md",
			"previous_mode":     "default",
			"write_allow_paths": []interface{}{"plan.md", "notes.md"},
		},
	}}
	state := Load(store)
	assert.Equal(t, StatusActive, state.Status)
	assert.Equal(t, []string{"plan.md", "notes.md"}, state.WriteAllowPaths)
}
