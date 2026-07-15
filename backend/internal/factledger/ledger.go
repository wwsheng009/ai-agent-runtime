package factledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
)

const (
	ScopeWorkspace = "workspace"
	ScopeSession   = "session"
	ScopeGoal      = "goal"

	memoryKindFact         = "structured_fact"
	memoryKindInvalidation = "fact_invalidation"
)

// Fact is an append-only, evidence-linked statement retained across compaction.
type Fact struct {
	FactID        string      `json:"fact_id"`
	WorkspaceID   string      `json:"workspace_id,omitempty"`
	SessionID     string      `json:"session_id"`
	GoalID        string      `json:"goal_id,omitempty"`
	Scope         string      `json:"scope"`
	Kind          string      `json:"kind"`
	Subject       string      `json:"subject"`
	Predicate     string      `json:"predicate"`
	Value         interface{} `json:"value"`
	SourceEventID string      `json:"source_event_id"`
	EvidenceRefs  []string    `json:"evidence_refs"`
	Confidence    float64     `json:"confidence"`
	ValidFrom     time.Time   `json:"valid_from"`
	InvalidatedBy string      `json:"invalidated_by,omitempty"`
	Supersedes    string      `json:"supersedes,omitempty"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

type Query struct {
	WorkspaceID string
	SessionID   string
	GoalID      string
	Kinds       []string
	Limit       int
}

type MemoryStore interface {
	InsertMemoryEntry(ctx context.Context, entry artifact.MemoryEntry) (string, error)
	LoadMemoryEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]artifact.MemoryEntry, error)
}

type Ledger struct{ store MemoryStore }

func New(store MemoryStore) *Ledger {
	if store == nil {
		return nil
	}
	return &Ledger{store: store}
}

func (l *Ledger) Append(ctx context.Context, fact Fact) (Fact, error) {
	if l == nil || l.store == nil {
		return Fact{}, fmt.Errorf("fact ledger store is not configured")
	}
	fact = normalizeFact(fact)
	if err := fact.Validate(); err != nil {
		return Fact{}, err
	}
	entry := artifact.MemoryEntry{
		SessionID:  fact.SessionID,
		TaskID:     fact.GoalID,
		Kind:       memoryKindFact,
		Priority:   factPriority(fact.Kind),
		Content:    factContent(fact),
		SourceRefs: append([]string(nil), fact.EvidenceRefs...),
		SourceHash: stableHash("fact", fact.FactID),
		CreatedAt:  fact.UpdatedAt,
	}
	if _, err := l.store.InsertMemoryEntry(ctx, entry); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

func (l *Ledger) Invalidate(ctx context.Context, sessionID, goalID, factID, invalidatedBy string, now time.Time) error {
	if l == nil || l.store == nil {
		return fmt.Errorf("fact ledger store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	factID = strings.TrimSpace(factID)
	invalidatedBy = strings.TrimSpace(invalidatedBy)
	if sessionID == "" || factID == "" || invalidatedBy == "" {
		return fmt.Errorf("session id, fact id, and invalidated_by are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := l.store.InsertMemoryEntry(ctx, artifact.MemoryEntry{
		SessionID: sessionID,
		TaskID:    strings.TrimSpace(goalID),
		Kind:      memoryKindInvalidation,
		Priority:  100,
		Content: map[string]interface{}{
			"fact_id": factID, "invalidated_by": invalidatedBy, "updated_at": now.Format(time.RFC3339Nano),
		},
		SourceHash: stableHash("invalidate", factID, invalidatedBy),
		CreatedAt:  now,
	})
	return err
}

func (f Fact) Validate() error {
	if strings.TrimSpace(f.FactID) == "" || strings.TrimSpace(f.SessionID) == "" {
		return fmt.Errorf("fact id and session id are required")
	}
	if f.Scope != ScopeWorkspace && f.Scope != ScopeSession && f.Scope != ScopeGoal {
		return fmt.Errorf("invalid fact scope %q", f.Scope)
	}
	if f.Scope == ScopeGoal && strings.TrimSpace(f.GoalID) == "" {
		return fmt.Errorf("goal-scoped fact requires goal id")
	}
	if strings.TrimSpace(f.Kind) == "" || strings.TrimSpace(f.Subject) == "" || strings.TrimSpace(f.Predicate) == "" {
		return fmt.Errorf("fact kind, subject, and predicate are required")
	}
	if strings.TrimSpace(f.SourceEventID) == "" || len(f.EvidenceRefs) == 0 {
		return fmt.Errorf("fact source event and evidence are required")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return fmt.Errorf("fact confidence must be between 0 and 1")
	}
	return nil
}

func uuidString() string { return uuid.NewString() }

func stableHash(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func factPriority(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "constraint":
		return 100
	case "decision", "test_result":
		return 90
	case "workspace", "runtime":
		return 80
	case "remaining_work":
		return 75
	default:
		return 60
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
