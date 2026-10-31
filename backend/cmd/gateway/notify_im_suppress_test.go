package main

import (
	"path/filepath"
	"testing"
)

// TestLocalSuppressionFromNodeTargets pins the automatic node-local
// suppression contract: only source=node targets owned by this node produce
// rules; source=gateway targets never suppress local fan-out.
func TestLocalSuppressionFromNodeTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	view := notifyIMFile{
		Version: 1,
		Targets: []notifyIMTarget{
			{ID: "node-default", Source: "node", NodeID: "n1", Scope: "all"},
			{ID: "node-sessions", Source: "node", NodeID: "n1", Scope: "sessions", Sessions: []string{"s1"}, Kinds: []string{"approval", "completed"}},
			{ID: "node-other", Source: "node", NodeID: "n2"},
			{ID: "gateway", ConnectorID: "qq-ops", Chat: "123"},
		},
	}
	if err := writeNotifyIMFile(path, view); err != nil {
		t.Fatalf("write config: %v", err)
	}
	src := newNotifyIMSource(path)

	rules := src.LocalSuppression("n1")
	if len(rules) != 2 {
		t.Fatalf("n1 rules = %+v, want 2", rules)
	}
	// Default node target: same default kinds as channels.IM.acceptsKind.
	if len(rules[0].Sessions) != 0 {
		t.Fatalf("default rule must cover all sessions: %+v", rules[0])
	}
	if got := rules[0].Kinds; len(got) != 2 || got[0] != "error" || got[1] != "waiting" {
		t.Fatalf("default kinds = %v, want [error waiting]", got)
	}
	// Explicit sessions rule.
	if len(rules[1].Sessions) != 1 || rules[1].Sessions[0] != "s1" {
		t.Fatalf("session rule = %+v", rules[1])
	}
	if got := rules[1].Kinds; len(got) != 2 || got[0] != "approval" || got[1] != "completed" {
		t.Fatalf("explicit kinds = %v", got)
	}

	if got := src.LocalSuppression("n2"); len(got) != 1 {
		t.Fatalf("n2 rules = %+v, want 1", got)
	}
	if got := src.LocalSuppression("n3"); len(got) != 0 {
		t.Fatalf("unrelated node must have no rules: %+v", got)
	}
	if got := src.LocalSuppression(""); len(got) != 0 {
		t.Fatalf("empty node must have no rules: %+v", got)
	}
}
