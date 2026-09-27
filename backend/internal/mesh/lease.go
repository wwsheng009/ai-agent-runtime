package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file owns the TTL leases of architecture §3.3 / §4.4: the file that says
// "node X is serving session S right now". Leases are the mesh's only mutual
// exclusion primitive; every operation here is best-effort and a failure
// degrades to "no mutual exclusion" instead of blocking a chat (MN1 / §4.7).

// Lease purposes (architecture §2.1). The purpose prefix keeps `ls leases/`
// readable: `session-<sid>.lock` is ownership, `spawn-<sid>.lock` is
// single-flight process startup.
const (
	LeasePurposeSession = "session"
	LeasePurposeSpawn   = "spawn"
)

const (
	// DefaultLeaseTTLSec is the documented lease TTL (architecture §3.3). The
	// 30s heartbeat renews it, which leaves a three-miss budget before a lease
	// looks stale.
	DefaultLeaseTTLSec = 90
	// DefaultLeaseTTL is DefaultLeaseTTLSec as a duration.
	DefaultLeaseTTL = DefaultLeaseTTLSec * time.Second
	// leaseAcquireAttempts bounds the create/reclaim retry loop: every retry
	// means another process won a race, so more than a couple is pathological.
	leaseAcquireAttempts = 4
	// staleLeaseSuffix names the intermediate file of an atomic reclaim
	// (`rename` then delete). It deliberately does not end in `.lock`, so a
	// leftover from a crash is never mistaken for a live lease.
	staleLeaseSuffix = ".stale-"
	// leaseClaimSuffix names the per-lease mutex file that serializes the
	// create-or-reclaim transition among contenders. Like staleLeaseSuffix it
	// deliberately does not end in `.lock`, so views/listing never mistake a
	// claim for a lease; the file lives only for a few file operations.
	leaseClaimSuffix = ".claim"
	// leaseClaimDeadAfter is the fallback age after which a claim whose holder
	// pid cannot be read is considered abandoned.
	leaseClaimDeadAfter = 30 * time.Second
)

// Reason values reported by LeaseOutcome. They are journal/log vocabulary, not
// wire values.
const (
	LeaseReasonAcquired   = "acquired"
	LeaseReasonHeld       = "held"
	LeaseReasonUnreadable = "unreadable"
	LeaseReasonDegraded   = "degraded"
	LeaseReasonContended  = "contended"
)

// Lease is one `mesh/leases/<purpose>-<key>.lock` file (architecture §3.3,
// schema_version 2).
//
// Exactly one process writes a given lease (its owner) and every write goes
// through a temp file + rename; every other process only reads it.
type Lease struct {
	SchemaVersion int       `json:"schema_version"`
	Purpose       string    `json:"purpose"`
	Key           string    `json:"key"`
	OwnerNodeID   string    `json:"owner_node_id"`
	OwnerPID      int       `json:"owner_pid"`
	AcquiredAt    time.Time `json:"acquired_at"`
	RenewedAt     time.Time `json:"renewed_at"`
	TTLSec        int       `json:"ttl_sec"`
}

// LeaseOwner identifies the process claiming or renewing a lease.
type LeaseOwner struct {
	NodeID string
	PID    int
}

// AcquireOptions tunes one acquisition attempt.
type AcquireOptions struct {
	// Now overrides the clock (zero means NowUTC()).
	Now time.Time
	// TTL is the lease lifetime (zero means DefaultLeaseTTL).
	TTL time.Duration
	// Takeover reclaims a lease that is still live and fresh. It is the
	// explicit `--takeover` path of §4.4; the default never steals.
	Takeover bool
}

// LeaseOutcome reports what Acquire did. Acquired=false with Degraded=false
// means "somebody else holds a usable lease"; Degraded=true means "this process
// runs without mutual exclusion" (no mesh root, unwritable dir, ...).
type LeaseOutcome struct {
	Acquired  bool
	Reclaimed bool
	Holder    *Lease
	Degraded  bool
	Reason    string
	Err       error
}

