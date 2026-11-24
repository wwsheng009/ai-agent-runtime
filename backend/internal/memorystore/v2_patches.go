package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

const (
	maxPatchContentRunes = 12000
	maxPatchEvidence     = 64
)

func (s *Store) CreatePatch(ctx context.Context, input PatchInput) (MemoryPatch, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryPatch{}, err
	}
	patch, err := normalizePatchInput(input, scope)
	if err != nil {
		return MemoryPatch{}, err
	}
	patch.ID = "patch_" + uuid.NewString()
	patch.Status = PatchStatusProposed
	patch.CreatedAt = time.Now().UTC()
	targetsJSON, _ := json.Marshal(patch.TargetIDs)
	metadataJSON, _ := json.Marshal(patch.ProposedMetadata)
	evidenceJSON, _ := json.Marshal(patch.Evidence)
	revisionsJSON, _ := json.Marshal(patch.ExpectedRevisions)
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_patches (
    id, tenant_id, project_key, scope_kind, scope_key, operation,
    target_ids_json, proposed_content, proposed_metadata_json, reason, evidence_json,
    risk_level, status, expected_revisions_json, created_by, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)
`, patch.ID, patch.TenantID, patch.ProjectKey, string(patch.ScopeKind), patch.ScopeKey,
		string(patch.Operation), string(targetsJSON), patch.ProposedContent, string(metadataJSON),
		patch.Reason, string(evidenceJSON), string(patch.RiskLevel), string(revisionsJSON),
		patch.CreatedBy, patch.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return MemoryPatch{}, fmt.Errorf("memorystore: create patch: %w", err)
	}
	return s.GetPatch(ctx, patch.ID)
}

func (s *Store) GetPatch(ctx context.Context, id string) (MemoryPatch, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryPatch{}, err
	}
	patch, err := s.getPatchRaw(ctx, id, scope.ProjectKey)
	if err != nil {
		return MemoryPatch{}, err
	}
	if !patchVisibleToScope(patch, scope) {
		return MemoryPatch{}, sql.ErrNoRows
	}
	return patch, nil
}

func (s *Store) ListPatches(ctx context.Context, filter PatchFilter) ([]MemoryPatch, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{"tenant_id = ?", "project_key = ?"}
	args := []interface{}{scope.TenantID, scope.ProjectKey}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.Risk != "" {
		where = append(where, "risk_level = ?")
		args = append(args, string(filter.Risk))
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, operation,
       target_ids_json, proposed_content, proposed_metadata_json, reason, evidence_json,
       risk_level, status, expected_revisions_json, applied_record_ids_json,
       applied_revisions_json, rollback_versions_json, created_by, reviewed_by,
       review_note, error, created_at, COALESCE(reviewed_at, ''),
       COALESCE(applied_at, ''), COALESCE(rolled_back_at, '')
FROM memory_patches
WHERE `+strings.Join(where, " AND ")+`
ORDER BY created_at DESC, id DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	patches := make([]MemoryPatch, 0, limit)
	for rows.Next() {
		patch, scanErr := scanPatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if !patchVisibleToScope(patch, scope) {
			continue
		}
		patches = append(patches, patch)
	}
	return patches, rows.Err()
}

func (s *Store) ApprovePatch(ctx context.Context, id, reviewer, note string) (MemoryPatch, error) {
	reviewer = strings.TrimSpace(reviewer)
	if reviewer == "" {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch reviewer is required")
	}
	note = sanitizePatchText(note)
	reviewedAt := time.Now().UTC()
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryPatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	patch, err := s.getPatchTx(ctx, tx, id)
	if err != nil {
		return MemoryPatch{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryPatch{}, err
	}
	if !patchVisibleToScope(patch, scope) {
		return MemoryPatch{}, sql.ErrNoRows
	}
	if patch.Status != PatchStatusProposed {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch %s is not proposed", id)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_patches
SET status = 'approved', reviewed_by = ?, review_note = ?, reviewed_at = ?
WHERE id = ? AND status = 'proposed'
`, reviewer, note, reviewedAt.Format(time.RFC3339Nano), patch.ID); err != nil {
		return MemoryPatch{}, err
	}
	if err := insertPatchAuditTx(ctx, tx, patch, "patch_approved", reviewer, map[string]interface{}{"note": note}); err != nil {
		return MemoryPatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryPatch{}, err
	}
	return s.GetPatch(ctx, id)
}

