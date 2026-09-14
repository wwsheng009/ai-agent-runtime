package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateTurnBudget_Levels(t *testing.T) {
	spec := TurnBudgetSpec{MaxSteps: 300, MaxWallClock: 40 * time.Minute, MaxTokens: 1000}

	ok := EvaluateTurnBudget(spec, TurnBudgetUsage{CompletedSteps: 100, Elapsed: 10 * time.Minute, TokensSpent: 200})
	require.Equal(t, TurnBudgetLevelOK, ok.Level)
	require.Empty(t, ok.Reasons)
	require.False(t, ok.ReachedSoftLimit())

	soft := EvaluateTurnBudget(spec, TurnBudgetUsage{CompletedSteps: 240, Elapsed: 32 * time.Minute, TokensSpent: 620})
	require.Equal(t, TurnBudgetLevelSoft, soft.Level)
	require.True(t, soft.ReachedSoftLimit())
	require.False(t, soft.HardReason(TurnBudgetDimensionSteps))
	require.Contains(t, soft.SoftReasons, TurnBudgetDimensionSteps)

	hard := EvaluateTurnBudget(spec, TurnBudgetUsage{CompletedSteps: 300, Elapsed: 41 * time.Minute, TokensSpent: 1200})
	require.Equal(t, TurnBudgetLevelHard, hard.Level)
	require.True(t, hard.ReachedSoftLimit())
	require.True(t, hard.HardReason(TurnBudgetDimensionWallClock))
	require.True(t, hard.HardReason(TurnBudgetDimensionTokens))
	require.True(t, hard.HardReason(TurnBudgetDimensionSteps))
}

func TestEvaluateTurnBudget_LineMatchesProductFormat(t *testing.T) {
	state := EvaluateTurnBudget(
		TurnBudgetSpec{MaxSteps: 300, MaxWallClock: 40 * time.Minute, MaxTokens: 1000},
		TurnBudgetUsage{CompletedSteps: 240, Elapsed: 32 * time.Minute, TokensSpent: 620},
	)
	require.Equal(t, "turn budget: step 240/300 · 32m/40m · tokens 62%", state.Line)
	require.Equal(t, "step 240/300", state.StepsLine)
	require.Equal(t, "32m/40m", state.WallLine)
	require.Equal(t, "tokens 62%", state.TokensLine)
}

func TestEvaluateTurnBudget_SkipsUnconfiguredDimensions(t *testing.T) {
	unlimitedSteps := EvaluateTurnBudget(
		TurnBudgetSpec{MaxTokens: 1000},
		TurnBudgetUsage{CompletedSteps: 7, Elapsed: 3 * time.Minute, TokensSpent: 620},
	)
	require.Equal(t, "turn budget: tokens 62%", unlimitedSteps.Line)
	require.Equal(t, TurnBudgetLevelOK, unlimitedSteps.Level)

	disabledTokens := EvaluateTurnBudget(
		TurnBudgetSpec{MaxSteps: 10},
		TurnBudgetUsage{CompletedSteps: 7, TokensSpent: 900},
	)
	require.Equal(t, "turn budget: step 7/10", disabledTokens.Line)
	require.Equal(t, TurnBudgetLevelOK, disabledTokens.Level)
}

func TestEvaluateTurnBudget_EmptySpecStaysSilent(t *testing.T) {
	state := EvaluateTurnBudget(TurnBudgetSpec{}, TurnBudgetUsage{CompletedSteps: 99, Elapsed: time.Hour, TokensSpent: 9999})
	require.Equal(t, TurnBudgetLevelOK, state.Level)
	require.Empty(t, state.Line)
	require.False(t, state.ReachedSoftLimit())
	require.True(t, TurnBudgetSpec{}.Empty())
	require.False(t, TurnBudgetSpec{MaxSteps: 1}.Empty())
	require.False(t, TurnBudgetSpec{MaxWallClock: time.Second}.Empty())
	require.False(t, TurnBudgetSpec{MaxTokens: 1}.Empty())
}

