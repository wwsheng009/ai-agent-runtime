package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestBackgroundMaxInteractiveSessionsPassthrough pins the 2026-10-14 knob:
// background.maxInteractiveSessions is the global ceiling for live interactive
// (write_stdin/PTY) sessions. They do not consume the maxConcurrentJobs
// scheduling slots, and 0 keeps the background manager's historical default
// (64), so partial `background:` blocks stay behavior-preserving.
func TestBackgroundMaxInteractiveSessionsPassthrough(t *testing.T) {
	require.Zero(t, DefaultRuntimeConfig().Background.MaxInteractiveSessions,
		"absent config must stay 0 = background manager default (64)")

	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("background:\n  maxInteractiveSessions: 8\n"), cfg))
	require.Equal(t, 8, cfg.Background.MaxInteractiveSessions)

	// Partial block tolerance: writing only maxConcurrentJobs must not disturb
	// the interactive ceiling.
	partial := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("background:\n  maxConcurrentJobs: 2\n"), partial))
	require.Equal(t, 2, partial.Background.MaxConcurrentJobs)
	require.Zero(t, partial.Background.MaxInteractiveSessions)
}
