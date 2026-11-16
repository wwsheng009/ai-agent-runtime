package runtimeapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestResolveServerLeaseStorePathUsesSharedDefault(t *testing.T) {
	sessionDir := t.TempDir()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir

	path, dsn := resolveServerLeaseStorePath(cfg, "")
	require.Empty(t, dsn)
	require.Equal(t, filepath.Join(sessionDir, "runtime", "session_leases.sqlite"), filepath.Clean(path),
		"empty lease config must resolve to the same shared lease file as aicli")
}

func TestResolveServerLeaseStorePathPreservesExplicitDSN(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = t.TempDir()
	cfg.SessionRuntime.LeaseStoreDSN = "file:shared-leases?mode=memory&cache=shared"

	path, dsn := resolveServerLeaseStorePath(cfg, "")
	require.Empty(t, path)
	require.Equal(t, cfg.SessionRuntime.LeaseStoreDSN, dsn,
		"an explicit DSN must not be overridden by a default path")
}

func TestResolveServerLeaseStorePathLeavesEmbeddedMemoryHostUntouched(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	path, dsn := resolveServerLeaseStorePath(cfg, "")
	require.Empty(t, path)
	require.Empty(t, dsn,
		"memory-only embedded hosts without sessions.dir must not open the user-level lease database")
}