// LeaseRenewal reports the result of a heartbeat renewal.
type LeaseRenewal struct {
	Lease   Lease
	Renewed bool
	// Lost is true when the lease is gone or owned by another node: the caller
	// no longer owns the resource (architecture §4.4 takeover detection).
	Lost bool
}

// LeaseFile is one tolerant read of a lease file (used by views and GC).
type LeaseFile struct {
	Path  string
	Lease Lease
	// OK is false when the file could not be parsed as a lease of this schema.
	OK bool
}

// TTL returns the lease lifetime as a duration.
func (l Lease) TTL() time.Duration {
	if l.TTLSec <= 0 {
		return DefaultLeaseTTL
	}
	return time.Duration(l.TTLSec) * time.Second
}

// ExpiresAt returns the instant after which the lease is reclaimable on time
// alone. TTL judgement prefers the monotonic-clock difference of a local
// observation (risk R4); a stored wall-clock timestamp is the portable
// fallback, which is what this helper exposes.
func (l Lease) ExpiresAt() time.Time {
	base := l.RenewedAt
	if base.IsZero() {
		base = l.AcquiredAt
	}
	return base.Add(l.TTL())
}

// Expired reports whether the lease ran past its TTL.
func (l Lease) Expired(now time.Time) bool {
	return !now.Before(l.ExpiresAt())
}

// Stale reports whether the lease may be reclaimed: the owner process is gone
// **or** the TTL lapsed (architecture §3.3).
func (l Lease) Stale(now time.Time) bool {
	if !processAlive(l.OwnerPID) {
		return true
	}
	return l.Expired(now)
}

// OwnedBy reports whether owner is the writer of this lease. Node id is the
// identity (one process = one node id per start); pid is only a fallback for
// records written without one.
func (l Lease) OwnedBy(owner LeaseOwner) bool {
	owner.NodeID = strings.TrimSpace(owner.NodeID)
	if owner.NodeID != "" && strings.TrimSpace(l.OwnerNodeID) != "" {
		return l.OwnerNodeID == owner.NodeID
	}
	return owner.PID > 0 && l.OwnerPID == owner.PID
}

