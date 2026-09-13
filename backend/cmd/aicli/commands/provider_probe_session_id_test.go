package commands

import (
	"strings"
	"testing"
)

// TestProviderProbeSessionIDIsUniquePerRun reproduces the coarse-clock hazard
// for probe traffic: the synthetic session id routes gateway affinity, so two
// probe runs that share an id are treated as one session and can pollute each
// other's routing (and the real session's affinity cache).
func TestProviderProbeSessionIDIsUniquePerRun(t *testing.T) {
	const runs = 64

	seen := make(map[string]struct{}, runs)
	for i := 0; i < runs; i++ {
		id := providerProbeSessionID()
		if !strings.HasPrefix(id, "aicli-probe-") {
			t.Fatalf("probe session id %q lost its prefix", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("probe session id %q was minted twice", id)
		}
		seen[id] = struct{}{}
	}
}
