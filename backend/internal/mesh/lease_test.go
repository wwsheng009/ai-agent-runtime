package mesh

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadPID returns a pid that is guaranteed not to be running any more: it runs
// the test binary once with no matching test and reaps the child.
//
// Windows keeps the process object (and with it the pid) alive while any handle
// to it is still open, so a reaped child can briefly still answer OpenProcess.
// Wait that out instead of handing a "maybe alive" pid to the caller.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("throwaway pid %d still looks alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pid
}

// writeRawLease plants a lease file directly, bypassing Acquire (tests need to
// create dead / expired / foreign leases that Acquire would never write).
func writeRawLease(t *testing.T, paths Paths, lease Lease) string {
	t.Helper()
	lease.SchemaVersion = SchemaVersion
	path := paths.LeasePath(lease.Purpose, lease.Key)
	if path == "" {
		t.Fatalf("no lease path for %s/%s", lease.Purpose, lease.Key)
	}
	if err := writeLeaseFile(path, lease); err != nil {
		t.Fatalf("write lease: %v", err)
	}
	return path
}

func staleLeftovers(t *testing.T, paths Paths) []string {
	t.Helper()
	entries, err := os.ReadDir(paths.Leases)
	if err != nil {
		t.Fatalf("read leases dir: %v", err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), staleLeaseSuffix) {
			leftovers = append(leftovers, entry.Name())
		}
	}
	return leftovers
}

func TestLeaseAcquireIsExclusive(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	owner := LeaseOwner{NodeID: "node-a", PID: os.Getpid()}

	first := Acquire(paths, LeasePurposeSession, "session_a", owner, AcquireOptions{Now: now})
	if !first.Acquired || first.Reason != LeaseReasonAcquired {
		t.Fatalf("first acquire = %+v, want acquired", first)
	}
	if first.Reclaimed {
		t.Fatalf("first acquire must not report a reclaim: %+v", first)
	}

	second := Acquire(paths, LeasePurposeSession, "session_a", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now})
	if second.Acquired {
		t.Fatalf("second acquire must not win a live lease: %+v", second)
	}
	if second.Reason != LeaseReasonHeld || second.Degraded {
		t.Fatalf("second acquire reason = %q degraded=%v, want held", second.Reason, second.Degraded)
	}
	if second.Holder == nil || second.Holder.OwnerNodeID != "node-a" {
		t.Fatalf("holder = %+v, want node-a", second.Holder)
	}

	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_a"))
	if !ok {
		t.Fatal("lease file is not readable")
	}
	if lease.SchemaVersion != SchemaVersion || lease.Purpose != LeasePurposeSession || lease.Key != "session_a" {
		t.Fatalf("lease header = %+v", lease)
	}
	if lease.OwnerNodeID != "node-a" || lease.OwnerPID != os.Getpid() {
		t.Fatalf("lease owner = %s/%d", lease.OwnerNodeID, lease.OwnerPID)
	}
	if lease.TTLSec != DefaultLeaseTTLSec {
		t.Fatalf("ttl_sec = %d, want %d", lease.TTLSec, DefaultLeaseTTLSec)
	}
}

func TestLeaseAcquireReclaimsDeadOwner(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_dead",
		OwnerNodeID: "node-dead",
		OwnerPID:    deadPID(t),
		AcquiredAt:  now.Add(-time.Minute),
		RenewedAt:   now,
		TTLSec:      DefaultLeaseTTLSec,
	})

	outcome := Acquire(paths, LeasePurposeSession, "session_dead", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now})
	if !outcome.Acquired || !outcome.Reclaimed {
		t.Fatalf("acquire over a dead owner = %+v, want acquired+reclaimed", outcome)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_dead"))
	if !ok || lease.OwnerNodeID != "node-b" {
		t.Fatalf("lease after reclaim = %+v ok=%v", lease, ok)
	}
	if leftovers := staleLeftovers(t, paths); len(leftovers) != 0 {
		t.Fatalf("reclaim left files behind: %v", leftovers)
	}
}

