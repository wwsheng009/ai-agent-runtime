package agent

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Node identity semantics (docs/gateway/10-runbook.md §13):
//
// The id must be stable across reconnection — including failover re-dials to a
// different gateway instance — so the same logical agent replaces its own
// stale registry entry instead of accumulating a second node. It must NOT be
// shared between processes on the same machine: two concurrent aicli TUIs are
// two independent agents, and a shared machine-level id makes the later
// REGISTER overwrite the earlier one (observed: two TUIs, one registry node).
//
// Therefore the id is generated once per process and cached for the process
// lifetime (sync.Once). It is deliberately not persisted: a restart creates a
// new agent instance, whose first REGISTER replaces the previous instance's
// stale entry — exactly the failover handoff we want. The overlayUnit test
// hooks keep the generation deterministic for tests.
var (
	nodeIDOnce  sync.Once
	cachedNodID string
)

// ProcessNodeID returns this process's gateway node id, generating it on first
// use. Always non-empty; the generator falling back to a zero-entropy id is
// prevented by construction (crypto/rand).
func ProcessNodeID() string {
	nodeIDOnce.Do(func() {
		cachedNodID = generateNodeID()
	})
	return cachedNodID
}

func generateNodeID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is catastrophic anyway; still produce a
		// deterministic-but-unique-enough fallback from time.
		return "node-" + hex.EncodeToString([]byte(timeNowBytes()))
	}
	return "node-" + hex.EncodeToString(buf)
}

func timeNowBytes() []byte {
	ns := timeNowUnixNano()
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[i] = byte(ns >> (56 - 8*i))
	}
	return out
}
