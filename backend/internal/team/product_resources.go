package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// TeamRunSnapshotSchemaVersion is the version of the immutable configuration
	// envelope persisted when a product Team Run draft is created.
	TeamRunSnapshotSchemaVersion = 1

	TeamRunStatusDraft = "draft"
	TeamRunPhaseDraft  = "draft"

	TeamRunStatusActive  = "active"
	TeamRunPhaseStarting = "starting"
	TeamRunPhaseRunning  = "running"
	TeamRunPhaseTerminal = "terminal"

	TeamRunCompatibilityNone           = "none"
	TeamRunCompatibilityLegacyTeamView = "legacy_team_view"
	TeamRunSummaryNotApplicable        = "not_applicable"
	TeamRunSummaryPending              = "pending"
	TeamRunSummaryReady                = "ready"
	TeamRunSummaryFailed               = "failed"

	maxTeamDefinitionConfigBytes = 1 << 20
	maxTeamRunSnapshotBytes      = 2 << 20
	maxTeamResourceOwnerIDLength = 256
	maxTeamResourceKeyLength     = 256
	maxTeamDefinitionNameLength  = 256
	maxTeamDefinitionDescLength  = 4096
	maxTeamRunGoalLength         = 16 << 10
)

var (
	ErrTeamDefinitionNotFound        = errors.New("team definition not found")
	ErrTeamDefinitionVersionConflict = errors.New("team definition version conflict")
	ErrTeamDefinitionArchived        = errors.New("team definition is archived")
	ErrTeamResourceInvalid           = errors.New("invalid team product resource")
	ErrTeamRunIdempotencyConflict    = errors.New("team run idempotency key conflicts with another request")
	ErrTeamRunSnapshotCorrupt        = errors.New("team run snapshot checksum mismatch")
	ErrTeamRunSnapshotImmutable      = errors.New("team run snapshot is immutable")
)