func TestLeaseAcquireReclaimsOnlyAfterTTL(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	// The owner pid is alive (this test process); only the TTL decides.
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_ttl",
		OwnerNodeID: "node-a",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  now.Add(-2 * time.Minute),
		RenewedAt:   now.Add(-89 * time.Second),
		TTLSec:      DefaultLeaseTTLSec,
	})
	fresh := Acquire(paths, LeasePurposeSession, "session_ttl", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now})
	if fresh.Acquired {
		t.Fatalf("a lease inside its TTL must not be reclaimed: %+v", fresh)
	}
	expired := Acquire(paths, LeasePurposeSession, "session_ttl", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now.Add(2 * time.Second)})
	if !expired.Acquired || !expired.Reclaimed {
		t.Fatalf("a lease past its TTL must be reclaimed: %+v", expired)
	}
}

func TestLeaseTakeoverReclaimsLiveLease(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_takeover",
		OwnerNodeID: "node-a",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  now,
		RenewedAt:   now,
		TTLSec:      DefaultLeaseTTLSec,
	})
	refused := Acquire(paths, LeasePurposeSession, "session_takeover", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now})
	if refused.Acquired {
		t.Fatalf("a live lease must not be stolen by default: %+v", refused)
	}
	taken := Acquire(paths, LeasePurposeSession, "session_takeover", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: now, Takeover: true})
	if !taken.Acquired || !taken.Reclaimed {
		t.Fatalf("--takeover must reclaim a live lease: %+v", taken)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_takeover"))
	if !ok || lease.OwnerNodeID != "node-b" {
		t.Fatalf("lease after takeover = %+v ok=%v", lease, ok)
	}
}

func TestLeaseAcquireDegradesWithoutMeshRoot(t *testing.T) {
	outcome := Acquire(Paths{}, LeasePurposeSession, "session_x", LeaseOwner{NodeID: "node-a", PID: os.Getpid()}, AcquireOptions{})
	if outcome.Acquired || !outcome.Degraded || outcome.Reason != LeaseReasonDegraded {
		t.Fatalf("fail-closed acquire = %+v, want degraded", outcome)
	}
	if outcome.Err != nil {
		t.Fatalf("a missing mesh root is not an error: %v", outcome.Err)
	}
	// An owner without identity is equally unusable: never write an
	// unclaimable lease.
	paths := testPaths(t)
	anonymous := Acquire(paths, LeasePurposeSession, "session_x", LeaseOwner{PID: os.Getpid()}, AcquireOptions{})
	if anonymous.Acquired || !anonymous.Degraded {
		t.Fatalf("acquire without node id = %+v, want degraded", anonymous)
	}
	if _, err := os.Stat(paths.LeasePath(LeasePurposeSession, "session_x")); !os.IsNotExist(err) {
		t.Fatalf("no lease file may be written without an owner: %v", err)
	}
}

func TestLeaseAcquireLeavesUnreadableFileAlone(t *testing.T) {
	paths := testPaths(t)
	path := paths.LeasePath(LeasePurposeSession, "session_corrupt")
	if err := os.MkdirAll(paths.Leases, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	garbage := []byte("{ not a lease\n")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("seed corrupt lease: %v", err)
	}
	outcome := Acquire(paths, LeasePurposeSession, "session_corrupt", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}, AcquireOptions{Now: NowUTC(), Takeover: true})
	if outcome.Acquired {
		t.Fatalf("an unparsable lease must not be reclaimed: %+v", outcome)
	}
	if outcome.Reason != LeaseReasonUnreadable {
		t.Fatalf("reason = %q, want %q", outcome.Reason, LeaseReasonUnreadable)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(garbage) {
		t.Fatalf("readers must never rewrite an unparsable lease: %q err=%v", after, err)
	}
}

