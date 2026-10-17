package goal

import (
	"testing"
)

// G2 轮数上限（DSH goal 借鉴，docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md
// §3.1）：durable 轮数计数、上限判定与 admit 单点语义。
func TestRoundLimitReached(t *testing.T) {
	if RoundLimitReached(nil) {
		t.Fatal("nil goal must not report round limit")
	}
	if RoundLimitReached(&SessionGoal{Status: StatusActive, MaxRounds: 0, RoundsStarted: 99}) {
		t.Fatal("MaxRounds=0 means unlimited")
	}
	if RoundLimitReached(&SessionGoal{Status: StatusPaused, MaxRounds: 3, RoundsStarted: 3}) {
		t.Fatal("only active goals are round-limited")
	}
	if RoundLimitReached(&SessionGoal{Status: StatusActive, MaxRounds: 3, RoundsStarted: 2}) {
		t.Fatal("below the cap must not report reached")
	}
	if !RoundLimitReached(&SessionGoal{Status: StatusActive, MaxRounds: 3, RoundsStarted: 3}) {
		t.Fatal("at the cap must report reached")
	}
}

func TestAdmitGoalRound(t *testing.T) {
	if AdmitGoalRound(nil) {
		t.Fatal("nil goal must not admit")
	}
	paused := &SessionGoal{Status: StatusPaused, MaxRounds: 2, RoundsStarted: 1}
	if AdmitGoalRound(paused) {
		t.Fatal("non-active goal must not admit")
	}
	if paused.RoundsStarted != 1 {
		t.Fatalf("non-active goal must not be counted, got %d", paused.RoundsStarted)
	}

	active := &SessionGoal{Status: StatusActive, MaxRounds: 0, RoundsStarted: 4}
	if AdmitGoalRound(active) {
		t.Fatal("unlimited goal never reports the cap")
	}
	if active.RoundsStarted != 5 {
		t.Fatalf("unlimited goal must still count rounds, got %d", active.RoundsStarted)
	}

	bounded := &SessionGoal{Status: StatusActive, MaxRounds: 2, RoundsStarted: 0}
	if AdmitGoalRound(bounded) {
		t.Fatal("first admitted round must not reach a cap of 2")
	}
	if bounded.RoundsStarted != 1 {
		t.Fatalf("first admit must increment to 1, got %d", bounded.RoundsStarted)
	}
	if !AdmitGoalRound(bounded) {
		t.Fatal("second admitted round must reach the cap of 2")
	}
	if bounded.RoundsStarted != 2 {
		t.Fatalf("second admit must increment to 2, got %d", bounded.RoundsStarted)
	}
}
