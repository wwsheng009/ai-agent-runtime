package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ScopeContext is the caller identity used to build a fail-closed visibility
// chain. TenantID is empty for the legacy single-tenant deployment. ProjectKey
// is mandatory. OwnerID/AgentID/SessionID/TaskID are optional at the project
// scope; a caller that supplies them may see the matching narrower scopes.
type ScopeContext struct {
	TenantID   string `json:"tenant_id,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Scope      Scope  `json:"scope,omitempty"`
	OwnerID    string `json:"owner_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
}

// ScopePermission is the access mode checked when a scope grant is consulted.
type ScopePermission string

const (
	ScopePermissionRead  ScopePermission = "read"
	ScopePermissionWrite ScopePermission = "write"
	ScopePermissionAdmin ScopePermission = "admin"
)

// MemoryScopeGrant is an explicit, audit-friendly exception to the default
// scope chain. Grants never cross tenant boundaries.
type MemoryScopeGrant struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id,omitempty"`
	ProjectKey  string    `json:"project_key,omitempty"`
	GrantorID   string    `json:"grantor_id"`
	GranteeType string    `json:"grantee_type"`
	GranteeID   string    `json:"grantee_id"`
	ScopeKind   Scope     `json:"scope_kind"`
	ScopeKey    string    `json:"scope_key"`
	Permission  string    `json:"permission"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	RevokedAt   time.Time `json:"revoked_at,omitempty"`
}

// ErrMemoryScopeContextMissing is returned when a read or write has no project
// scope to anchor on. Callers must fail closed instead of falling back to an
// unscoped project-global query.
var ErrMemoryScopeContextMissing = fmt.Errorf("memorystore: project scope context is required")

type memoryScopeContextKey struct{}

// WithMemoryScope attaches an explicit caller scope to a store operation.
// Missing fields are filled from the store's configured default scope.
func WithMemoryScope(ctx context.Context, scope ScopeContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, memoryScopeContextKey{}, scope)
}

// MemoryScopeFromContext returns the caller-supplied scope, if any.
func MemoryScopeFromContext(ctx context.Context) (ScopeContext, bool) {
	if ctx == nil {
		return ScopeContext{}, false
	}
	scope, ok := ctx.Value(memoryScopeContextKey{}).(ScopeContext)
	return scope, ok
}

func normalizeScopeKind(scope Scope) Scope {
	return Scope(strings.ToLower(strings.TrimSpace(string(scope))))
}

func validMemoryScopeKind(scope Scope) bool {
	switch normalizeScopeKind(scope) {
	case ScopeProject, ScopeUser, ScopeProfile, ScopeAgent, ScopeSession, ScopeTask:
		return true
	default:
		return false
	}
}

func (s *Store) defaultScopeContext() ScopeContext {
	if s == nil {
		return ScopeContext{}
	}
	scope := s.cfg.Scope
	scope.TenantID = strings.TrimSpace(scope.TenantID)
	scope.ProjectKey = firstNonEmpty(scope.ProjectKey, s.cfg.ProjectKey)
	scope.OwnerID = strings.TrimSpace(scope.OwnerID)
	scope.AgentID = strings.TrimSpace(scope.AgentID)
	scope.SessionID = strings.TrimSpace(scope.SessionID)
	scope.TaskID = strings.TrimSpace(scope.TaskID)
	return scope
}

func normalizeScopeContext(scope, defaults ScopeContext) (ScopeContext, error) {
	scope.TenantID = strings.TrimSpace(scope.TenantID)
	scope.ProjectKey = strings.TrimSpace(scope.ProjectKey)
	scope.OwnerID = strings.TrimSpace(scope.OwnerID)
	scope.AgentID = strings.TrimSpace(scope.AgentID)
	scope.SessionID = strings.TrimSpace(scope.SessionID)
	scope.TaskID = strings.TrimSpace(scope.TaskID)

	if scope.TenantID == "" {
		scope.TenantID = strings.TrimSpace(defaults.TenantID)
	}
	if defaults.TenantID != "" && scope.TenantID != strings.TrimSpace(defaults.TenantID) {
		return ScopeContext{}, fmt.Errorf("memorystore: scope tenant %q does not match store tenant %q", scope.TenantID, defaults.TenantID)
	}
	if scope.ProjectKey == "" {
		scope.ProjectKey = strings.TrimSpace(defaults.ProjectKey)
	}
	if defaults.ProjectKey != "" && scope.ProjectKey != strings.TrimSpace(defaults.ProjectKey) {
		return ScopeContext{}, fmt.Errorf("memorystore: scope project %q does not match store project %q", scope.ProjectKey, defaults.ProjectKey)
	}
	if scope.ProjectKey == "" {
		return ScopeContext{}, ErrMemoryScopeContextMissing
	}
	if scope.OwnerID == "" {
		scope.OwnerID = strings.TrimSpace(defaults.OwnerID)
	}
	if scope.AgentID == "" {
		scope.AgentID = strings.TrimSpace(defaults.AgentID)
	}
	if scope.SessionID == "" {
		scope.SessionID = strings.TrimSpace(defaults.SessionID)
	}
	if scope.TaskID == "" {
		scope.TaskID = strings.TrimSpace(defaults.TaskID)
	}
	return scope, nil
}

func (s *Store) resolveScopeContext(ctx context.Context) (ScopeContext, error) {
	explicit, ok := MemoryScopeFromContext(ctx)
	if !ok {
		explicit = ScopeContext{}
	}
	return normalizeScopeContext(explicit, s.defaultScopeContext())
}

// normalizeRecordScope must run before every memory_entries insert. It stamps
// the physical scope snapshot and recomputes the content hash with the narrower
// scope key so identical text in different user/agent/task scopes cannot
// collide through the legacy project-only hash.
func (s *Store) normalizeRecordScope(ctx context.Context, record *Record) error {
	if record == nil {
		return fmt.Errorf("memorystore: record is nil")
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return err
	}
	record.ProjectKey = firstNonEmpty(record.ProjectKey, scope.ProjectKey)
	if record.ProjectKey != scope.ProjectKey {
		return fmt.Errorf("memorystore: record project %q does not match active scope %q", record.ProjectKey, scope.ProjectKey)
	}
	record.TenantID = firstNonEmpty(record.TenantID, scope.TenantID)
	if scope.TenantID != "" && record.TenantID != scope.TenantID {
		return fmt.Errorf("memorystore: record tenant %q does not match active scope %q", record.TenantID, scope.TenantID)
	}

	kind := normalizeScopeKind(record.ScopeKind)
	if kind == "" {
		if validMemoryScopeKind(normalizeScopeKind(record.Scope)) {
			kind = normalizeScopeKind(record.Scope)
		}
	}
	if kind == "" {
		kind = normalizeScopeKind(scope.Scope)
	}
	if kind == "" {
		kind = ScopeProject
	}
	if !validMemoryScopeKind(kind) {
		return fmt.Errorf("memorystore: invalid scope kind %q", kind)
	}
	record.ScopeKind = kind
	record.Scope = kind
	record.ScopeKey = strings.TrimSpace(record.ScopeKey)
	record.OwnerID = strings.TrimSpace(record.OwnerID)
	record.AgentID = strings.TrimSpace(record.AgentID)
	record.TaskID = strings.TrimSpace(record.TaskID)
	record.SourceSessionID = strings.TrimSpace(record.SourceSessionID)

	switch kind {
	case ScopeProject:
		// Import/remap uses the destination project key as the canonical project
		// scope; never preserve a source workspace's project scope key.
		record.ScopeKey = record.ProjectKey
	case ScopeUser, ScopeProfile:
		record.OwnerID = firstNonEmpty(record.OwnerID, record.ScopeKey, scope.OwnerID)
		if record.OwnerID == "" {
			return fmt.Errorf("memorystore: user/profile scope requires owner id")
		}
		record.ScopeKey = firstNonEmpty(record.ScopeKey, record.OwnerID)
	case ScopeAgent:
		record.AgentID = firstNonEmpty(record.AgentID, record.ScopeKey, scope.AgentID)
		if record.AgentID == "" {
			return fmt.Errorf("memorystore: agent scope requires agent id")
		}
		record.ScopeKey = firstNonEmpty(record.ScopeKey, record.AgentID)
	case ScopeSession:
		record.SourceSessionID = firstNonEmpty(record.SourceSessionID, record.ScopeKey, scope.SessionID)
		if record.SourceSessionID == "" {
			return fmt.Errorf("memorystore: session scope requires session id")
		}
		record.ScopeKey = firstNonEmpty(record.ScopeKey, record.SourceSessionID)
	case ScopeTask:
		record.TaskID = firstNonEmpty(record.TaskID, record.ScopeKey, scope.TaskID)
		if record.TaskID == "" {
			return fmt.Errorf("memorystore: task scope requires task id")
		}
		record.ScopeKey = firstNonEmpty(record.ScopeKey, record.TaskID)
	}
	record.ContentHash = memoryRecordContentHash(*record)
	return nil
}

func memoryRecordContentHash(record Record) string {
	kind := normalizeScopeKind(record.ScopeKind)
	if kind == "" || kind == ScopeProject {
		return memoryContentHash(string(ScopeProject), record.ProjectKey, record.Kind, record.Text)
	}
	scopeIdentity := strings.Join([]string{
		record.TenantID, string(kind), record.ScopeKey, record.OwnerID, record.AgentID, record.TaskID,
	}, "\x00")
	return memoryContentHash(scopeIdentity, record.ProjectKey, record.Kind, record.Text)
}

func memoryScopeColumn(alias, name string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return name
	}
	return alias + "." + name
}

// backfillMemoryScopeColumns is idempotent repair for databases that were
// created before v24 or for rows inserted by a legacy code path that did not
// yet populate the canonical scope key.
func backfillMemoryScopeColumns(ctx context.Context, db *sql.DB) error {
	// Preserve legacy non-project scope values instead of silently turning
	// every historical row into project scope. Such rows remain fail-closed
	// until an owner/agent/session/task key is supplied.
	if _, err := db.ExecContext(ctx, `
