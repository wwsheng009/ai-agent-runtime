package planmode

import (
	"path/filepath"
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

// TestEnterPlanResolvesWriteAllowPaths pins §4.7 A: the raw allowlist is kept
// for display/compat while EnterPlan records the workspace-anchored form.
func TestEnterPlanResolvesWriteAllowPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	state := EnterPlan(EnterOptions{
		PreviousMode:    string(runtimepolicy.ModeDefault),
		PlanPath:        "docs/plan.md",
		WriteAllowPaths: []string{"notes/extra.md"},
		Workspace:       workspace,
	})
	assert.Equal(t, StatusActive, state.Status)
	assert.Equal(t, []string{"docs/plan.md", "notes/extra.md"}, state.WriteAllowPaths,
		"the raw allowlist keeps its relative form")
	assert.Equal(t, []string{
		filepath.Join(workspace, "docs", "plan.md"),
		filepath.Join(workspace, "notes", "extra.md"),
	}, state.WriteAllowPathsResolved)
}

// TestEnterPlanWithoutWorkspaceMatchesEnter is the regression guard for "no
// workspace behaves exactly like today": resolved stays empty and the raw state
// is identical to Enter.
func TestEnterPlanWithoutWorkspaceMatchesEnter(t *testing.T) {
	t.Parallel()

	fromEnter := Enter(string(runtimepolicy.ModeDefault), "docs/plan.md", "notes/extra.md")
	fromOptions := EnterPlan(EnterOptions{
		PreviousMode:    string(runtimepolicy.ModeDefault),
		PlanPath:        "docs/plan.md",
		WriteAllowPaths: []string{"notes/extra.md"},
	})
	assert.Empty(t, fromOptions.WriteAllowPathsResolved)
	assert.Equal(t, fromEnter.Status, fromOptions.Status)
	assert.Equal(t, fromEnter.PlanPath, fromOptions.PlanPath)
	assert.Equal(t, fromEnter.PreviousMode, fromOptions.PreviousMode)
	assert.Equal(t, fromEnter.WriteAllowPaths, fromOptions.WriteAllowPaths)
}

func TestWriteAllowPathsResolvedRoundTrip(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := &memoryContext{}
	Save(store, EnterPlan(EnterOptions{PlanPath: "plan.md", Workspace: workspace}))

	loaded := Load(store)
	assert.Equal(t, []string{filepath.Join(workspace, "plan.md")}, loaded.WriteAllowPathsResolved)

	// Records persisted before this field existed keep the relative fallback.
	legacy := &memoryContext{values: map[string]interface{}{
		ContextKey: map[string]interface{}{
			"status":            "active",
			"plan_path":         "plan.md",
			"write_allow_paths": []interface{}{"plan.md"},
		},
	}}
	assert.Empty(t, Load(legacy).WriteAllowPathsResolved)
}

func TestApplyToEngineSetsResolvedPlanPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	engine := &runtimepolicy.Engine{}
	ApplyToEngine(engine, EnterPlan(EnterOptions{PlanPath: "plan.md", Workspace: workspace}))
	assert.Equal(t, runtimepolicy.ModePlan, engine.Mode)
	assert.Equal(t, []string{"plan.md"}, engine.PlanWriteAllowPaths)
	assert.Equal(t, []string{filepath.Join(workspace, "plan.md")}, engine.PlanWriteAllowPathsResolved)

	// An active state without resolved paths clears a stale resolved list so the
	// relative fallback applies again.
	engine.PlanWriteAllowPathsResolved = []string{filepath.Join(workspace, "stale.md")}
	ApplyToEngine(engine, Enter(string(runtimepolicy.ModeDefault), "plan.md"))
	assert.Nil(t, engine.PlanWriteAllowPathsResolved)
}

func TestResolveWriteAllowPathsIsBestEffort(t *testing.T) {
	t.Parallel()

	assert.Nil(t, resolveWriteAllowPaths([]string{"plan.md"}, ""), "no workspace keeps the relative fallback")
	assert.Nil(t, resolveWriteAllowPaths(nil, t.TempDir()))

	absolute := filepath.Join(t.TempDir(), "plan.md")
	assert.Equal(t, []string{absolute}, resolveWriteAllowPaths([]string{absolute}, t.TempDir()),
		"absolute entries are only cleaned")

	anchored, err := filepath.Abs("plan.md")
	require.NoError(t, err)
	assert.Equal(t, []string{anchored}, resolveWriteAllowPaths([]string{"plan.md"}, "."),
		"a relative workspace resolves against the process working directory")
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

func TestRecordAndConsumeReviewNotesRoundTrip(t *testing.T) {
	t.Parallel()

	state := Enter(string(runtimepolicy.ModeDefault), "plan.md")
	state = RecordReviewNotes(state, "  add rollback risks  ")
	assert.Equal(t, "add rollback risks", state.PendingReviewNotes)
	assert.Equal(t, 1, state.ReviewRound)

	// Persisted shape keeps both fields.
	store := &memoryContext{}
	Save(store, state)
	loaded := Load(store)
	assert.Equal(t, "add rollback risks", loaded.PendingReviewNotes)
	assert.Equal(t, 1, loaded.ReviewRound)

	cleared, notes := ConsumeReviewNotes(loaded)
	assert.Equal(t, "add rollback risks", notes)
	assert.Empty(t, cleared.PendingReviewNotes)
	assert.Equal(t, 1, cleared.ReviewRound, "round count survives consumption")

	state = RecordReviewNotes(cleared, "second pass")
	assert.Equal(t, 2, state.ReviewRound)
}

func TestExitSourceRoundTripAndPendingRequest(t *testing.T) {
	t.Parallel()

	pending := RequestExitFrom(Enter(string(runtimepolicy.ModeDefault), "plan.md"), ExitSourceModel, "ready for review")
	assert.True(t, ExitRequested(pending))
	assert.Equal(t, ExitSourceModel, pending.LastExitSource)
	assert.Equal(t, StatusActive, pending.Status, "a pending request keeps plan mode active")
	assert.Equal(t, "ready for review", pending.Notes)

	store := &memoryContext{}
	Save(store, pending)
	loaded := Load(store)
	assert.True(t, ExitRequested(loaded))
	assert.Equal(t, ExitSourceModel, loaded.LastExitSource)

	assert.Equal(t, ExitSourceUser, NormalizeExitSource(""))
	assert.Equal(t, ExitSourceModel, NormalizeExitSource("agent"))
	assert.Equal(t, ExitSourceModel, NormalizeExitSource("MODEL"))
}

func TestModelMayDecideExitHonorsAutonomyEnv(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	assert.False(t, ModelMayDecideExit())

	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "1")
	assert.True(t, ModelMayDecideExit())

	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "off")
	assert.False(t, ModelMayDecideExit())
}

func TestResumeModeAfterExitDowngradesBypassOnApprove(t *testing.T) {
	t.Parallel()

	state := Enter(string(runtimepolicy.ModeBypassPermissions), "plan.md")
	approved, err := Exit(state, ExitApprove, "")
	require.NoError(t, err)
	assert.Equal(t, string(runtimepolicy.ModeAcceptEdits), ResumeModeAfterExit(approved))

	quit, err := Exit(Enter(string(runtimepolicy.ModeBypassPermissions), "plan.md"), ExitQuit, "")
	require.NoError(t, err)
	assert.Equal(t, string(runtimepolicy.ModeBypassPermissions), ResumeModeAfterExit(quit),
		"quit restores the previous mode untouched")
}
