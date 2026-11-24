package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxRecordEntities = 16

var memoryEntityPatterns = []struct {
	entityType string
	pattern    *regexp.Regexp
}{
	{"error_code", regexp.MustCompile(`\bE_[A-Z0-9_]{2,}\b`)},
	{"version", regexp.MustCompile(`\bv[0-9]+\.[0-9]+(?:\.[0-9]+)?\b`)},
	{"path", regexp.MustCompile(`\b(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|kt|md|yaml|yml|json|toml)\b`)},
	{"constant", regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)},
}

func (s *Store) UpsertEntity(ctx context.Context, entity MemoryEntity) (MemoryEntity, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryEntity{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryEntity{}, err
	}
	entity.TenantID = firstNonEmpty(entity.TenantID, scope.TenantID)
	entity.ProjectKey = firstNonEmpty(entity.ProjectKey, scope.ProjectKey)
	if entity.ProjectKey != scope.ProjectKey {
		return MemoryEntity{}, fmt.Errorf("memorystore: entity project identity mismatch")
	}
	if entity.ScopeKind == "" {
		entity.ScopeKind = ScopeProject
	}
	if !validMemoryScopeKind(entity.ScopeKind) {
		return MemoryEntity{}, fmt.Errorf("memorystore: invalid entity scope kind %q", entity.ScopeKind)
	}
	if entity.ScopeKey == "" {
		entity.ScopeKey = entity.ProjectKey
	}
	entity.CanonicalName = strings.TrimSpace(entity.CanonicalName)
	entity.EntityType = strings.ToLower(strings.TrimSpace(entity.EntityType))
	if entity.CanonicalName == "" || entity.EntityType == "" {
		return MemoryEntity{}, fmt.Errorf("memorystore: entity name and type are required")
	}
	entity.Aliases = normalizeStringList(entity.Aliases, 32)
	now := time.Now().UTC()
	aliasesJSON, _ := json.Marshal(entity.Aliases)
	metadataJSON, _ := json.Marshal(entity.Metadata)
	entity.ID = "ent_" + uuid.NewString()
	entity.CreatedAt = now
	entity.UpdatedAt = now
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_entities (
    id, tenant_id, project_key, scope_kind, scope_key, canonical_name,
    entity_type, aliases_json, metadata_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(tenant_id, project_key, scope_kind, scope_key, entity_type, canonical_name)
DO UPDATE SET aliases_json = excluded.aliases_json,
              metadata_json = excluded.metadata_json,
              updated_at = excluded.updated_at
`, entity.ID, entity.TenantID, entity.ProjectKey, string(entity.ScopeKind), entity.ScopeKey,
		entity.CanonicalName, entity.EntityType, string(aliasesJSON), string(metadataJSON),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return MemoryEntity{}, fmt.Errorf("memorystore: upsert entity: %w", err)
	}
	return s.GetEntityByName(ctx, entity.TenantID, entity.ProjectKey,
		entity.ScopeKind, entity.ScopeKey, entity.EntityType, entity.CanonicalName)
}

func (s *Store) GetEntity(ctx context.Context, id string) (MemoryEntity, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryEntity{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return MemoryEntity{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, canonical_name,
       entity_type, aliases_json, metadata_json, created_at, updated_at
FROM memory_entities
WHERE id = ? AND tenant_id = ? AND project_key = ?
`, strings.TrimSpace(id), scope.TenantID, scope.ProjectKey)
	return scanEntity(row)
}