UPDATE memory_entries
SET scope_kind = LOWER(scope)
WHERE LOWER(scope) IN ('user', 'profile', 'agent', 'session', 'task')
  AND (scope_key = '' OR scope_key IS NULL)
`); err != nil {
		return fmt.Errorf("memorystore: backfill entry scope kind: %w", err)
	}
	for _, statement := range []string{
		`UPDATE memory_entries SET scope_key = owner_id
		 WHERE scope_kind IN ('user', 'profile') AND (scope_key = '' OR scope_key IS NULL) AND owner_id != ''`,
		`UPDATE memory_entries SET scope_key = agent_id
		 WHERE scope_kind = 'agent' AND (scope_key = '' OR scope_key IS NULL) AND agent_id != ''`,
		`UPDATE memory_entries SET scope_key = source_session_id
		 WHERE scope_kind = 'session' AND (scope_key = '' OR scope_key IS NULL) AND source_session_id != ''`,
		`UPDATE memory_entries SET scope_key = task_id
		 WHERE scope_kind = 'task' AND (scope_key = '' OR scope_key IS NULL) AND task_id != ''`,
		`UPDATE memory_entries SET scope_kind = 'project', scope_key = project_key
		 WHERE scope_kind = 'project' AND (scope_key = '' OR scope_key IS NULL)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("memorystore: backfill entry scope key: %w", err)
		}
	}

	scopeTables := []string{
		"memory_jobs",
		"memory_summaries",
		"memory_turn_digests",
		"memory_suppressions",
	}
	for _, table := range scopeTables {
		if _, err := db.ExecContext(ctx, `
UPDATE `+table+`
SET scope_kind = LOWER(scope)
WHERE LOWER(scope) IN ('user', 'profile', 'agent', 'session', 'task')
  AND (scope_key = '' OR scope_key IS NULL)
`); err != nil {
			return fmt.Errorf("memorystore: backfill %s scope kind: %w", table, err)
		}
		if _, err := db.ExecContext(ctx, `
UPDATE `+table+`
SET scope_kind = 'project', scope_key = project_key
WHERE scope_kind = 'project' AND (scope_key = '' OR scope_key IS NULL)
`); err != nil {
			return fmt.Errorf("memorystore: backfill %s project scope: %w", table, err)
		}
	}

	tables := []string{
		"memory_events",
		"memory_usage",
		"memory_recall_traces",
		"memory_turn_output_receipts",
		"memory_versions",
		"memory_core",
		"memory_core_usage",
	}
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, `
UPDATE `+table+`
SET scope_key = project_key
WHERE scope_kind = 'project' AND (scope_key = '' OR scope_key IS NULL)
`); err != nil {
			return fmt.Errorf("memorystore: backfill %s scope: %w", table, err)
		}
	}
	return nil
}

