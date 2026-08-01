package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// newOwnerOrchestrator builds an orchestrator with a stable owner identity and
// a short lease TTL so tests can exercise takeover quickly.
func newOwnerOrchestrator(store Store, ownerID string, ttl time.Duration) *Orchestrator {
	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.OwnerID = ownerID
	orchestrator.OwnerInstance = "test-instance"
	orchestrator.OwnerLeaseTTL = ttl
	orchestrator.TickInterval = 50 * time.Millisecond
	return orchestrator
}

func waitForLease(t *testing.T, store *SQLiteStore, teamID, ownerID string) *OrchestratorLease {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lease, err := store.GetOrchestratorLease(context.Background(), teamID)
		require.NoError(t, err)
		if lease != nil && lease.OwnerID == ownerID {
			return lease
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("orchestrator lease for team %s was never acquired by %s", teamID, ownerID)
	return nil
}

func expireLease(t *testing.T, store *SQLiteStore, teamID string) {
	t.Helper()
	result, err := store.db.Exec(`
		UPDATE orchestrator_owner_leases
		SET lease_until = ?
		WHERE team_id = ?
	`, formatTime(time.Now().UTC().Add(-time.Second)), teamID)
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected, "lease row must exist before expiring it")
}

// TestOrchestratorOwnerLeaseSingleWinner verifies that two runtime instances
// competing for the same team produce exactly one owner (P5 acceptance item 1).
func TestOrchestratorOwnerLeaseSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)

	o1 := newOwnerOrchestrator(store, "owner-a", 2*time.Second)
	o2 := newOwnerOrchestrator(store, "owner-b", 2*time.Second)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- o1.RunWithWake(runCtx, teamID, nil) }()

	lease := waitForLease(t, store, teamID, "owner-a")
	require.Equal(t, "owner-a", lease.OwnerID)
	require.NotEmpty(t, lease.FencingToken)
	require.Equal(t, 0, lease.RestartCount)

	// The second instance must be rejected while the first holds a valid lease.
	require.ErrorIs(t, o2.RunWithWake(context.Background(), teamID, nil), ErrOrchestratorLeaseHeld)

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
}

// TestOrchestratorOwnerTakeoverAfterCrash verifies that a crashed owner's lease
// expires and a second instance takes over with a fresh fencing token and a
// bumped restart counter (P5 acceptance item 2).
func TestOrchestratorOwnerTakeoverAfterCrash(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)

	o1 := newOwnerOrchestrator(store, "owner-a", 400*time.Millisecond)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- o1.RunWithWake(runCtx, teamID, nil) }()
	first := waitForLease(t, store, teamID, "owner-a")
	require.Equal(t, "owner-a", first.OwnerID)

	// Simulate a crash: the owner stops renewing and its lease expires.
	expireLease(t, store, teamID)

	// The old loop must notice the lost lease on its next renewal.
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, ErrOrchestratorLeaseLost)
	case <-time.After(5 * time.Second):
		t.Fatal("old owner did not detect lease loss")
	}

	// A second instance takes over immediately after expiry.
	o2 := newOwnerOrchestrator(store, "owner-b", 2*time.Second)
	runCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- o2.RunWithWake(runCtx2, teamID, nil) }()
	second := waitForLease(t, store, teamID, "owner-b")
	require.Equal(t, "owner-b", second.OwnerID)
	require.NotEqual(t, first.FencingToken, second.FencingToken, "takeover must rotate the fencing token")
	require.Equal(t, 1, second.RestartCount, "takeover must bump the restart counter")

	cancel2()
	require.ErrorIs(t, <-errCh2, context.Canceled)
}

// TestOrchestratorStaleOwnerCannotClaim verifies that an owner whose lease was
// taken over can no longer claim tasks with its old fencing token (P5
// acceptance item 3).
func TestOrchestratorStaleOwnerCannotClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	mateID, err := store.UpsertTeammate(ctx, Teammate{
		ID:      "mate-a",
		TeamID:  teamID,
		Name:    "mate-a",
		State:   TeammateStateIdle,
	})
	require.NoError(t, err)
	createReady := func(title string) string {
		taskID, err := store.CreateTask(ctx, Task{
			TeamID: teamID,
			Title:  title,
			Goal:   "run",
			Status: TaskStatusReady,
		})
		require.NoError(t, err)
		return taskID
	}
	task1 := createReady("one")
	task2 := createReady("two")

	registry := NewAgentControlTaskRegistry(store)
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)

	// Owner A acquires the lease and claims task1.
	leaseA, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-a",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	_, claimed, err := registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              task1,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		Assignee:        mateID,
		LeaseUntil:      leaseUntil,
		OwnerID:         "owner-a",
		OwnerToken:      leaseA.FencingToken,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	// Owner A crashes; its lease expires and owner B takes over.
	expireLease(t, store, teamID)
	leaseB, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-b",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, leaseB.RestartCount)

	// The stale owner with the old token must be rejected...
	_, _, err = registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              task2,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		Assignee:        mateID,
		LeaseUntil:      leaseUntil,
		OwnerID:         "owner-a",
		OwnerToken:      leaseA.FencingToken,
	})
	require.ErrorIs(t, err, agentcontrol.ErrOrchestratorOwnerMismatch)

	// ...while the current owner with the fresh token succeeds.
	_, claimed, err = registry.ClaimAgentControlTask(ctx, agentcontrol.TaskClaimRequest{
		ID:              task2,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		Assignee:        mateID,
		LeaseUntil:      leaseUntil,
		OwnerID:         "owner-b",
		OwnerToken:      leaseB.FencingToken,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	valid, err := store.ValidateOrchestratorOwner(ctx, teamID, "owner-a", leaseA.FencingToken)
	require.NoError(t, err)
	require.False(t, valid, "stale owner token must not validate")
	valid, err = store.ValidateOrchestratorOwner(ctx, teamID, "owner-b", leaseB.FencingToken)
	require.NoError(t, err)
	require.True(t, valid, "current owner token must validate")
}

// TestOrchestratorLeaseRenewExtends verifies that renewal extends the lease
// window and that an unrenewed lease expires.
func TestOrchestratorLeaseRenewExtends(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)

	lease, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-a",
	}, 500*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease)
	firstUntil := lease.LeaseUntil

	time.Sleep(100 * time.Millisecond)
	renewed, err := store.RenewOrchestratorLease(ctx, teamID, "owner-a", lease.FencingToken, time.Second)
	require.NoError(t, err)
	require.True(t, renewed)

	current, err := store.GetOrchestratorLease(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.True(t, current.LeaseUntil.After(firstUntil), "renewal must extend lease_until")
	require.NotNil(t, current.LastTickAt, "renewal must record a tick heartbeat")
	require.Equal(t, "owner-a", current.OwnerID)

	// A stale token cannot renew.
	renewed, err = store.RenewOrchestratorLease(ctx, teamID, "owner-a", "bogus-token", time.Second)
	require.NoError(t, err)
	require.False(t, renewed)

	// An expired lease cannot renew either.
	expireLease(t, store, teamID)
	renewed, err = store.RenewOrchestratorLease(ctx, teamID, "owner-a", lease.FencingToken, time.Second)
	require.NoError(t, err)
	require.False(t, renewed)
}

