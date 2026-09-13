package agent

import (
	"strings"
	"testing"
)

// TestSubagentBatchFencingTokenRotatesPerClaim pins the fencing contract: every
// durable claim mints a fresh token, even when two claims land inside the same
// clock tick. Stale workers compare the token by equality, so a duplicate would
// let a superseded worker keep heartbeating (and terminalizing) a batch that a
// newer claim already owns.
func TestSubagentBatchFencingTokenRotatesPerClaim(t *testing.T) {
	const claims = 64

	seen := make(map[string]struct{}, claims)
	for i := 0; i < claims; i++ {
		token := subagentBatchFencingToken("owner-a")
		if !strings.HasPrefix(token, "owner-a/") {
			t.Fatalf("fencing token %q lost its owner prefix", token)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("fencing token %q was minted twice", token)
		}
		seen[token] = struct{}{}
	}
}

// TestSubagentBatchFencingTokenTrimsOwner keeps the token well-formed for owner
// ids that arrive with surrounding whitespace.
func TestSubagentBatchFencingTokenTrimsOwner(t *testing.T) {
	token := subagentBatchFencingToken("  owner-b  ")
	if !strings.HasPrefix(token, "owner-b/") {
		t.Fatalf("fencing token %q did not trim the owner id", token)
	}
	if strings.Contains(strings.TrimPrefix(token, "owner-b/"), " ") {
		t.Fatalf("fencing token %q contains whitespace", token)
	}
}