// buildMemoryScopeVisibility emits a parameterized tenant/project/scope-chain
// predicate. It is the single place allowed to decide which memory rows are
// visible; callers must not hand-write project_key-only predicates.
func buildMemoryScopeVisibility(
	scope ScopeContext, alias string, permission ScopePermission, now time.Time,
) (string, []interface{}, error) {
	scope, err := normalizeScopeContext(scope, scope)
	if err != nil {
		return "", nil, err
	}
	if permission != ScopePermissionRead && permission != ScopePermissionWrite {
		permission = ScopePermissionRead
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	col := func(name string) string { return memoryScopeColumn(alias, name) }
	args := []interface{}{scope.TenantID, scope.ProjectKey}
	branches := []string{
		fmt.Sprintf("(%s = 'project' AND %s = ?)", col("scope_kind"), col("scope_key")),
	}
	args = append(args, scope.ProjectKey)
	if scope.OwnerID != "" {
		branches = append(branches, fmt.Sprintf(
			"(%s IN ('user','profile') AND (%s = ? OR %s = ?))",
			col("scope_kind"), col("owner_id"), col("scope_key")))
		args = append(args, scope.OwnerID, scope.OwnerID)
	}
	if scope.AgentID != "" {
		branches = append(branches, fmt.Sprintf(
			"(%s = 'agent' AND (%s = ? OR %s = ?))",
			col("scope_kind"), col("agent_id"), col("scope_key")))
		args = append(args, scope.AgentID, scope.AgentID)
	}
	if scope.SessionID != "" {
		branches = append(branches, fmt.Sprintf(
			"(%s = 'session' AND (%s = ? OR %s = ?))",
			col("scope_kind"), col("source_session_id"), col("scope_key")))
		args = append(args, scope.SessionID, scope.SessionID)
	}
	if scope.TaskID != "" {
		branches = append(branches, fmt.Sprintf(
			"(%s = 'task' AND (%s = ? OR %s = ?))",
			col("scope_kind"), col("task_id"), col("scope_key")))
		args = append(args, scope.TaskID, scope.TaskID)
	}

	type grantee struct {
		kind string
		id   string
	}
	grantees := make([]grantee, 0, 3)
	if scope.OwnerID != "" {
		grantees = append(grantees, grantee{kind: "user", id: scope.OwnerID})
	}
	if scope.AgentID != "" {
		grantees = append(grantees, grantee{kind: "agent", id: scope.AgentID})
	}
	if scope.TaskID != "" {
		grantees = append(grantees, grantee{kind: "task", id: scope.TaskID})
	}
	if len(grantees) > 0 {
		permissions := "'read','admin'"
		if permission == ScopePermissionWrite {
			permissions = "'write','admin'"
		}
		grantParts := make([]string, 0, len(grantees))
		grantArgs := []interface{}{scope.TenantID, scope.ProjectKey, now.UTC().Format(time.RFC3339Nano)}
		for _, grantee := range grantees {
			grantParts = append(grantParts, "(g.grantee_type = ? AND g.grantee_id = ?)")
			grantArgs = append(grantArgs, grantee.kind, grantee.id)
		}
		branches = append(branches, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM memory_scope_grants g
			WHERE g.tenant_id = ? AND g.project_key = ?
			  AND g.scope_kind = %s AND g.scope_key = %s
			  AND (g.revoked_at IS NULL OR g.revoked_at = '')
			  AND (g.expires_at IS NULL OR g.expires_at = '' OR g.expires_at > ?)
			  AND g.permission IN (%s)
			  AND (%s)
		)`, col("scope_kind"), col("scope_key"), permissions, strings.Join(grantParts, " OR ")))
		args = append(args, grantArgs...)
	}

	predicate := fmt.Sprintf(
		"(%s = ? AND %s = ? AND (%s))",
		col("tenant_id"), col("project_key"), strings.Join(branches, " OR "),
	)
	return predicate, args, nil
}

func (s *Store) memoryScopeVisibility(
	ctx context.Context, alias string, permission ScopePermission, now time.Time,
) (string, []interface{}, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return "", nil, err
	}
	return buildMemoryScopeVisibility(scope, alias, permission, now)
}

// CreateMemoryScopeGrant records an explicit read/write/admin exception. The
// tenant and project default to the current store; a caller cannot grant across
// tenants. Authorization of who may create grants belongs to the admin API.
func (s *Store) CreateMemoryScopeGrant(ctx context.Context, grant MemoryScopeGrant) (MemoryScopeGrant, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryScopeGrant{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryScopeGrant{}, err
	}
	grant.TenantID = firstNonEmpty(grant.TenantID, scope.TenantID)
	grant.ProjectKey = firstNonEmpty(grant.ProjectKey, scope.ProjectKey)
	if scope.TenantID != "" && grant.TenantID != scope.TenantID {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: scope grant cannot cross tenant boundary")
	}
	if grant.ProjectKey != scope.ProjectKey {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: scope grant project %q does not match active project %q", grant.ProjectKey, scope.ProjectKey)
	}
	grant.GrantorID = strings.TrimSpace(grant.GrantorID)
	if grant.GrantorID == "" {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: scope grant requires grantor id")
	}
	grant.GranteeType = strings.ToLower(strings.TrimSpace(grant.GranteeType))
	switch grant.GranteeType {
	case "user", "agent", "task":
	default:
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: invalid scope grant grantee type %q", grant.GranteeType)
	}
	grant.GranteeID = strings.TrimSpace(grant.GranteeID)
	if grant.GranteeID == "" {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: scope grant requires grantee id")
	}
	grant.ScopeKind = normalizeScopeKind(grant.ScopeKind)
	if !validMemoryScopeKind(grant.ScopeKind) {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: invalid scope grant kind %q", grant.ScopeKind)
	}
	grant.ScopeKey = strings.TrimSpace(grant.ScopeKey)
	if grant.ScopeKey == "" {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: scope grant requires scope key")
	}
	grant.Permission = strings.ToLower(strings.TrimSpace(grant.Permission))
	switch ScopePermission(grant.Permission) {
	case ScopePermissionRead, ScopePermissionWrite, ScopePermissionAdmin:
	default:
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: invalid scope grant permission %q", grant.Permission)
	}
	if grant.ID == "" {
		grant.ID = "grant_" + uuid.NewString()
	}
	now := time.Now().UTC()
	grant.CreatedAt = now
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_scope_grants (
    id, tenant_id, project_key, grantor_id, grantee_type, grantee_id,
    scope_kind, scope_key, permission, expires_at, created_at, revoked_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, grant.ID, grant.TenantID, grant.ProjectKey, grant.GrantorID, grant.GranteeType,
		grant.GranteeID, string(grant.ScopeKind), grant.ScopeKey, grant.Permission,
		nullableTime(grant.ExpiresAt), grant.CreatedAt.Format(time.RFC3339Nano),
		nullableTime(grant.RevokedAt))
	if err != nil {
		return MemoryScopeGrant{}, fmt.Errorf("memorystore: create scope grant: %w", err)
	}
	return grant, nil
}

// RevokeMemoryScopeGrant marks a grant unusable without deleting its audit row.
func (s *Store) RevokeMemoryScopeGrant(ctx context.Context, id string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `
UPDATE memory_scope_grants
SET revoked_at = ?
WHERE id = ? AND tenant_id = ? AND project_key = ? AND (revoked_at IS NULL OR revoked_at = '')
`, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id), scope.TenantID, scope.ProjectKey)
	if err != nil {
		return fmt.Errorf("memorystore: revoke scope grant: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
