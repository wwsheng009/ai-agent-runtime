package connectors

import "testing"

// TestNormalizeRunnerPreference 锁定词表与容错（未知值回落默认，不炸启动）。
func TestNormalizeRunnerPreference(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", RunnerPreferenceDefault},
		{"runtime-server", RunnerPreferenceRuntimeServer},
		{"AICLI", RunnerPreferenceAICLI},
		{"  round-robin  ", RunnerPreferenceRoundRobin},
		{"typo", RunnerPreferenceDefault},
	}
	for _, tc := range cases {
		if got := NormalizeRunnerPreference(tc.in); got != tc.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func runnerCandidates() []runnerCandidate {
	return []runnerCandidate{
		{HostKind: RunnerPreferenceAICLI, NodeID: "aicli-1", Alive: true},
		{HostKind: RunnerPreferenceRuntimeServer, NodeID: "rs-1", Alive: true},
		{HostKind: RunnerPreferenceRuntimeServer, NodeID: "rs-2", Alive: false},
	}
}

// TestPickRunnerForUnbound 锁定 R3 选举语义（D2）：偏好优先 + 回落 + 轮转。
func TestPickRunnerForUnbound(t *testing.T) {
	candidates := runnerCandidates()

	// 默认（runtime-server 优先）：选 rs-1（rs-2 不 alive 不参选）。
	got, _, ok := PickRunnerForUnbound("", candidates, 0)
	if !ok || got.NodeID != "rs-1" {
		t.Fatalf("default pick = %+v ok=%v", got, ok)
	}
	// aicli 优先：选 aicli-1。
	got, _, ok = PickRunnerForUnbound("aicli", candidates, 0)
	if !ok || got.NodeID != "aicli-1" {
		t.Fatalf("aicli pick = %+v ok=%v", got, ok)
	}
	// round-robin：稳定排序（aicli-1 < rs-1）后轮转。
	got, next, ok := PickRunnerForUnbound("round-robin", candidates, 0)
	if !ok || got.NodeID != "aicli-1" {
		t.Fatalf("rr first = %+v ok=%v", got, ok)
	}
	got, next, ok = PickRunnerForUnbound("round-robin", candidates, next)
	if !ok || got.NodeID != "rs-1" {
		t.Fatalf("rr second = %+v ok=%v", got, ok)
	}
	// 回绕。
	got, _, ok = PickRunnerForUnbound("round-robin", candidates, next)
	if !ok || got.NodeID != "aicli-1" {
		t.Fatalf("rr wrap = %+v ok=%v", got, ok)
	}

	// 全部掉线：无候选不猜。
	dead := []runnerCandidate{{HostKind: RunnerPreferenceRuntimeServer, NodeID: "rs-1", Alive: false}}
	if _, _, ok := PickRunnerForUnbound("", dead, 0); ok {
		t.Fatal("no alive candidates must return false")
	}
	// 空候选同样 false。
	if _, _, ok := PickRunnerForUnbound("", nil, 0); ok {
		t.Fatal("empty candidates must return false")
	}
}