func (s *Store) GetEntityByName(
	ctx context.Context, tenantID, projectKey string, scopeKind Scope, scopeKey, entityType, canonicalName string,
) (MemoryEntity, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryEntity{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, canonical_name,
       entity_type, aliases_json, metadata_json, created_at, updated_at
FROM memory_entities
WHERE tenant_id = ? AND project_key = ? AND scope_kind = ? AND scope_key = ?
  AND entity_type = ? AND canonical_name = ?
`, tenantID, projectKey, string(scopeKind), scopeKey, entityType, canonicalName)
	return scanEntity(row)
}

func (s *Store) ListEntities(ctx context.Context, limit int) ([]MemoryEntity, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, canonical_name,
       entity_type, aliases_json, metadata_json, created_at, updated_at
FROM memory_entities
WHERE tenant_id = ? AND project_key = ?
ORDER BY updated_at DESC, id DESC
LIMIT ?
`, scope.TenantID, scope.ProjectKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entities := make([]MemoryEntity, 0, limit)
	for rows.Next() {
		entity, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entities = append(entities, entity)
	}
	return entities, rows.Err()
}

func (s *Store) LinkRecordEntity(ctx context.Context, memoryID string, link EntityLink) (MemoryEntity, error) {
	record, err := s.GetRecord(ctx, memoryID)
	if err != nil {
		return MemoryEntity{}, err
	}
	scopeKind := normalizeScopeKind(record.ScopeKind)
	if scopeKind == "" {
		scopeKind = ScopeProject
	}
	entity, err := s.UpsertEntity(ctx, MemoryEntity{
		TenantID: record.TenantID, ProjectKey: record.ProjectKey,
		ScopeKind: scopeKind, ScopeKey: record.ScopeKey,
		CanonicalName: link.CanonicalName, EntityType: link.EntityType,
		Aliases: link.Aliases,
	})
	if err != nil {
		return MemoryEntity{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return MemoryEntity{}, err
	}
	_, err = db.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_entity_links (
    id, memory_id, entity_id, relation, confidence, created_at
) VALUES (?, ?, ?, ?, ?, ?)
`, "link_"+uuid.NewString(), record.ID, entity.ID, firstNonEmpty(link.Relation, "mentions"),
		link.Confidence, nowText())
	if err != nil {
		return MemoryEntity{}, err
	}
	return entity, nil
}

func (s *Store) ListRecordEntities(ctx context.Context, memoryID string) ([]MemoryEntity, error) {
	record, err := s.GetRecord(ctx, memoryID)
	if err != nil {
		return nil, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT e.id, e.tenant_id, e.project_key, e.scope_kind, e.scope_key, e.canonical_name,
       e.entity_type, e.aliases_json, e.metadata_json, e.created_at, e.updated_at
FROM memory_entities e
JOIN memory_entity_links l ON l.entity_id = e.id
WHERE l.memory_id = ?
ORDER BY l.confidence DESC, e.canonical_name
`, record.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entities := make([]MemoryEntity, 0, 8)
	for rows.Next() {
		entity, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entities = append(entities, entity)
	}
	return entities, rows.Err()
}

func linkRecordEntitiesTx(ctx context.Context, tx *sql.Tx, record Record) error {
	if tx == nil || strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Text) == "" {
		return nil
	}
	links := extractEntityLinks(record.Text)
	for _, link := range links {
		entityID := "ent_" + uuid.NewString()
		aliasesJSON, _ := json.Marshal(link.Aliases)
		metadataJSON, _ := json.Marshal(map[string]interface{}{"auto": true})
		now := nowText()
		_, err := tx.ExecContext(ctx, `
INSERT INTO memory_entities (
    id, tenant_id, project_key, scope_kind, scope_key, canonical_name,
    entity_type, aliases_json, metadata_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(tenant_id, project_key, scope_kind, scope_key, entity_type, canonical_name)
DO UPDATE SET updated_at = excluded.updated_at
`, entityID, record.TenantID, record.ProjectKey, string(record.ScopeKind), record.ScopeKey,
			link.CanonicalName, link.EntityType, string(aliasesJSON), string(metadataJSON), now, now)
		if err != nil {
			return err
		}
		var resolvedID string
		if err := tx.QueryRowContext(ctx, `
SELECT id FROM memory_entities
WHERE tenant_id = ? AND project_key = ? AND scope_kind = ? AND scope_key = ?
  AND entity_type = ? AND canonical_name = ?
`, record.TenantID, record.ProjectKey, string(record.ScopeKind), record.ScopeKey,
			link.EntityType, link.CanonicalName).Scan(&resolvedID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_entity_links (
    id, memory_id, entity_id, relation, confidence, created_at
) VALUES (?, ?, ?, ?, ?, ?)
`, "link_"+uuid.NewString(), record.ID, resolvedID, link.Relation, link.Confidence, now); err != nil {
			return err
		}
	}
	return nil
}

func extractEntityLinks(text string) []EntityLink {
	seen := make(map[string]EntityLink)
	for _, candidate := range memoryEntityPatterns {
		for _, term := range candidate.pattern.FindAllString(text, maxRecordEntities) {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			key := candidate.entityType + "\x00" + term
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = EntityLink{
				CanonicalName: term, EntityType: candidate.entityType,
				Aliases: []string{term}, Relation: "mentions", Confidence: 0.8,
			}
			if len(seen) >= maxRecordEntities {
				break
			}
		}
	}
	links := make([]EntityLink, 0, len(seen))
	for _, link := range seen {
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].EntityType == links[j].EntityType {
			return links[i].CanonicalName < links[j].CanonicalName
		}
		return links[i].EntityType < links[j].EntityType
	})
	return links
}

type entityScanner interface {
	Scan(dest ...interface{}) error
}

func scanEntity(row entityScanner) (MemoryEntity, error) {
	var (
		entity                    MemoryEntity
		id, tenantID, projectKey  sql.NullString
		scopeKind, scopeKey, name sql.NullString
		entityType, aliasesJSON   sql.NullString
		metadataJSON, createdAt   sql.NullString
		updatedAt                 sql.NullString
	)
	if err := row.Scan(&id, &tenantID, &projectKey, &scopeKind, &scopeKey, &name,
		&entityType, &aliasesJSON, &metadataJSON, &createdAt, &updatedAt); err != nil {
		return MemoryEntity{}, err
	}
	entity = MemoryEntity{
		ID: id.String, TenantID: tenantID.String, ProjectKey: projectKey.String,
		ScopeKind: Scope(scopeKind.String), ScopeKey: scopeKey.String,
		CanonicalName: name.String, EntityType: entityType.String,
		CreatedAt: parseTime(createdAt.String), UpdatedAt: parseTime(updatedAt.String),
	}
	_ = json.Unmarshal([]byte(aliasesJSON.String), &entity.Aliases)
	if strings.TrimSpace(metadataJSON.String) != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &entity.Metadata)
	}
	return entity, nil
}

func (s *Store) searchEntityLinkedRecordIDs(ctx context.Context, query string, limit int) ([]string, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	like := "%" + query + "%"
	rows, err := db.QueryContext(ctx, `
SELECT l.memory_id, MAX(l.confidence)
FROM memory_entities e
JOIN memory_entity_links l ON l.entity_id = e.id
JOIN memory_entries m ON m.id = l.memory_id
WHERE e.tenant_id = ? AND e.project_key = ?
  AND (LOWER(e.canonical_name) LIKE ? OR LOWER(e.aliases_json) LIKE ?)
  AND m.status = 'active' AND m.archived = 0
GROUP BY l.memory_id
ORDER BY MAX(l.confidence) DESC, l.memory_id
LIMIT ?
`, scope.TenantID, scope.ProjectKey, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		var confidence float64
		if err := rows.Scan(&id, &confidence); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