func TestLeaseRenewRefreshesAndDetectsLoss(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	owner := LeaseOwner{NodeID: "node-a", PID: os.Getpid()}
	if outcome := Acquire(paths, LeasePurposeSession, "session_renew", owner, AcquireOptions{Now: now}); !outcome.Acquired {
		t.Fatalf("acquire: %+v", outcome)
	}

	later := now.Add(30 * time.Second)
	renewed := Renew(paths, LeasePurposeSession, "session_renew", owner, later, DefaultLeaseTTL)
	if !renewed.Renewed || renewed.Lost {
		t.Fatalf("renew = %+v", renewed)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_renew"))
	if !ok || !lease.RenewedAt.Equal(later) {
		t.Fatalf("renewed_at = %v, want %v", lease.RenewedAt, later)
	}
	if !lease.AcquiredAt.Equal(now) {
		t.Fatalf("renew must keep acquired_at: %v", lease.AcquiredAt)
	}

	// Another node takes the lease over: the next renewal must notice.
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_renew",
		OwnerNodeID: "node-b",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  later,
		RenewedAt:   later,
		TTLSec:      DefaultLeaseTTLSec,
	})
	lost := Renew(paths, LeasePurposeSession, "session_renew", owner, later.Add(30*time.Second), DefaultLeaseTTL)
	if !lost.Lost || lost.Renewed {
		t.Fatalf("renew after takeover = %+v, want lost", lost)
	}
	// Renew never creates a lease out of thin air.
	missing := Renew(paths, LeasePurposeSession, "session_absent", owner, later, DefaultLeaseTTL)
	if !missing.Lost {
		t.Fatalf("renew of a missing lease = %+v, want lost", missing)
	}
}

// TestRenewAfterTakeoverNeverClobbers 钉住 2026-10-14 的 M1 修复：续约在
// `.claim` 锁内重读校验，租约被显式接管后 Renew 必须报 Lost，并且**绝不**
// 用陈旧内容覆盖新持有者写下的租约（修复前是"读→rename 覆盖"的裸写）。
func TestRenewAfterTakeoverNeverClobbers(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	ownerA := LeaseOwner{NodeID: "node-a", PID: os.Getpid()}
	ownerB := LeaseOwner{NodeID: "node-b", PID: os.Getpid()}

	if outcome := Acquire(paths, LeasePurposeSession, "session_m1", ownerA, AcquireOptions{Now: now}); !outcome.Acquired {
		t.Fatalf("acquire A: %+v", outcome)
	}
	// B 显式接管（模拟回收路径）：接管之后 A 的续约不得覆盖 B。
	if outcome := Acquire(paths, LeasePurposeSession, "session_m1", ownerB, AcquireOptions{Now: now.Add(time.Second), TTL: time.Minute, Takeover: true}); !outcome.Acquired {
		t.Fatalf("takeover B: %+v", outcome)
	}
	lost := Renew(paths, LeasePurposeSession, "session_m1", ownerA, now.Add(2*time.Second), DefaultLeaseTTL)
	if !lost.Lost || lost.Renewed {
		t.Fatalf("renew by stale owner = %+v, want lost", lost)
	}
	stored, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_m1"))
	if !ok {
		t.Fatal("lease file missing after takeover")
	}
	if stored.OwnerNodeID != "node-b" {
		t.Fatalf("stale renew clobbered the new owner: owner=%q, want node-b", stored.OwnerNodeID)
	}
	if renewed := Renew(paths, LeasePurposeSession, "session_m1", ownerB, now.Add(3*time.Second), time.Minute); !renewed.Renewed || renewed.Lost {
		t.Fatalf("new owner must still renew: %+v", renewed)
	}
}

func TestLeaseReleaseOnlyWhenOwned(t *testing.T) {
	paths := testPaths(t)
	owner := LeaseOwner{NodeID: "node-a", PID: os.Getpid()}
	if outcome := Acquire(paths, LeasePurposeSession, "session_release", owner, AcquireOptions{Now: NowUTC()}); !outcome.Acquired {
		t.Fatalf("acquire: %+v", outcome)
	}
	path := paths.LeasePath(LeasePurposeSession, "session_release")

	if Release(paths, LeasePurposeSession, "session_release", LeaseOwner{NodeID: "node-b", PID: os.Getpid()}) {
		t.Fatal("a foreign owner must not release the lease")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lease must survive a foreign release: %v", err)
	}
	if !Release(paths, LeasePurposeSession, "session_release", owner) {
		t.Fatal("the owner must be able to release")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lease file must be gone: %v", err)
	}
	if !Release(paths, LeasePurposeSession, "session_release", owner) {
		t.Fatal("releasing an already gone lease must stay quiet")
	}
}