// TestOrchestratorReleaseAllowsReacquire verifies that a clean release frees
// the team for the next owner without a restart penalty.
func TestOrchestratorReleaseAllowsReacquire(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)

	leaseA, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-a",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, store.ReleaseOrchestratorLease(ctx, teamID, "owner-a", leaseA.FencingToken))
	released, err := store.GetOrchestratorLease(ctx, teamID)
	require.NoError(t, err)
	require.Nil(t, released, "release must remove the lease row")

	leaseB, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-b",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 0, leaseB.RestartCount, "clean handoff must not count as a restart")
	require.NotEqual(t, leaseA.FencingToken, leaseB.FencingToken)
}

// TestOrchestratorRunWithWakeClaimAndReleaseOnExit verifies the full loop
// contract: the owner loop acquires the lease, claims ready tasks with its
// fencing token, and releases the lease on clean exit.
func TestOrchestratorRunWithWakeClaimAndReleaseOnExit(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:      "mate-a",
		TeamID:  teamID,
		Name:    "mate-a",
		State:   TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "owned",
		Goal:   "run",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	o1 := newOwnerOrchestrator(store, "owner-a", 2*time.Second)
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- o1.RunWithWake(runCtx, teamID, nil) }()
	lease := waitForLease(t, store, teamID, "owner-a")
	require.Equal(t, "owner-a", lease.OwnerID)

	// The loop must claim the ready task under the owner's fencing token.
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := store.GetTask(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, task)
		if task.Status == TaskStatusRunning && task.FencingToken != "" {
			// The claim succeeded while carrying the owner's fencing token;
			// had the token been stale the registry would have refused it with
			// ErrOrchestratorOwnerMismatch and the task would stay ready.
			require.True(t, lease.FencingToken != "", "owner lease must mint a fencing token")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("orchestrator loop did not claim the ready task")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
	after, err := store.GetOrchestratorLease(ctx, teamID)
	require.NoError(t, err)
	require.Nil(t, after, "clean loop exit must release the owner lease")

	// A new owner can acquire immediately after the clean exit.
	_, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-b",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
}

// TestOrchestratorNewOwnerResumesHealthReconcile verifies P5 item 4: after a
// takeover, the new owner's first ticks resume active-task health judgement
// (expired attempt leases are reclaimed) instead of waiting for the old owner.
func TestOrchestratorNewOwnerResumesHealthReconcile(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{Status: TeamStatusActive})
	require.NoError(t, err)
	mateID, err := store.UpsertTeammate(ctx, Teammate{
		ID:      "mate-a",
		TeamID:  teamID,
		Name:    "mate-a",
		State:   TeammateStateBusy,
	})
	require.NoError(t, err)
	// A task whose attempt lease expired while its owner was alive.
	expired := time.Now().UTC().Add(-time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "stale-attempt",
		Goal:       "run",
		Status:     TaskStatusRunning,
		Assignee:   &mateID,
		LeaseUntil: &expired,
	})
	require.NoError(t, err)

	// Old owner acquires the lease, then crashes without releasing.
	_, acquired, err := store.AcquireOrchestratorLease(ctx, OrchestratorLease{
		TeamID:  teamID,
		OwnerID: "owner-a",
	}, 2*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	expireLease(t, store, teamID)

	// New owner takes over and must reclaim the stale attempt.
	o2 := newOwnerOrchestrator(store, "owner-b", 2*time.Second)
	o2.LeaseManager = NewLeaseManager(store, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- o2.RunWithWake(runCtx, teamID, nil) }()
	waitForLease(t, store, teamID, "owner-b")

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := store.GetTask(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, task)
		if task.Status == TaskStatusReclaimPending || task.Status == TaskStatusReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new owner did not resume health judgement; task stuck at %s", task.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
}