func TestEvaluateTurnBudget_SoftRatioBoundary(t *testing.T) {
	spec := TurnBudgetSpec{MaxTokens: 100}
	justBelow := EvaluateTurnBudget(spec, TurnBudgetUsage{TokensSpent: 79})
	require.Equal(t, TurnBudgetLevelOK, justBelow.Level)

	atWatermark := EvaluateTurnBudget(spec, TurnBudgetUsage{TokensSpent: 80})
	require.Equal(t, TurnBudgetLevelSoft, atWatermark.Level)
	require.Equal(t, []string{TurnBudgetDimensionTokens}, atWatermark.SoftReasons)

	exhausted := EvaluateTurnBudget(spec, TurnBudgetUsage{TokensSpent: 100})
	require.Equal(t, TurnBudgetLevelHard, exhausted.Level)
	require.Equal(t, []string{TurnBudgetDimensionTokens}, exhausted.HardReasons)
	require.Equal(t, "tokens 100%", exhausted.TokensLine)
}

func TestTokensSpentFromBudget(t *testing.T) {
	require.Equal(t, 0, TokensSpentFromBudget(0, 500))
	require.Equal(t, 0, TokensSpentFromBudget(500, 500))
	require.Equal(t, 200, TokensSpentFromBudget(500, 300))
	// remaining 大于上限（例如预算被放大）时不得报负数消耗。
	require.Equal(t, 0, TokensSpentFromBudget(500, 600))
	require.Equal(t, 500, TokensSpentFromBudget(500, 0))
	// 透支保留真实值（505/500 = 101%），便于上报"超支"，不静默截断到 100%。
	require.Equal(t, 505, TokensSpentFromBudget(500, -5))
}

func TestFormatTurnBudgetDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{in: 0, want: "0s"},
		{in: -time.Second, want: "0s"},
		{in: 45 * time.Second, want: "45s"},
		{in: 90 * time.Second, want: "1m"},
		{in: 32 * time.Minute, want: "32m"},
		{in: 40 * time.Minute, want: "40m"},
		{in: time.Hour, want: "1h"},
		{in: 80 * time.Minute, want: "1h20m"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, FormatTurnBudgetDuration(tc.in), tc.in.String())
	}
}

func TestTurnBudgetSoftLandingMessageCarriesWatermarkAndHandoff(t *testing.T) {
	state := EvaluateTurnBudget(
		TurnBudgetSpec{MaxSteps: 300, MaxWallClock: 40 * time.Minute, MaxTokens: 1000},
		TurnBudgetUsage{CompletedSteps: 240, Elapsed: 32 * time.Minute, TokensSpent: 620},
	)
	body := TurnBudgetSoftLandingMessage(state)
	require.Contains(t, body, "80% of its configured budget")
	require.Contains(t, body, "turn budget: step 240/300 · 32m/40m · tokens 62%")
	require.Contains(t, body, "Wrap up now")
	require.Contains(t, body, "persist any workspace/session findings")
	require.Contains(t, body, "exact next action to resume")
}

func TestTurnBudgetHardStopMessageCarriesWatermark(t *testing.T) {
	state := EvaluateTurnBudget(TurnBudgetSpec{MaxTokens: 100}, TurnBudgetUsage{TokensSpent: 100})
	body := TurnBudgetHardStopMessage(state)
	require.Contains(t, body, "token 预算上限")
	require.Contains(t, body, "tokens 100%")
	require.Contains(t, body, "续跑")
}

func TestNewTurnBudgetReminderMessageIsDurableAndTyped(t *testing.T) {
	state := EvaluateTurnBudget(TurnBudgetSpec{MaxTokens: 100}, TurnBudgetUsage{TokensSpent: 85})
	msg := newTurnBudgetReminderMessage(state)
	require.NotNil(t, msg)
	require.True(t, IsSystemReminder(*msg))
	require.Equal(t, ReminderKindTurnBudget, ReminderKindOf(*msg))
	require.True(t, IsSystemReminderDurable(*msg), "wrap-up handoff must survive persist")
	require.Contains(t, msg.Content, "<system-reminder kind=\"turn_budget\">")
	require.Contains(t, msg.Content, "tokens 85%")
}

func TestNormalizeReminderKindKeepsTurnBudgetCanonical(t *testing.T) {
	require.Equal(t, ReminderKindTurnBudget, NormalizeReminderKind(" Turn_Budget "))
	require.False(t, IsPureAdvisoryReminderKind(ReminderKindTurnBudget))
}