func TestLeaseConcurrentAcquireHasSingleWinner(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	// Seed a reclaimable lease so every contender starts from the same race:
	// all of them must reclaim before they can create.
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_race",
		OwnerNodeID: "node-dead",
		OwnerPID:    deadPID(t),
		AcquiredAt:  now.Add(-time.Hour),
		RenewedAt:   now.Add(-time.Hour),
		TTLSec:      DefaultLeaseTTLSec,
	})

	const workers = 8
	results := make([]LeaseOutcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index] = Acquire(paths, LeasePurposeSession, "session_race",
				LeaseOwner{NodeID: fmt.Sprintf("node-%d", index), PID: os.Getpid()},
				AcquireOptions{Now: now})
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, outcome := range results {
		if outcome.Acquired {
			winners++
		}
		if outcome.Degraded {
			t.Fatalf("no contender may degrade here: %+v", outcome)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (%+v)", winners, results)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_race"))
	if !ok {
		t.Fatal("the winning lease must be readable")
	}
	if !strings.HasPrefix(lease.OwnerNodeID, "node-") || lease.OwnerPID != os.Getpid() {
		t.Fatalf("surviving lease owner = %s/%d", lease.OwnerNodeID, lease.OwnerPID)
	}
	if leftovers := staleLeftovers(t, paths); len(leftovers) != 0 {
		t.Fatalf("racing reclaims left files behind: %v", leftovers)
	}
}

func TestLeaseListIgnoresStaleLeftovers(t *testing.T) {
	paths := testPaths(t)
	if outcome := Acquire(paths, LeasePurposeSession, "session_listed", LeaseOwner{NodeID: "node-a", PID: os.Getpid()}, AcquireOptions{Now: NowUTC()}); !outcome.Acquired {
		t.Fatalf("acquire: %+v", outcome)
	}
	leftover := paths.LeasePath(LeasePurposeSession, "session_listed") + staleLeaseSuffix + "1"
	if err := os.WriteFile(leftover, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}
	files := ListLeases(paths)
	if len(files) != 1 {
		t.Fatalf("ListLeases = %+v, want exactly the .lock file", files)
	}
	if !files[0].OK || files[0].Lease.Key != "session_listed" {
		t.Fatalf("listed lease = %+v", files[0])
	}
}

func TestHostSessionLeaseLifecycle(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{Now: clock.Now, HeartbeatInterval: time.Hour})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()

	sessionID := "session_lease_lifecycle"
	host.SetSession(&SessionInfo{ID: sessionID, Title: "lease"})
	status := host.SessionLeaseStatus()
	if !status.Held || status.Lost || status.Key != sessionID || status.Purpose != LeasePurposeSession {
		t.Fatalf("lease status after activation = %+v", status)
	}
	path := paths.LeasePath(LeasePurposeSession, sessionID)
	lease, ok := ReadLease(path)
	if !ok || lease.OwnerNodeID != host.NodeID() {
		t.Fatalf("lease file = %+v ok=%v, want owner %s", lease, ok, host.NodeID())
	}

	clock.Advance(30 * time.Second)
	host.heartbeatOnce()
	renewed, ok := ReadLease(path)
	if !ok || !renewed.RenewedAt.Equal(clock.Now()) {
		t.Fatalf("renewed_at = %v, want %v", renewed.RenewedAt, clock.Now())
	}

	host.SetSession(nil)
	if status := host.SessionLeaseStatus(); status.Held || status.Lost {
		t.Fatalf("lease status after deactivation = %+v", status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lease must be released on deactivation: %v", err)
	}
	kinds := journalKinds(t, paths, host.NodeID())
	if countKind(kinds, JournalLeaseAcquired) != 1 || countKind(kinds, JournalLeaseReleased) != 1 {
		t.Fatalf("journal = %v, want one acquire and one release", kinds)
	}
}

