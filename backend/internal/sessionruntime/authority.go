package sessionruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AuthorityKind string

const (
	AuthorityUnowned       AuthorityKind = "unowned"
	AuthorityRuntimeServer AuthorityKind = "runtime-server"
	AuthorityAICLILocal    AuthorityKind = "aicli-local"
	AuthorityScheduled     AuthorityKind = "scheduled"
	AuthorityExternal      AuthorityKind = "external"
)

// AuthorityForOwnerKind maps the durable lease owner kind to the execution
// authority it represents. It intentionally does not infer authority from a
// store path: the live lease holder is the execution authority.
func AuthorityForOwnerKind(ownerKind string) AuthorityKind {
	switch strings.TrimSpace(ownerKind) {
	case "":
		return AuthorityUnowned
	case "runtime-server-actor", "runtime-server-agent-chat":
		return AuthorityRuntimeServer
	case "aicli-actor":
		return AuthorityAICLILocal
	case "scheduled-daemon":
		return AuthorityScheduled
	default:
		return AuthorityExternal
	}
}

// StoreID returns a stable, non-secret identifier for a runtime/event/lease
// store. Stores exposing Path() are canonicalized to an absolute sqlite path;
// everything else is identified as a process-local memory store.
func StoreID(store any) string {
	if store == nil {
		return ""
	}
	if pathProvider, ok := store.(interface{ Path() string }); ok {
		if path := strings.TrimSpace(pathProvider.Path()); path != "" {
			return SQLiteStoreID(path)
		}
	}
	return fmt.Sprintf("memory:%d:%T", os.Getpid(), store)
}

func SQLiteStoreID(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	return "sqlite:" + filepath.ToSlash(path)
}

func StoreDurable(store any) bool {
	if store == nil {
		return false
	}
	pathProvider, ok := store.(interface{ Path() string })
	return ok && strings.TrimSpace(pathProvider.Path()) != ""
}

// AuthorityInfo is the shared diagnostics shape emitted by runtime-server and
// aicli. LeaseOwner* fields are empty when no live lease exists.
type AuthorityInfo struct {
	ExecutionAuthority  AuthorityKind `json:"execution_authority"`
	Authoritative       bool          `json:"authoritative"`
	AuthoritySource     string        `json:"authority_source"`
	AuthorityDomain     string        `json:"authority_domain,omitempty"`
	RuntimeStoreID      string        `json:"runtime_store_id,omitempty"`
	EventStoreID        string        `json:"event_store_id,omitempty"`
	LeaseStoreID        string        `json:"lease_store_id,omitempty"`
	RuntimeStoreDurable bool          `json:"runtime_store_durable"`
	EventStoreDurable   bool          `json:"event_store_durable"`
	LeaseOwnerID        string        `json:"lease_owner_id,omitempty"`
	LeaseOwnerKind      string        `json:"lease_owner_kind,omitempty"`
	LeaseOwnerPID       int           `json:"lease_owner_pid,omitempty"`
	LeaseOwnerHostname  string        `json:"lease_owner_hostname,omitempty"`
	LeaseExpiresAt      *time.Time    `json:"lease_expires_at,omitempty"`
}

type AuthorityInput struct {
	LocalAuthority AuthorityKind
	OwnerKind      string
	OwnerID        string
	PID            int
	Hostname       string
	ExpiresAt      time.Time
	Now            time.Time
	RuntimeStore   any
	EventStore     any
	LeaseStore     any
}

func BuildAuthorityInfo(input AuthorityInput) AuthorityInfo {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ownerKind := strings.TrimSpace(input.OwnerKind)
	expiresAt := input.ExpiresAt
	liveLease := ownerKind != "" && !expiresAt.IsZero() && now.Before(expiresAt)
	executionAuthority := AuthorityUnowned
	if liveLease {
		executionAuthority = AuthorityForOwnerKind(ownerKind)
	}
	localAuthority := input.LocalAuthority
	if localAuthority == "" {
		localAuthority = AuthorityExternal
	}
	authoritative := !liveLease || executionAuthority == localAuthority
	runtimeStoreID := StoreID(input.RuntimeStore)
	eventStoreID := StoreID(input.EventStore)
	leaseStoreID := StoreID(input.LeaseStore)
	if leaseStoreID == "" {
		leaseStoreID = runtimeStoreID
	}
	info := AuthorityInfo{
		ExecutionAuthority:  executionAuthority,
		Authoritative:       authoritative,
		AuthoritySource:     "unowned",
		AuthorityDomain:     leaseStoreID,
		RuntimeStoreID:      runtimeStoreID,
		EventStoreID:        eventStoreID,
		LeaseStoreID:        leaseStoreID,
		RuntimeStoreDurable: StoreDurable(input.RuntimeStore),
		EventStoreDurable:   StoreDurable(input.EventStore),
		LeaseOwnerID:        strings.TrimSpace(input.OwnerID),
		LeaseOwnerKind:      ownerKind,
		LeaseOwnerPID:       input.PID,
		LeaseOwnerHostname:  strings.TrimSpace(input.Hostname),
	}
	if liveLease {
		info.AuthoritySource = "lease"
		expiry := expiresAt.UTC()
		info.LeaseExpiresAt = &expiry
	}
	return info
}