// TeamDefinition is reusable configuration intent. It is deliberately
// separate from the legacy Team execution record.
type TeamDefinition struct {
	ID          string          `json:"id"`
	OwnerID     string          `json:"owner_id"`
	WorkspaceID string          `json:"workspace_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Version     int64           `json:"version"`
	Config      json.RawMessage `json:"config"`
	ArchivedAt  *time.Time      `json:"archived_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// TeamRun is one draft or execution derived from an immutable configuration
// snapshot. Snapshot is returned only to the owner-scoped store caller.
type TeamRun struct {
	ID                      string          `json:"id"`
	OwnerID                 string          `json:"owner_id"`
	WorkspaceID             string          `json:"workspace_id"`
	DefinitionID            string          `json:"definition_id,omitempty"`
	DefinitionVersion       *int64          `json:"definition_version,omitempty"`
	LegacyTeamID            string          `json:"legacy_team_id,omitempty"`
	Goal                    string          `json:"goal"`
	Status                  string          `json:"status"`
	Phase                   string          `json:"phase"`
	CompatibilityMode       string          `json:"compatibility_mode"`
	SnapshotSchema          int             `json:"snapshot_schema_version"`
	Snapshot                json.RawMessage `json:"config_snapshot"`
	SnapshotChecksum        string          `json:"snapshot_checksum"`
	Version                 int64           `json:"version"`
	EventSeq                int64           `json:"event_seq"`
	SummaryState            string          `json:"summary_state"`
	IdempotencyKeyHash      string          `json:"-"`
	RequestDigest           string          `json:"-"`
	RequestID               string          `json:"request_id,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	StartedAt               *time.Time      `json:"started_at,omitempty"`
	TerminalAt              *time.Time      `json:"terminal_at,omitempty"`
	StartIdempotencyKeyHash string          `json:"-"`
	StartRequestDigest      string          `json:"-"`
}

// TeamDefinitionCreateRequest creates a reusable, owner-scoped definition.
type TeamDefinitionCreateRequest struct {
	OwnerID     string
	WorkspaceID string
	Name        string
	Description string
	Config      json.RawMessage
}

// TeamDefinitionUpdateRequest updates mutable definition fields using a
// compare-and-swap version. Archived definitions cannot be modified.
type TeamDefinitionUpdateRequest struct {
	ID              string
	OwnerID         string
	ExpectedVersion int64
	Name            string
	Description     string
	Config          json.RawMessage
}

// TeamRunCreateRequest creates a draft from a Definition or an inline config.
// OwnerID is supplied by the trusted caller, never inferred from request JSON.
type TeamRunCreateRequest struct {
	OwnerID                   string
	WorkspaceID               string
	DefinitionID              string
	ExpectedDefinitionVersion int64
	Goal                      string
	Config                    json.RawMessage
	IdempotencyKey            string
	RequestID                 string
}

// TeamDefinitionRunStore is the additive persistence contract for new
// Definition/Run resources. It is kept separate from Store so legacy Team
// stores and callers remain source-compatible while the migration is staged.
type TeamDefinitionRunStore interface {
	CreateTeamDefinition(ctx context.Context, request TeamDefinitionCreateRequest) (*TeamDefinition, error)
	GetTeamDefinition(ctx context.Context, ownerID, definitionID string) (*TeamDefinition, error)
	ListTeamDefinitions(ctx context.Context, ownerID, workspaceID string, limit int) ([]TeamDefinition, error)
	UpdateTeamDefinition(ctx context.Context, request TeamDefinitionUpdateRequest) (*TeamDefinition, error)
	CreateTeamRunDraft(ctx context.Context, request TeamRunCreateRequest) (*TeamRun, error)
	GetTeamRun(ctx context.Context, ownerID, runID string) (*TeamRun, error)
	ListTeamRuns(ctx context.Context, ownerID, workspaceID string, limit int) ([]TeamRun, error)
}

// TeamRunStarter is the additive execution contract for turning an immutable
// Team Run snapshot into a real legacy Team runtime. It is intentionally
// separate from TeamDefinitionRunStore so callers that only provide the
// definition/draft surface do not accidentally claim to support execution.
type TeamRunStarter interface {
	StartTeamRun(ctx context.Context, request TeamRunStartRequest) (*TeamRun, error)
}

// TeamRunPreflightRequest identifies a Run using the same trusted ownership
// boundary as Start. WorkspaceID is checked by the implementation even though
// OwnerID already scopes the primary lookup; keeping both values here prevents
// an adapter from accidentally treating an owner-only lookup as sufficient.
type TeamRunPreflightRequest struct {
	OwnerID     string
	WorkspaceID string
	RunID       string
}

// TeamRunPreflightReport is the read-only, server-authoritative readiness
// result for a product Run. It intentionally reports the snapshot projection
// findings without claiming that route preview, worker liveness, artifact
// access, or any other future capability has been verified.
type TeamRunPreflightReport struct {
	RunID                  string             `json:"run_id"`
	LegacyTeamID           string             `json:"legacy_team_id,omitempty"`
	Version                int64              `json:"version"`
	Status                 string             `json:"status"`
	Phase                  string             `json:"phase"`
	SnapshotSchema         int                `json:"snapshot_schema_version"`
	Ready                  bool               `json:"ready"`
	BlockerCount           int                `json:"blocker_count"`
	WarningCount           int                `json:"warning_count"`
	InfoCount              int                `json:"info_count"`
	Findings               []PreflightFinding `json:"findings,omitempty"`
	Blockers               []PreflightFinding `json:"blockers,omitempty"`
	Warnings               []PreflightFinding `json:"warnings,omitempty"`
	Infos                  []PreflightFinding `json:"infos,omitempty"`
	RoutePreviewCapability string             `json:"route_preview_capability"`
}

// TeamRunPreflighter is optional so a draft-only or compatibility store cannot
// accidentally advertise a Start preflight surface it does not implement.
type TeamRunPreflighter interface {
	PreflightTeamRun(ctx context.Context, request TeamRunPreflightRequest) (*TeamRunPreflightReport, error)
}

// TeamRunLifecycleProjector updates the independent Run view from its
// authoritative legacy Team after the existing orchestrator reaches a
// terminal state. The legacy Team remains the execution authority during the
// compatibility period.
type TeamRunLifecycleProjector interface {
	ProjectTeamRunTerminalState(ctx context.Context, legacyTeamID string) error
}

// TeamRunStartRequest contains the trusted identity and optimistic/idempotent
// guards for a Start operation. OwnerID and WorkspaceID must come from the
// authenticated caller, never from the request body.
type TeamRunStartRequest struct {
	OwnerID         string
	WorkspaceID     string
	RunID           string
	ExpectedVersion int64
	IdempotencyKey  string
	RequestID       string
}

var (
	ErrTeamRunNotFound                 = errors.New("team run not found")
	ErrTeamRunVersionConflict          = errors.New("team run version conflict")
	ErrTeamRunInvalidTransition        = errors.New("team run cannot be started in its current state")
	ErrTeamRunStartIdempotencyConflict = errors.New("team run start idempotency key conflicts with another request")
	ErrTeamRunPreflightBlocked         = errors.New("team run preflight has blockers")
	ErrTeamRunProjectionConflict       = errors.New("team run legacy projection conflicts with an existing Team")
)

// TeamRunPreflightBlockedError retains the structured server-side findings
// that prevented Start. Callers can use errors.Is(err, ErrTeamRunPreflightBlocked)
// while API adapters can expose Findings without parsing the error string.
type TeamRunPreflightBlockedError struct {
	Findings []PreflightFinding
}

func (e *TeamRunPreflightBlockedError) Error() string {
	return ErrTeamRunPreflightBlocked.Error()
}

func (e *TeamRunPreflightBlockedError) Unwrap() error {
	return ErrTeamRunPreflightBlocked
}

type teamRunSnapshotEnvelope struct {
	SchemaVersion     int             `json:"schema_version"`
	OwnerID           string          `json:"owner_id"`
	WorkspaceID       string          `json:"workspace_id"`
	Goal              string          `json:"goal"`
	DefinitionID      string          `json:"definition_id,omitempty"`
	DefinitionVersion *int64          `json:"definition_version,omitempty"`
	Config            json.RawMessage `json:"config"`
	RequestID         string          `json:"request_id,omitempty"`
	IdempotencyHash   string          `json:"idempotency_key_hash"`
}

func normalizeTeamResourceJSON(raw json.RawMessage, maxBytes int) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxBytes {
		return nil, fmt.Errorf("%w: config must be non-empty and no larger than %d bytes", ErrTeamResourceInvalid, maxBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%w: config must be a JSON object: %v", ErrTeamResourceInvalid, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: config must be a JSON object", ErrTeamResourceInvalid)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: encode config: %v", ErrTeamResourceInvalid, err)
	}
	if len(canonical) > maxBytes {
		return nil, fmt.Errorf("%w: normalized config exceeds %d bytes", ErrTeamResourceInvalid, maxBytes)
	}
	return canonical, nil
}

func validateTeamResourceOwner(ownerID string) (string, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || len(ownerID) > maxTeamResourceOwnerIDLength {
		return "", fmt.Errorf("%w: owner_id is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceOwnerIDLength)
	}
	return ownerID, nil
}

func validateTeamResourceWorkspace(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || len(workspaceID) > maxTeamResourceKeyLength {
		return "", fmt.Errorf("%w: workspace_id is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceKeyLength)
	}
	return workspaceID, nil
}

func validateTeamDefinitionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxTeamDefinitionNameLength {
		return "", fmt.Errorf("%w: name is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamDefinitionNameLength)
	}
	return name, nil
}

func validateTeamDefinitionDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if len(description) > maxTeamDefinitionDescLength {
		return "", fmt.Errorf("%w: description must be at most %d bytes", ErrTeamResourceInvalid, maxTeamDefinitionDescLength)
	}
	return description, nil
}

func validateTeamRunGoal(goal string) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" || len(goal) > maxTeamRunGoalLength {
		return "", fmt.Errorf("%w: goal is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamRunGoalLength)
	}
	return goal, nil
}
