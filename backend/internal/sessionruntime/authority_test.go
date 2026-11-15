package sessionruntime

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type authorityPathStore struct {
	path string
}

func (s authorityPathStore) Path() string { return s.path }

func TestAuthorityForOwnerKind(t *testing.T) {
	require.Equal(t, AuthorityUnowned, AuthorityForOwnerKind(""))
	require.Equal(t, AuthorityRuntimeServer, AuthorityForOwnerKind("runtime-server-actor"))
	require.Equal(t, AuthorityRuntimeServer, AuthorityForOwnerKind("runtime-server-agent-chat"))
	require.Equal(t, AuthorityAICLILocal, AuthorityForOwnerKind("aicli-actor"))
	require.Equal(t, AuthorityScheduled, AuthorityForOwnerKind("scheduled-daemon"))
	require.Equal(t, AuthorityExternal, AuthorityForOwnerKind("unknown-owner"))
}

func TestStoreIDAndDurability(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_runtime.sqlite")
	store := authorityPathStore{path: path}

	want := "sqlite:" + filepath.ToSlash(path)
	if absolute, err := filepath.Abs(path); err == nil {
		want = "sqlite:" + filepath.ToSlash(absolute)
	}
	require.Equal(t, want, StoreID(store))
	require.True(t, StoreDurable(store))

	memoryID := StoreID(struct{}{})
	require.Contains(t, memoryID, "memory:")
	require.Contains(t, memoryID, strconv.Itoa(os.Getpid()))
	require.False(t, StoreDurable(struct{}{}))
	require.Empty(t, StoreID(nil))
}

func TestBuildAuthorityInfoSeparatesExecutionAuthorityFromStorePath(t *testing.T) {
	now := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	runtimeStore := authorityPathStore{path: filepath.Join(t.TempDir(), "server_runtime.sqlite")}
	eventStore := authorityPathStore{path: filepath.Join(t.TempDir(), "server_events.sqlite")}
	leaseStore := authorityPathStore{path: filepath.Join(t.TempDir(), "session_leases.sqlite")}

	unowned := BuildAuthorityInfo(AuthorityInput{
		LocalAuthority: AuthorityRuntimeServer,
		Now:            now,
		RuntimeStore:   runtimeStore,
		EventStore:     eventStore,
		LeaseStore:     leaseStore,
	})
	require.Equal(t, AuthorityUnowned, unowned.ExecutionAuthority)
	require.True(t, unowned.Authoritative)
	require.Equal(t, "unowned", unowned.AuthoritySource)
	require.Equal(t, StoreID(leaseStore), unowned.AuthorityDomain)
	require.True(t, unowned.RuntimeStoreDurable)
	require.True(t, unowned.EventStoreDurable)
	require.Nil(t, unowned.LeaseExpiresAt)

	foreign := BuildAuthorityInfo(AuthorityInput{
		LocalAuthority: AuthorityRuntimeServer,
		OwnerKind:      "aicli-actor",
		OwnerID:        "aicli-actor:123",
		PID:            123,
		Hostname:       "host-a",
		ExpiresAt:      now.Add(time.Minute),
		Now:            now,
		RuntimeStore:   runtimeStore,
		EventStore:     eventStore,
		LeaseStore:     leaseStore,
	})
	require.Equal(t, AuthorityAICLILocal, foreign.ExecutionAuthority)
	require.False(t, foreign.Authoritative)
	require.Equal(t, "lease", foreign.AuthoritySource)
	require.Equal(t, 123, foreign.LeaseOwnerPID)
	require.NotNil(t, foreign.LeaseExpiresAt)

	expired := BuildAuthorityInfo(AuthorityInput{
		LocalAuthority: AuthorityRuntimeServer,
		OwnerKind:      "aicli-actor",
		ExpiresAt:      now.Add(-time.Second),
		Now:            now,
		RuntimeStore:   runtimeStore,
		EventStore:     eventStore,
		LeaseStore:     leaseStore,
	})
	require.Equal(t, AuthorityUnowned, expired.ExecutionAuthority)
	require.True(t, expired.Authoritative)
}