// Acquire tries to take `purpose-key` for owner.
//
// The algorithm is the documented create-or-reclaim loop: an exclusive create
// (`O_CREATE|O_EXCL`) wins outright; an existing lease is read and, when its
// owner is dead or its TTL lapsed, reclaimed with an atomic rename before the
// create is retried. Every attempt runs under a per-lease claim file that
// serializes contenders, so the read-decide-rename-create sequence cannot
// interleave into two winners. A live, fresh lease is never stolen unless
// Takeover is set.
func Acquire(paths Paths, purpose, key string, owner LeaseOwner, opts AcquireOptions) LeaseOutcome {
	if opts.Now.IsZero() {
		opts.Now = NowUTC()
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultLeaseTTL
	}
	path := paths.LeasePath(purpose, key)
	if path == "" || strings.TrimSpace(owner.NodeID) == "" || owner.PID <= 0 {
		// Fail-closed: no mesh root (or an unusable owner identity) means "no
		// mutual exclusion", never a CWD-relative fallback and never a lease
		// file without an owner to reclaim it.
		return LeaseOutcome{Reason: LeaseReasonDegraded, Degraded: true}
	}
	lease := Lease{
		SchemaVersion: SchemaVersion,
		Purpose:       purpose,
		Key:           key,
		OwnerNodeID:   owner.NodeID,
		OwnerPID:      owner.PID,
		AcquiredAt:    opts.Now,
		RenewedAt:     opts.Now,
		TTLSec:        int(opts.TTL / time.Second),
	}
	reclaimed := false
	for attempt := 0; attempt < leaseAcquireAttempts; attempt++ {
		release, claimErr := acquireLeaseClaim(path)
		if claimErr != nil {
			return LeaseOutcome{Reason: LeaseReasonDegraded, Degraded: true, Err: claimErr}
		}
		if release == nil {
			// Another process is mid-create-or-reclaim for this path. The
			// holder only publishes complete lease files, so a visible fresh
			// lease is the answer; otherwise back off and retry.
			if existing, ok := ReadLease(path); ok && !opts.Takeover && !existing.Stale(opts.Now) {
				holder := existing
				return LeaseOutcome{Reason: LeaseReasonHeld, Holder: &holder}
			}
			time.Sleep(time.Duration(attempt+1) * time.Millisecond)
			continue
		}
		err := createLeaseFile(path, lease)
		if err == nil {
			release()
			return LeaseOutcome{Acquired: true, Reclaimed: reclaimed, Reason: LeaseReasonAcquired}
		}
		if !errors.Is(err, fs.ErrExist) {
			release()
			return LeaseOutcome{Reason: LeaseReasonDegraded, Degraded: true, Err: err}
		}
		existing, ok := ReadLease(path)
		if !ok {
			// The file exists but is not a lease this build understands. The
			// conservative answer is "somebody holds it": readers never rewrite
			// what they cannot parse (architecture §3.6).
			release()
			return LeaseOutcome{Reason: LeaseReasonUnreadable}
		}
		if existing.OwnedBy(owner) {
			// Same node re-acquiring (resume of the same session inside one
			// process): keep the original acquisition time, refresh the rest.
			lease.AcquiredAt = existing.AcquiredAt
			if err := writeLeaseFile(path, lease); err != nil {
				release()
				return LeaseOutcome{Reason: LeaseReasonDegraded, Degraded: true, Err: err}
			}
			release()
			return LeaseOutcome{Acquired: true, Reclaimed: reclaimed, Reason: LeaseReasonAcquired}
		}
		if !opts.Takeover && !existing.Stale(opts.Now) {
			holder := existing
			release()
			return LeaseOutcome{Reason: LeaseReasonHeld, Holder: &holder}
		}
		if !reclaimLease(path, opts.Now) {
			// Another process reclaimed it first: retry the exclusive create.
			release()
			continue
		}
		reclaimed = true
		// Publish the new lease while still holding the claim: no contender can
		// slip a create into the window between reclaim and create.
		if err := createLeaseFile(path, lease); err != nil {
			release()
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return LeaseOutcome{Reason: LeaseReasonDegraded, Degraded: true, Err: err}
		}
		release()
		return LeaseOutcome{Acquired: true, Reclaimed: true, Reason: LeaseReasonAcquired}
	}
	return LeaseOutcome{Reason: LeaseReasonContended}
}

// Renew refreshes renewed_at of a lease this process still owns. It is called
// from the heartbeat (architecture §4.1) and never creates a lease.
func Renew(paths Paths, purpose, key string, owner LeaseOwner, now time.Time, ttl time.Duration) LeaseRenewal {
	if now.IsZero() {
		now = NowUTC()
	}
	path := paths.LeasePath(purpose, key)
	if path == "" {
		return LeaseRenewal{Lost: true}
	}
	current, ok := ReadLease(path)
	if !ok || !current.OwnedBy(owner) {
		return LeaseRenewal{Lost: true}
	}
	current.RenewedAt = now
	if ttl > 0 {
		current.TTLSec = int(ttl / time.Second)
	}
	if err := writeLeaseFile(path, current); err != nil {
		return LeaseRenewal{Lease: current}
	}
	return LeaseRenewal{Lease: current, Renewed: true}
}