func (s *Store) RejectPatch(ctx context.Context, id, reviewer, note string) (MemoryPatch, error) {
	reviewer = strings.TrimSpace(reviewer)
	if reviewer == "" {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch reviewer is required")
	}
	note = sanitizePatchText(note)
	reviewedAt := time.Now().UTC()
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryPatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	patch, err := s.getPatchTx(ctx, tx, id)
	if err != nil {
		return MemoryPatch{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return MemoryPatch{}, err
	}
	if !patchVisibleToScope(patch, scope) {
		return MemoryPatch{}, sql.ErrNoRows
	}
	if patch.Status != PatchStatusProposed && patch.Status != PatchStatusApproved {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch %s cannot be rejected from status %s", id, patch.Status)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_patches
SET status = 'rejected', reviewed_by = ?, review_note = ?, reviewed_at = ?
WHERE id = ?
`, reviewer, note, reviewedAt.Format(time.RFC3339Nano), patch.ID); err != nil {
		return MemoryPatch{}, err
	}
	if err := insertPatchAuditTx(ctx, tx, patch, "patch_rejected", reviewer, map[string]interface{}{"note": note}); err != nil {
		return MemoryPatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryPatch{}, err
	}
	return s.GetPatch(ctx, id)
}

func (s *Store) ApplyPatch(ctx context.Context, id string, opts PatchApplyOptions) (MemoryPatch, error) {
	patch, err := s.GetPatch(ctx, id)
	if err != nil {
		return MemoryPatch{}, err
	}
	if patch.Status == PatchStatusApplied {
		return patch, nil
	}
	actor := strings.TrimSpace(opts.Actor)
	if actor == "" {
		actor = "admin"
	}
	if patch.Status == PatchStatusProposed && patch.RiskLevel == PatchRiskLow && opts.AutoApproveLowRisk {
		patch, err = s.ApprovePatch(ctx, id, actor, "auto-approved low-risk patch")
		if err != nil {
			return MemoryPatch{}, err
		}
	}
	if patch.Status != PatchStatusApproved {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch %s must be approved before apply", id)
	}
	appliedIDs, appliedRevisions, rollbackVersions, err := s.applyPatchOperation(ctx, patch, actor)
	if err != nil {
		_ = s.markPatchFailed(ctx, patch, err)
		return MemoryPatch{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryPatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	appliedJSON, _ := json.Marshal(appliedIDs)
	appliedRevisionsJSON, _ := json.Marshal(appliedRevisions)
	rollbackJSON, _ := json.Marshal(rollbackVersions)
	appliedAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_patches
SET status = 'applied', applied_at = ?, applied_record_ids_json = ?,
    applied_revisions_json = ?, rollback_versions_json = ?, error = ''
WHERE id = ? AND status = 'approved'
`, appliedAt.Format(time.RFC3339Nano), string(appliedJSON), string(appliedRevisionsJSON),
		string(rollbackJSON), patch.ID); err != nil {
		return MemoryPatch{}, err
	}
	if err := insertPatchAuditTx(ctx, tx, patch, "patch_applied", actor, map[string]interface{}{
		"applied_record_ids": appliedIDs,
	}); err != nil {
		return MemoryPatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryPatch{}, err
	}
	return s.GetPatch(ctx, id)
}

func (s *Store) RollbackPatch(ctx context.Context, id, actor, reason string) (MemoryPatch, error) {
	patch, err := s.GetPatch(ctx, id)
	if err != nil {
		return MemoryPatch{}, err
	}
	if patch.Status != PatchStatusApplied {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch %s is not applied", id)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "admin"
	}
	reason = sanitizePatchText(reason)
	if reason == "" {
		reason = "patch rollback"
	}
	for _, recordID := range patch.AppliedRecordIDs {
		record, err := s.GetRecord(ctx, recordID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return MemoryPatch{}, err
		}
		if expected := patch.AppliedRevisions[recordID]; expected > 0 && record.Revision != expected {
			return MemoryPatch{}, fmt.Errorf("memorystore: patch target %s changed after apply", recordID)
		}
		version := patch.RollbackVersions[recordID]
		if version > 0 {
			_, err = s.RollbackRecord(ctx, recordID, RecordRollbackOptions{
				TargetVersion: version, ExpectedRevision: record.Revision,
				Reason: reason, ChangedBy: actor, RevertSupersede: true,
			})
			if err != nil {
				return MemoryPatch{}, err
			}
			continue
		}
		if err := s.DeleteRecord(ctx, recordID); err != nil && err != sql.ErrNoRows {
			return MemoryPatch{}, err
		}
	}
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryPatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rolledBackAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_patches SET status = 'rolled_back', rolled_back_at = ? WHERE id = ? AND status = 'applied'
`, rolledBackAt.Format(time.RFC3339Nano), patch.ID); err != nil {
		return MemoryPatch{}, err
	}
	if err := insertPatchAuditTx(ctx, tx, patch, "patch_rolled_back", actor, map[string]interface{}{"reason": reason}); err != nil {
		return MemoryPatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryPatch{}, err
	}
	return s.GetPatch(ctx, id)
}

func (s *Store) applyPatchOperation(
	ctx context.Context, patch MemoryPatch, actor string,
) ([]string, map[string]int64, map[string]int64, error) {
	appliedIDs := []string{}
	appliedRevisions := map[string]int64{}
	rollbackVersions := map[string]int64{}
	switch patch.Operation {
	case PatchOperationAdd:
		record, err := s.applyAddPatch(ctx, patch)
		if err != nil {
			return nil, nil, nil, err
		}
		appliedIDs = append(appliedIDs, record.ID)
		appliedRevisions[record.ID] = record.Revision
		rollbackVersions[record.ID] = 0
	case PatchOperationUpdate, PatchOperationMerge:
		if len(patch.TargetIDs) == 0 {
			return nil, nil, nil, fmt.Errorf("memorystore: update/merge patch requires a target")
		}
		target, err := s.patchTarget(ctx, patch, patch.TargetIDs[0])
		if err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPatchRevision(patch, target); err != nil {
			return nil, nil, nil, err
		}
		recordPatch := recordPatchFromPatch(patch)
		updated, err := s.UpdateRecord(ctx, target.ID, recordPatch)
		if err != nil {
			return nil, nil, nil, err
		}
		appliedIDs = append(appliedIDs, updated.ID)
		appliedRevisions[updated.ID] = updated.Revision
		rollbackVersions[updated.ID] = target.Revision
	case PatchOperationDelete:
		if len(patch.TargetIDs) == 0 {
			return nil, nil, nil, fmt.Errorf("memorystore: delete patch requires a target")
		}
		target, err := s.patchTarget(ctx, patch, patch.TargetIDs[0])
		if err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPatchRevision(patch, target); err != nil {
			return nil, nil, nil, err
		}
		if err := s.DeleteRecord(ctx, target.ID); err != nil {
			return nil, nil, nil, err
		}
		deleted, err := s.GetRecord(ctx, target.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		appliedIDs = append(appliedIDs, deleted.ID)
		appliedRevisions[deleted.ID] = deleted.Revision
		rollbackVersions[deleted.ID] = target.Revision
	case PatchOperationSupersede:
		if len(patch.TargetIDs) < 2 {
			return nil, nil, nil, fmt.Errorf("memorystore: supersede patch requires loser and winner ids")
		}
		loser, err := s.patchTarget(ctx, patch, patch.TargetIDs[0])
		if err != nil {
			return nil, nil, nil, err
		}
		winner, err := s.patchTarget(ctx, patch, patch.TargetIDs[1])
		if err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPatchRevision(patch, loser); err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPatchRevision(patch, winner); err != nil {
			return nil, nil, nil, err
		}
		if _, err := s.SupersedeRecord(ctx, loser.ID, winner.ID, firstNonEmpty(patch.Reason, "dreaming supersede")); err != nil {
			return nil, nil, nil, err
		}
		loserAfter, err := s.GetRecord(ctx, loser.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		winnerAfter, err := s.GetRecord(ctx, winner.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		appliedIDs = append(appliedIDs, loser.ID, winner.ID)
		appliedRevisions[loser.ID] = loserAfter.Revision
		appliedRevisions[winner.ID] = winnerAfter.Revision
		rollbackVersions[loser.ID] = loser.Revision
		rollbackVersions[winner.ID] = winner.Revision
	default:
		return nil, nil, nil, fmt.Errorf("memorystore: patch operation %q is not implemented", patch.Operation)
	}
	_ = actor
	return appliedIDs, appliedRevisions, rollbackVersions, nil
}

func (s *Store) applyAddPatch(ctx context.Context, patch MemoryPatch) (Record, error) {
	metadata := patch.ProposedMetadata
	kind := metadataString(metadata, "kind", "reference")
	record := Record{
		ID: "mem_patch_" + uuid.NewString(), ProjectKey: patch.ProjectKey,
		TenantID: patch.TenantID, ScopeKind: patch.ScopeKind, ScopeKey: patch.ScopeKey,
		Kind: kind, Status: StatusActive, Text: patch.ProposedContent,
		Title: metadataString(metadata, "title", ""), Summary: metadataString(metadata, "summary", ""),
		CanonicalID: metadataString(metadata, "canonical_id", ""),
		Tags:        metadataStringSlice(metadata, "tags"), Confidence: metadataFloat(metadata, "confidence", 1),
		Sensitivity:    metadataString(metadata, "sensitivity", "internal"),
		Pinned:         metadataBool(metadata, "pinned", false),
		RetentionClass: metadataString(metadata, "retention_class", "standard"),
		SourceKind:     metadataString(metadata, "source_kind", "dreaming_patch"),
		SourceType:     "dreaming_patch", SourceTrust: "agent_inference",
		SourceSessionID: metadataString(metadata, "session_id", ""),
		SourceRefs:      append([]string(nil), patch.Evidence...),
		Revision:        1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Freshness: FreshnessFresh, LastVerifiedAt: time.Now().UTC(),
		Metadata: map[string]interface{}{"patch_id": patch.ID},
	}
	if record.CanonicalID == "" {
		record.CanonicalID = record.ID
	}
	if record.ScopeKind == "" {
		record.ScopeKind = ScopeProject
	}
	if record.ScopeKey == "" {
		record.ScopeKey = patch.ProjectKey
	}
	switch record.ScopeKind {
	case ScopeUser, ScopeProfile:
		record.OwnerID = record.ScopeKey
	case ScopeAgent:
		record.AgentID = record.ScopeKey
	case ScopeSession:
		record.SourceSessionID = firstNonEmpty(record.SourceSessionID, record.ScopeKey)
	case ScopeTask:
		record.TaskID = record.ScopeKey
	}
	record.NormalizedText = normalizeMemoryText(record.Text)
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	if err := s.insertRecordTx(ctx, db, record, "patch", record.SourceSessionID); err != nil {
		return Record{}, err
	}
	return s.GetRecord(ctx, record.ID)
}

func (s *Store) patchTarget(ctx context.Context, patch MemoryPatch, id string) (Record, error) {
	record, err := s.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if normalizeScopeKind(record.ScopeKind) != normalizeScopeKind(patch.ScopeKind) ||
		record.ScopeKey != patch.ScopeKey || record.ProjectKey != patch.ProjectKey ||
		(patch.TenantID != "" && record.TenantID != patch.TenantID) {
		return Record{}, fmt.Errorf("memorystore: patch target %s scope mismatch", id)
	}
	return record, nil
}

func verifyPatchRevision(patch MemoryPatch, record Record) error {
	expected := patch.ExpectedRevisions[record.ID]
	if expected <= 0 {
		return fmt.Errorf("memorystore: patch expected revision is required for %s", record.ID)
	}
	if record.Revision != expected {
		return fmt.Errorf("memorystore: patch target %s revision mismatch", record.ID)
	}
	return nil
}

func (s *Store) markPatchFailed(ctx context.Context, patch MemoryPatch, applyErr error) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	message := sanitizePatchText(applyErr.Error())
	_, err = db.ExecContext(ctx, `
UPDATE memory_patches SET status = 'failed', error = ? WHERE id = ? AND status = 'approved'
`, message, patch.ID)
	return err
}

func recordPatchFromPatch(patch MemoryPatch) RecordPatch {
	recordPatch := RecordPatch{}
	if strings.TrimSpace(patch.ProposedContent) != "" {
		text := strings.TrimSpace(patch.ProposedContent)
		recordPatch.Text = &text
	}
	metadata := patch.ProposedMetadata
	if value, ok := metadata["title"].(string); ok {
		recordPatch.Title = &value
	}
	if value, ok := metadata["summary"].(string); ok {
		recordPatch.Summary = &value
	}
	if value, ok := metadata["canonical_id"].(string); ok {
		recordPatch.CanonicalID = &value
	}
	if tags := metadataStringSlice(metadata, "tags"); len(tags) > 0 {
		recordPatch.Tags = &tags
	}
	if value, ok := metadata["pinned"].(bool); ok {
		recordPatch.Pinned = &value
	}
	if value, ok := metadata["archived"].(bool); ok {
		recordPatch.Archived = &value
	}
	return recordPatch
}

func normalizePatchInput(input PatchInput, scope ScopeContext) (MemoryPatch, error) {
	input.ProjectKey = firstNonEmpty(input.ProjectKey, scope.ProjectKey)
	input.TenantID = firstNonEmpty(input.TenantID, scope.TenantID)
	if input.ProjectKey == "" || input.ProjectKey != scope.ProjectKey {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch project identity mismatch")
	}
	if scope.TenantID != "" && input.TenantID != scope.TenantID {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch tenant identity mismatch")
	}
	scopeKind := normalizeScopeKind(input.ScopeKind)
	if scopeKind == "" {
		scopeKind = normalizedScopeOrProject(scope.Scope)
	}
	if !validMemoryScopeKind(scopeKind) {
		return MemoryPatch{}, fmt.Errorf("memorystore: invalid patch scope kind %q", scopeKind)
	}
	scopeKey := strings.TrimSpace(input.ScopeKey)
	switch scopeKind {
	case ScopeProject:
		scopeKey = input.ProjectKey
	case ScopeUser, ScopeProfile:
		scopeKey = firstNonEmpty(scopeKey, scope.OwnerID)
	case ScopeAgent:
		scopeKey = firstNonEmpty(scopeKey, scope.AgentID)
	case ScopeSession:
		scopeKey = firstNonEmpty(scopeKey, scope.SessionID)
	case ScopeTask:
		scopeKey = firstNonEmpty(scopeKey, scope.TaskID)
	}
	if scopeKey == "" {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch scope key is required")
	}
	operation := PatchOperation(strings.ToLower(strings.TrimSpace(string(input.Operation))))
	switch operation {
	case PatchOperationAdd, PatchOperationUpdate, PatchOperationMerge,
		PatchOperationSupersede, PatchOperationDelete:
	default:
		return MemoryPatch{}, fmt.Errorf("memorystore: patch operation %q is not supported", operation)
	}
	risk := PatchRisk(strings.ToLower(strings.TrimSpace(string(input.RiskLevel))))
	if risk == "" {
		risk = PatchRiskMedium
	}
	switch risk {
	case PatchRiskLow, PatchRiskMedium, PatchRiskHigh:
	default:
		return MemoryPatch{}, fmt.Errorf("memorystore: invalid patch risk %q", risk)
	}
	content := strings.TrimSpace(input.ProposedContent)
	if len([]rune(content)) > maxPatchContentRunes {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch content exceeds limit")
	}
	if memoryredact.DetectSecret(content) != "" {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch content contains secret-shaped data")
	}
	reason := sanitizePatchText(input.Reason)
	if reason == "" {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch reason is required")
	}
	evidence := normalizeStringList(input.Evidence, maxPatchEvidence)
	if len(evidence) == 0 {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch evidence/source ids are required")
	}
	targetIDs := normalizeStringList(input.TargetIDs, 16)
	if operation != PatchOperationAdd && len(targetIDs) == 0 {
		return MemoryPatch{}, fmt.Errorf("memorystore: patch target ids are required")
	}
	expected := make(map[string]int64, len(input.ExpectedRevisions))
	for id, revision := range input.ExpectedRevisions {
		id = strings.TrimSpace(id)
		if id != "" && revision > 0 {
			expected[id] = revision
		}
	}
	if operation != PatchOperationAdd {
		for _, id := range targetIDs {
			if expected[id] <= 0 {
				return MemoryPatch{}, fmt.Errorf("memorystore: patch expected revision is required for %s", id)
			}
		}
	}
	createdBy := strings.TrimSpace(input.CreatedBy)
	if createdBy == "" {
		createdBy = "admin"
	}
	return MemoryPatch{
		TenantID: input.TenantID, ProjectKey: input.ProjectKey,
		ScopeKind: scopeKind, ScopeKey: scopeKey, Operation: operation,
		TargetIDs: targetIDs, ProposedContent: content,
		ProposedMetadata: cloneMap(input.ProposedMetadata), Reason: reason,
		Evidence: evidence, RiskLevel: risk, ExpectedRevisions: expected,
		CreatedBy: createdBy,
	}, nil
}

func normalizedScopeOrProject(scope Scope) Scope {
	normalized := normalizeScopeKind(scope)
	if normalized == "" {
		return ScopeProject
	}
	return normalized
}

func patchVisibleToScope(patch MemoryPatch, scope ScopeContext) bool {
	if patch.ProjectKey != scope.ProjectKey {
		return false
	}
	if scope.TenantID != "" && patch.TenantID != scope.TenantID {
		return false
	}
	switch normalizeScopeKind(patch.ScopeKind) {
	case ScopeProject:
		return true
	case ScopeUser, ScopeProfile:
		return scope.OwnerID != "" && patch.ScopeKey == scope.OwnerID
	case ScopeAgent:
		return scope.AgentID != "" && patch.ScopeKey == scope.AgentID
	case ScopeSession:
		return scope.SessionID != "" && patch.ScopeKey == scope.SessionID
	case ScopeTask:
		return scope.TaskID != "" && patch.ScopeKey == scope.TaskID
	default:
		return false
	}
}

func sanitizePatchText(text string) string {
	text = strings.TrimSpace(text)
	text, _ = memoryredact.Redact(text)
	if runes := []rune(text); len(runes) > maxEpisodeTextRunes {
		text = string(runes[:maxEpisodeTextRunes])
	}
	return text
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func normalizeStringList(values []string, limit int) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 512 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func metadataString(metadata map[string]interface{}, key, fallback string) string {
	if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func metadataStringSlice(metadata map[string]interface{}, key string) []string {
	raw, ok := metadata[key]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return normalizeStringList(values, 64)
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return normalizeStringList(out, 64)
	default:
		return nil
	}
}

func metadataFloat(metadata map[string]interface{}, key string, fallback float64) float64 {
	if value, ok := metadata[key].(float64); ok {
		return value
	}
	return fallback
}

func metadataBool(metadata map[string]interface{}, key string, fallback bool) bool {
	if value, ok := metadata[key].(bool); ok {
		return value
	}
	return fallback
}

func (s *Store) getPatchRaw(ctx context.Context, id, projectKey string) (MemoryPatch, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MemoryPatch{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, operation,
       target_ids_json, proposed_content, proposed_metadata_json, reason, evidence_json,
       risk_level, status, expected_revisions_json, applied_record_ids_json,
       applied_revisions_json, rollback_versions_json, created_by, reviewed_by,
       review_note, error, created_at, COALESCE(reviewed_at, ''),
       COALESCE(applied_at, ''), COALESCE(rolled_back_at, '')
FROM memory_patches
WHERE id = ? AND project_key = ?
`, strings.TrimSpace(id), strings.TrimSpace(projectKey))
	return scanPatch(row)
}

