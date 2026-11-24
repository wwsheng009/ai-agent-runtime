package memorystore

import "time"

type TrustLevel string

const (
	TrustSystemPolicy      TrustLevel = "system_policy"
	TrustUserExplicit      TrustLevel = "user_explicit"
	TrustUserConfirmed     TrustLevel = "user_confirmed"
	TrustAgentInference    TrustLevel = "agent_inference"
	TrustUntrustedExternal TrustLevel = "untrusted_external"
)

type MemoryEntity struct {
	ID            string                 `json:"id"`
	TenantID      string                 `json:"tenant_id,omitempty"`
	ProjectKey    string                 `json:"project_key"`
	ScopeKind     Scope                  `json:"scope_kind"`
	ScopeKey      string                 `json:"scope_key"`
	CanonicalName string                 `json:"canonical_name"`
	EntityType    string                 `json:"entity_type"`
	Aliases       []string               `json:"aliases,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type MemoryEntityLink struct {
	ID         string    `json:"id"`
	MemoryID   string    `json:"memory_id"`
	EntityID   string    `json:"entity_id"`
	Relation   string    `json:"relation"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
}

type EntityLink struct {
	CanonicalName string
	EntityType    string
	Aliases       []string
	Relation      string
	Confidence    float64
}

type MemoryFeedbackKind string

const (
	FeedbackHelpful    MemoryFeedbackKind = "helpful"
	FeedbackIrrelevant MemoryFeedbackKind = "irrelevant"
	FeedbackStale      MemoryFeedbackKind = "stale"
	FeedbackIncorrect  MemoryFeedbackKind = "incorrect"
)

type MemoryFeedback struct {
	ID         string             `json:"id"`
	MemoryID   string             `json:"memory_id"`
	TenantID   string             `json:"tenant_id,omitempty"`
	ProjectKey string             `json:"project_key"`
	SessionID  string             `json:"session_id,omitempty"`
	TurnID     string             `json:"turn_id,omitempty"`
	Kind       MemoryFeedbackKind `json:"kind"`
	Comment    string             `json:"comment,omitempty"`
	Actor      string             `json:"actor"`
	CreatedAt  time.Time          `json:"created_at"`
}

type FeedbackInput struct {
	MemoryID  string
	SessionID string
	TurnID    string
	Kind      MemoryFeedbackKind
	Comment   string
	Actor     string
}

type OperationalMetrics struct {
	TrustCounts       map[string]int `json:"trust_counts,omitempty"`
	FeedbackByKind    map[string]int `json:"feedback_by_kind,omitempty"`
	PatchesByStatus   map[string]int `json:"patches_by_status,omitempty"`
	DreamRunsByStatus map[string]int `json:"dream_runs_by_status,omitempty"`
	EntityCount       int            `json:"entity_count"`
}