func TestHostSessionLeaseSwitchesWithSession(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{Now: clock.Now, HeartbeatInterval: time.Hour})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()

	host.SetSession(&SessionInfo{ID: "session_one", Title: "one"})
	host.SetSession(&SessionInfo{ID: "session_two", Title: "two"})
	if _, err := os.Stat(paths.LeasePath(LeasePurposeSession, "session_one")); !os.IsNotExist(err) {
		t.Fatalf("the previous session lease must be released: %v", err)
	}
	if status := host.SessionLeaseStatus(); !status.Held || status.Key != "session_two" {
		t.Fatalf("lease status = %+v, want session_two", status)
	}
	// Re-publishing the same session must not churn the lease.
	before, _ := ReadLease(paths.LeasePath(LeasePurposeSession, "session_two"))
	host.SetSession(&SessionInfo{ID: "session_two", Title: "two renamed"})
	after, _ := ReadLease(paths.LeasePath(LeasePurposeSession, "session_two"))
	if !before.AcquiredAt.Equal(after.AcquiredAt) {
		t.Fatalf("acquired_at changed on a title refresh: %v -> %v", before.AcquiredAt, after.AcquiredAt)
	}
}

func TestHostLeaseHeldByPeerDegradesWithoutStealing(t *testing.T) {
	paths := testMeshPaths(t)
	sessionID := "session_peer_held"
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         sessionID,
		OwnerNodeID: "node-peer",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  NowUTC(),
		RenewedAt:   NowUTC(),
		TTLSec:      DefaultLeaseTTLSec,
	})
	host := NewHost(HostConfig{Now: NowUTC, HeartbeatInterval: time.Hour})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()

	host.SetSession(&SessionInfo{ID: sessionID, Title: "contended"})
	if status := host.SessionLeaseStatus(); status.Held || status.Lost {
		t.Fatalf("a refused lease must not look held or lost: %+v", status)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID))
	if !ok || lease.OwnerNodeID != "node-peer" {
		t.Fatalf("the peer lease must survive untouched: %+v", lease)
	}
	if kinds := journalKinds(t, paths, host.NodeID()); countKind(kinds, JournalLeaseDegraded) != 1 {
		t.Fatalf("journal = %v, want one lease.degraded", kinds)
	}
}

func TestHostNoticesLeaseTakeover(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{Now: clock.Now, HeartbeatInterval: time.Hour})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()

	sessionID := "session_taken_over"
	host.SetSession(&SessionInfo{ID: sessionID, Title: "mine"})
	// Another node takes the lease over (§4.4): the next heartbeat must notice
	// and stop pretending to own the session.
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         sessionID,
		OwnerNodeID: "node-thief",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  clock.Now(),
		RenewedAt:   clock.Now(),
		TTLSec:      DefaultLeaseTTLSec,
	})
	clock.Advance(30 * time.Second)
	host.heartbeatOnce()

	status := host.SessionLeaseStatus()
	if status.Held || !status.Lost {
		t.Fatalf("lease status after takeover = %+v, want lost", status)
	}
	kinds := journalKinds(t, paths, host.NodeID())
	if countKind(kinds, JournalLeaseDegraded) != 1 {
		t.Fatalf("journal = %v, want one lease.degraded", kinds)
	}
	lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID))
	if !ok || lease.OwnerNodeID != "node-thief" {
		t.Fatalf("the thief's lease must survive: %+v", lease)
	}
	// A later heartbeat must not resurrect the lease.
	clock.Advance(30 * time.Second)
	host.heartbeatOnce()
	if renewed, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID)); !ok || !renewed.RenewedAt.Equal(clock.Now().Add(-60*time.Second)) {
		t.Fatalf("a lost lease must not be renewed: %+v", renewed)
	}
}

