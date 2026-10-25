package searchcore

import (
	"strings"
	"testing"
)

// TestRipgrepNoConfigIsThreeState pins the only base flag a caller may turn off.
// The three states exist so that "unset" keeps the deterministic default (a
// shared tool must not answer differently on two machines because one has a
// RIPGREP_CONFIG_PATH) while `no_config=false` still inherits the machine config
// when a caller explicitly asks for it.
func TestRipgrepNoConfigIsThreeState(t *testing.T) {
	cases := []struct {
		name     string
		opts     GrepOptions
		wantFlag bool
	}{
		{"unset keeps the deterministic default", GrepOptions{}, true},
		{"explicit true asks for it", GrepOptions{NoConfig: true, NoConfigSet: true}, true},
		{"explicit false inherits the machine config", GrepOptions{NoConfigSet: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := ripgrepSearchArgs(tc.opts)
			has := false
			for _, arg := range args {
				if arg == ripgrepNoConfigFlag {
					has = true
				}
			}
			if has != tc.wantFlag {
				t.Fatalf("ripgrepSearchArgs(%+v) = %v, --no-config present=%v, want %v",
					tc.opts, args, has, tc.wantFlag)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--threads 1") || !strings.Contains(joined, "--sort path") {
				t.Fatalf("dropping --no-config must not disturb the other base flags: %s", joined)
			}
		})
	}
}

// TestGrepNoConfigRequestReachesTheEngineAndTheMetadata walks the whole seam for
// the three states: what the caller wrote, what the engine receives, and the
// effective value the call reports back. The last one matters most - an unset
// request runs without a config, so reporting the raw request (false) would
// describe the opposite of what ran.
func TestGrepNoConfigRequestReachesTheEngineAndTheMetadata(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name              string
		args              map[string]any
		wantReported      bool
		wantSetAtEngine   bool
		wantValueAtEngine bool
	}{
		{"unset", map[string]any{"pattern": "needle"}, true, false, false},
		{"true", map[string]any{"pattern": "needle", "no_config": true}, true, true, true},
		{"false", map[string]any{"pattern": "needle", "no_config": false}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			searcher := &fakeSearcher{available: true}
			result := runGrepWithSearcher(t, root, searcher, tc.args)
			if result.Metadata["no_config"] != tc.wantReported {
				t.Fatalf("metadata.no_config = %#v, want %v (the effective value)",
					result.Metadata["no_config"], tc.wantReported)
			}
			if len(searcher.calls) == 0 {
				t.Fatalf("the engine was never asked for %v", tc.args)
			}
			got := searcher.calls[0]
			if got.NoConfigSet != tc.wantSetAtEngine || got.NoConfig != tc.wantValueAtEngine {
				t.Fatalf("engine received NoConfig=%v NoConfigSet=%v, want %v/%v",
					got.NoConfig, got.NoConfigSet, tc.wantValueAtEngine, tc.wantSetAtEngine)
			}
		})
	}
}