// Release deletes the lease when (and only when) it is still owned by owner.
//
// It returns true when no lease file remains afterwards — including the "it was
// already gone" case, which must stay quiet on double-exit paths. A lease owned
// by somebody else is left alone.
func Release(paths Paths, purpose, key string, owner LeaseOwner) bool {
	path := paths.LeasePath(purpose, key)
	if path == "" {
		return false
	}
	current, ok := ReadLease(path)
	if !ok {
		// Either no lease at all (fine) or one we must not touch.
		_, statErr := os.Stat(path)
		return errors.Is(statErr, fs.ErrNotExist)
	}
	if !current.OwnedBy(owner) {
		return false
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

// ReadLease reads one lease file tolerantly: a missing, unreadable, malformed
// or unknown-schema file yields ok=false. Callers treat "no lease" as
// "reclaimable" but must never rewrite a file they could not read.
func ReadLease(path string) (Lease, bool) {
	if strings.TrimSpace(path) == "" {
		return Lease{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Lease{}, false
	}
	var lease Lease
	if err := json.Unmarshal(data, &lease); err != nil {
		return Lease{}, false
	}
	if lease.SchemaVersion != SchemaVersion {
		return Lease{}, false
	}
	if lease.AcquiredAt.IsZero() && lease.RenewedAt.IsZero() {
		return Lease{}, false
	}
	return lease, true
}

// ListLeases lists every `*.lock` file under paths.Leases, sorted by path.
// Leftover `.stale-<ts>` files from an interrupted reclaim are ignored.
func ListLeases(paths Paths) []LeaseFile {
	if !paths.Enabled() {
		return nil
	}
	entries, err := os.ReadDir(paths.Leases)
	if err != nil {
		return nil
	}
	files := make([]LeaseFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		path := filepath.Join(paths.Leases, entry.Name())
		lease, ok := ReadLease(path)
		files = append(files, LeaseFile{Path: path, Lease: lease, OK: ok})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// createLeaseFile performs the exclusive create of §3.3. It is the only place
// that uses O_EXCL: everything else goes through the atomic rewrite.
func createLeaseFile(path string, lease Lease) error {
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return fmt.Errorf("mesh: encode lease: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mesh: create lease dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("mesh: write lease %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("mesh: sync lease %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("mesh: close lease %s: %w", path, err)
	}
	return nil
}

// writeLeaseFile replaces an existing lease atomically.
func writeLeaseFile(path string, lease Lease) error {
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return fmt.Errorf("mesh: encode lease: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}

// reclaimLease removes a dead or expired lease with the atomic rename-then-
// delete dance of §3.3: among competing processes exactly one rename wins, and
// only that one may proceed to acquire.
func reclaimLease(path string, now time.Time) bool {
	stalePath := fmt.Sprintf("%s%s%d", path, staleLeaseSuffix, now.UnixNano())
	if err := os.Rename(path, stalePath); err != nil {
		return false
	}
	_ = os.Remove(stalePath)
	return true
}

// acquireLeaseClaim takes the exclusive per-lease claim that serializes the
// create-or-reclaim transition. It returns a release func and nil error when
// the claim is held; (nil, nil) when a live contender holds it (the caller
// should back off); and (nil, err) for an unusable lease directory.
//
// Reclaim is a read-decide-rename-create sequence, which is not atomic on its
// own: without serialization a delayed contender can decide to reclaim the
// stale lease it read earlier, then rename away and delete the fresh lease a
// faster contender just published, and finally create its own - two winners
// for one key. The claim closes that window. It is best-effort like every
// other lease primitive: a claim whose holder died is detected by pid (or,
// failing that, by age) and removed.
func acquireLeaseClaim(path string) (func(), error) {
	claim := path + leaseClaimSuffix
	if err := os.MkdirAll(filepath.Dir(claim), 0o700); err != nil {
		return nil, err
	}
	create := func() (func(), error) {
		file, err := os.OpenFile(claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(claim)
			if writeErr != nil {
				return nil, writeErr
			}
			return nil, closeErr
		}
		return func() { _ = os.Remove(claim) }, nil
	}
	release, err := create()
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return release, err
	}
	if !leaseClaimHolderDead(claim) {
		return nil, nil
	}
	if err := os.Remove(claim); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	release, err = create()
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return release, nil
}

// leaseClaimHolderDead reports whether a claim file may be removed: either the
// process that created it is gone, or the claim is older than the age bound.
// A live holder keeps the claim for a few file operations only, far below the
// threshold, so the age bound only ever reclaims a leaked claim (for example
// when removing it failed).
func leaseClaimHolderDead(claim string) bool {
	expired := false
	if info, err := os.Stat(claim); err == nil {
		expired = time.Since(info.ModTime()) > leaseClaimDeadAfter
	}
	if data, err := os.ReadFile(claim); err == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
			return !processAlive(pid) || expired
		}
	}
	return expired
}