// TestAcquireAssignsMonotonicEpoch 钉住 M2（fencing token）：新建 = 1；续约
// 保持不变；接管他人租约 = 旧值 + 1（下游据此判定"我的任期是否已被接管"）。
func TestAcquireAssignsMonotonicEpoch(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	owner := LeaseOwner{NodeID: "node-epoch", PID: os.Getpid()}
	other := LeaseOwner{NodeID: "node-epoch-b", PID: os.Getpid()}

	first := Acquire(paths, LeasePurposeSession, "session_epoch", owner, AcquireOptions{Now: now})
	if !first.Acquired || first.Epoch != 1 {
		t.Fatalf("first acquire = %+v, want acquired with epoch 1", first)
	}
	if stored, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_epoch")); !ok || stored.Epoch != 1 {
		t.Fatalf("stored lease = %+v ok=%v, want epoch 1", stored, ok)
	}
	// 续约保持 epoch（同一任期）。
	renewed := Renew(paths, LeasePurposeSession, "session_epoch", owner, now.Add(10*time.Second), time.Minute)
	if !renewed.Renewed || renewed.Lease.Epoch != 1 {
		t.Fatalf("renew = %+v, want renewed with epoch 1", renewed)
	}
	// 接管（显式 takeover）→ 旧值 + 1。
	takeover := Acquire(paths, LeasePurposeSession, "session_epoch", other, AcquireOptions{Now: now.Add(20 * time.Second), TTL: time.Minute, Takeover: true})
	if !takeover.Acquired || takeover.Epoch != 2 {
		t.Fatalf("takeover = %+v, want acquired with epoch 2", takeover)
	}
	// 再接管回原 owner → 3（单调不减）。
	back := Acquire(paths, LeasePurposeSession, "session_epoch", owner, AcquireOptions{Now: now.Add(30 * time.Second), TTL: time.Minute, Takeover: true})
	if !back.Acquired || back.Epoch != 3 {
		t.Fatalf("second takeover = %+v, want acquired with epoch 3", back)
	}
}

// TestAcquireEpochAfterReleaseRestarts 记录一个刻意的边界：release 表示旧持有者
// 已明确放弃，无重叠任期需要栅栏，因此重新竞争从 1 起（fencing 只需覆盖"接管"）。
func TestAcquireEpochAfterReleaseRestarts(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	owner := LeaseOwner{NodeID: "node-epoch-release", PID: os.Getpid()}
	if outcome := Acquire(paths, LeasePurposeSession, "session_epoch_rel", owner, AcquireOptions{Now: now}); !outcome.Acquired || outcome.Epoch != 1 {
		t.Fatalf("acquire = %+v", outcome)
	}
	Release(paths, LeasePurposeSession, "session_epoch_rel", owner)
	again := Acquire(paths, LeasePurposeSession, "session_epoch_rel", owner, AcquireOptions{Now: now.Add(time.Second)})
	if !again.Acquired || again.Epoch != 1 {
		t.Fatalf("acquire after release = %+v, want epoch 1", again)
	}
}

// TestAcquireLegacyLeaseEpoch 钉住兼容面：旧文件无 epoch 字段 → 读到 0（不可
// 判定），接管后从 1 起，不因缺字段拒绝读取。
func TestAcquireLegacyLeaseEpoch(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_epoch_legacy",
		OwnerNodeID: "node-legacy",
		OwnerPID:    os.Getpid(),
		AcquiredAt:  now.Add(-time.Minute),
		RenewedAt:   now.Add(-time.Minute),
		TTLSec:      90,
	})
	legacy, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "session_epoch_legacy"))
	if !ok || legacy.Epoch != 0 {
		t.Fatalf("legacy lease = %+v ok=%v, want epoch 0", legacy, ok)
	}
	taker := LeaseOwner{NodeID: "node-epoch-taker", PID: os.Getpid()}
	outcome := Acquire(paths, LeasePurposeSession, "session_epoch_legacy", taker, AcquireOptions{Now: now, TTL: time.Minute, Takeover: true})
	if !outcome.Acquired || outcome.Epoch != 1 {
		t.Fatalf("takeover of legacy lease = %+v, want epoch 1", outcome)
	}
}