func (s *Store) getPatchTx(ctx context.Context, tx *sql.Tx, id string) (MemoryPatch, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, tenant_id, project_key, scope_kind, scope_key, operation,
       target_ids_json, proposed_content, proposed_metadata_json, reason, evidence_json,
       risk_level, status, expected_revisions_json, applied_record_ids_json,
       applied_revisions_json, rollback_versions_json, created_by, reviewed_by,
       review_note, error, created_at, COALESCE(reviewed_at, ''),
       COALESCE(applied_at, ''), COALESCE(rolled_back_at, '')
FROM memory_patches
WHERE id = ?
`, strings.TrimSpace(id))
	return scanPatch(row)
}

func scanPatch(row rowScanner) (MemoryPatch, error) {
	var (
		patch                              MemoryPatch
		id, tenantID, projectKey           sql.NullString
		scopeKind, scopeKey, operation     sql.NullString
		targetsJSON, proposedContent       sql.NullString
		metadataJSON, reason, evidenceJSON sql.NullString
		risk, status, revisionsJSON        sql.NullString
		appliedIDsJSON, appliedRevsJSON    sql.NullString
		rollbackJSON, createdBy            sql.NullString
		reviewedBy, reviewNote, errorText  sql.NullString
		createdAt, reviewedAt              sql.NullString
		appliedAt, rolledBackAt            sql.NullString
	)
	if err := row.Scan(
		&id, &tenantID, &projectKey, &scopeKind, &scopeKey, &operation,
		&targetsJSON, &proposedContent, &metadataJSON, &reason, &evidenceJSON,
		&risk, &status, &revisionsJSON, &appliedIDsJSON, &appliedRevsJSON,
		&rollbackJSON, &createdBy, &reviewedBy, &reviewNote, &errorText,
		&createdAt, &reviewedAt, &appliedAt, &rolledBackAt,
	); err != nil {
		return MemoryPatch{}, err
	}
	patch = MemoryPatch{
		ID: id.String, TenantID: tenantID.String, ProjectKey: projectKey.String,
		ScopeKind: Scope(scopeKind.String), ScopeKey: scopeKey.String,
		Operation: PatchOperation(operation.String), ProposedContent: proposedContent.String,
		Reason: reason.String, RiskLevel: PatchRisk(risk.String),
		Status: PatchStatus(status.String), CreatedBy: createdBy.String,
		ReviewedBy: reviewedBy.String, ReviewNote: reviewNote.String,
		Error: errorText.String, CreatedAt: parseTime(createdAt.String),
		ReviewedAt: parseTime(reviewedAt.String), AppliedAt: parseTime(appliedAt.String),
		RolledBackAt: parseTime(rolledBackAt.String),
	}
	_ = json.Unmarshal([]byte(targetsJSON.String), &patch.TargetIDs)
	_ = json.Unmarshal([]byte(metadataJSON.String), &patch.ProposedMetadata)
	_ = json.Unmarshal([]byte(evidenceJSON.String), &patch.Evidence)
	_ = json.Unmarshal([]byte(revisionsJSON.String), &patch.ExpectedRevisions)
	_ = json.Unmarshal([]byte(appliedIDsJSON.String), &patch.AppliedRecordIDs)
	_ = json.Unmarshal([]byte(appliedRevsJSON.String), &patch.AppliedRevisions)
	_ = json.Unmarshal([]byte(rollbackJSON.String), &patch.RollbackVersions)
	return patch, nil
}

func (s *Store) insertPatchEvent(ctx context.Context, patch MemoryPatch, eventType, actor string, payload map[string]interface{}) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertPatchAuditTx(ctx, tx, patch, eventType, actor, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func insertPatchAuditTx(ctx context.Context, tx *sql.Tx, patch MemoryPatch, eventType, actor string, payload map[string]interface{}) error {
	payload = cloneMap(payload)
	payload["patch_operation"] = string(patch.Operation)
	payload["risk_level"] = string(patch.RiskLevel)
	_, err := insertEventIDTx(ctx, tx, patch.ProjectKey, patch.ID, eventType, actor, "", 1, payload)
	return err
}
