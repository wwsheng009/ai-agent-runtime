package supervision

import (
	"context"
	"time"
)

// StoreConfig controls persistence settings for the supervision store.
type StoreConfig struct {
	Path string
	DSN  string
}

// Store is the durable persistence contract for the supervision control
// plane: lifecycle notifications, durable actions, wake pending rows and
// Team parent edges. All stores must be safe for concurrent access.
type Store interface {
	Close() error

	// --- Notifications (doc 6.3) ---

	// UpsertNotification inserts or refreshes a notification keyed by its
	// idempotency key (root_scope+subject+subject_version+event_type).
	// A refresh keeps the original notification_id and timestamps but updates
	// state/reason/severity and bumps version.
	UpsertNotification(ctx context.Context, n Notification) (Notification, error)
	GetNotification(ctx context.Context, notificationID string) (*Notification, error)
	ListNotifications(ctx context.Context, filter NotificationFilter) ([]Notification, error)
	// LastNotificationSeq returns the highest event_seq observed for a root scope.
	LastNotificationSeq(ctx context.Context, rootScopeID string) (int64, error)

	MarkNotificationDelivered(ctx context.Context, notificationID string, at time.Time) error
	MarkNotificationSeen(ctx context.Context, notificationID string, at time.Time) error
	// AcknowledgeNotification CAS-guards the decision transition.
	AcknowledgeNotification(ctx context.Context, notificationID string, at time.Time, expectedVersion int64) (bool, error)
	// DeferNotification CAS-guards the defer transition.
	DeferNotification(ctx context.Context, notificationID string, until time.Time, reason string, expectedVersion int64) (bool, error)
	// ResolveNotification transitions the resolution state (CAS).
	ResolveNotification(ctx context.Context, notificationID string, resolution ResolutionState, at time.Time, expectedVersion int64) (bool, error)

	// --- Actions (doc 6.6) ---

	CreateAction(ctx context.Context, a ActionRecord) (ActionRecord, error)
	GetAction(ctx context.Context, actionID string) (*ActionRecord, error)
	ListActions(ctx context.Context, filter ActionFilter) ([]ActionRecord, error)
	// UpdateActionStatus CAS-guards action state transitions.
	UpdateActionStatus(ctx context.Context, a ActionRecord, expectedVersion int64) (bool, error)

	// --- Wake pending (doc 6.5 rule 3) ---

	InsertWakePending(ctx context.Context, w WakePending) error
	ListWakePending(ctx context.Context, filter WakeFilter) ([]WakePending, error)
	ClaimWakePending(ctx context.Context, wakeID, claimedBy string, at time.Time) (bool, error)
	ResolveWakePending(ctx context.Context, wakeID string) error

	// --- Team parent edges (doc 6.1/6.7) ---

	UpsertTeamEdge(ctx context.Context, edge TeamEdge) (TeamEdge, error)
	GetTeamEdge(ctx context.Context, edgeID string) (*TeamEdge, error)
	// ListChildTeams returns active child team edges for a parent team.
	ListChildTeams(ctx context.Context, parentTeamID string) ([]TeamEdge, error)
	// ListTeamAncestors returns the chain from the root team down to the child
	// team (root first), rejecting cycles.
	ListTeamAncestors(ctx context.Context, childTeamID string) ([]TeamEdge, error)
	// CloseTeamEdge marks a child edge terminal.
	CloseTeamEdge(ctx context.Context, edgeID string, at time.Time) error
}
