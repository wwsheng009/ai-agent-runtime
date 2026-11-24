package team

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *SQLiteStore) CreateTeamDefinition(ctx context.Context, request TeamDefinitionCreateRequest) (*TeamDefinition, error) {
	ownerID, err := validateTeamResourceOwner(request.OwnerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := validateTeamResourceWorkspace(request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	name, err := validateTeamDefinitionName(request.Name)
	if err != nil {
		return nil, err
	}
	description, err := validateTeamDefinitionDescription(request.Description)
	if err != nil {
		return nil, err
	}
	config, err := normalizeTeamResourceJSON(request.Config, maxTeamDefinitionConfigBytes)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	definition := &TeamDefinition{
		ID:          "tdef_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		OwnerID:     ownerID,
		WorkspaceID: workspaceID,
		Name:        name,
		Description: description,
		Version:     1,
		Config:      config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_definitions (
			id, owner_id, workspace_id, name, description, version, config_json,
			archived_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
	`, definition.ID, definition.OwnerID, definition.WorkspaceID, definition.Name,
		definition.Description, definition.Version, string(definition.Config),
		formatTime(now), formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("insert Team Definition: %w", err)
	}
	return definition, nil
}

func (s *SQLiteStore) GetTeamDefinition(ctx context.Context, ownerID, definitionID string) (*TeamDefinition, error) {
	ownerID, err := validateTeamResourceOwner(ownerID)
	if err != nil {
		return nil, err
	}
	definitionID = strings.TrimSpace(definitionID)
	if definitionID == "" {
		return nil, fmt.Errorf("%w: definition_id is required", ErrTeamResourceInvalid)
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return nil, nil
	}
	return loadTeamDefinition(ctx, s.db, ownerID, definitionID)
}

func (s *SQLiteStore) ListTeamDefinitions(ctx context.Context, ownerID, workspaceID string, limit int) ([]TeamDefinition, error) {
	ownerID, err := validateTeamResourceOwner(ownerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err = validateTeamResourceWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, fmt.Errorf("%w: limit cannot be negative", ErrTeamResourceInvalid)
	}
	if limit > 500 {
		limit = 500
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return []TeamDefinition{}, nil
	}

	query := `
		SELECT id, owner_id, workspace_id, name, description, version, config_json,
			archived_at, created_at, updated_at
		FROM team_definitions
		WHERE owner_id = ? AND workspace_id = ? AND archived_at IS NULL
		ORDER BY updated_at DESC, id ASC
	`
	args := []interface{}{ownerID, workspaceID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list Team Definitions: %w", err)
	}
	defer rows.Close()
	definitions := make([]TeamDefinition, 0)
	for rows.Next() {
		definition, err := scanTeamDefinition(rows)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func (s *SQLiteStore) UpdateTeamDefinition(ctx context.Context, request TeamDefinitionUpdateRequest) (*TeamDefinition, error) {
	ownerID, err := validateTeamResourceOwner(request.OwnerID)
	if err != nil {
		return nil, err
	}
	definitionID := strings.TrimSpace(request.ID)
	if definitionID == "" || request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: definition_id and positive expected_version are required", ErrTeamResourceInvalid)
	}
	name, err := validateTeamDefinitionName(request.Name)
	if err != nil {
		return nil, err
	}
	description, err := validateTeamDefinitionDescription(request.Description)
	if err != nil {
		return nil, err
	}
	config, err := normalizeTeamResourceJSON(request.Config, maxTeamDefinitionConfigBytes)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE team_definitions
		SET name = ?, description = ?, config_json = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND version = ? AND archived_at IS NULL
	`, name, description, string(config), formatTime(now), definitionID, ownerID, request.ExpectedVersion)
	if err != nil {
		return nil, fmt.Errorf("update Team Definition: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count updated Team Definition rows: %w", err)
	}
	if affected == 0 {
		current, err := loadTeamDefinition(ctx, s.db, ownerID, definitionID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, ErrTeamDefinitionNotFound
		}
		if current.ArchivedAt != nil {
			return nil, ErrTeamDefinitionArchived
		}
		return nil, fmt.Errorf("%w: expected %d, current %d", ErrTeamDefinitionVersionConflict, request.ExpectedVersion, current.Version)
	}
	return loadTeamDefinition(ctx, s.db, ownerID, definitionID)
}

func (s *SQLiteStore) CreateTeamRunDraft(ctx context.Context, request TeamRunCreateRequest) (*TeamRun, error) {
	ownerID, err := validateTeamResourceOwner(request.OwnerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := validateTeamResourceWorkspace(request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	goal, err := validateTeamRunGoal(request.Goal)
	if err != nil {
		return nil, err
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > maxTeamResourceKeyLength {
		return nil, fmt.Errorf("%w: idempotency_key is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceKeyLength)
	}
	requestID := strings.TrimSpace(request.RequestID)
	if len(requestID) > maxTeamResourceKeyLength {
		return nil, fmt.Errorf("%w: request_id must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceKeyLength)
	}
	definitionID := strings.TrimSpace(request.DefinitionID)
	if definitionID == "" && request.ExpectedDefinitionVersion != 0 {
		return nil, fmt.Errorf("%w: expected_definition_version requires definition_id", ErrTeamResourceInvalid)
	}
	if definitionID != "" && request.ExpectedDefinitionVersion <= 0 {
		return nil, fmt.Errorf("%w: positive expected_definition_version is required with definition_id", ErrTeamResourceInvalid)
	}
	var inlineConfig json.RawMessage
	if definitionID == "" {
		inlineConfig, err = normalizeTeamResourceJSON(request.Config, maxTeamDefinitionConfigBytes)
		if err != nil {
			return nil, err
		}
	} else if len(bytesTrimSpace(request.Config)) > 0 {
		return nil, fmt.Errorf("%w: supply either definition_id or inline config, not both", ErrTeamResourceInvalid)
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}

	keyHash := digestTeamResource([]byte("create_team_run_draft\x00" + idempotencyKey))
	requestBody := struct {
		Action            string          `json:"action"`
		OwnerID           string          `json:"owner_id"`
		WorkspaceID       string          `json:"workspace_id"`
		DefinitionID      string          `json:"definition_id,omitempty"`
		DefinitionVersion int64           `json:"definition_version,omitempty"`
		Goal              string          `json:"goal"`
		Config            json.RawMessage `json:"config,omitempty"`
	}{
		Action:            "create_team_run_draft",
		OwnerID:           ownerID,
		WorkspaceID:       workspaceID,
		DefinitionID:      definitionID,
		DefinitionVersion: request.ExpectedDefinitionVersion,
		Goal:              goal,
		Config:            inlineConfig,
	}
	requestBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode Team Run request digest: %w", err)
	}
	requestDigest := digestTeamResource(requestBytes)

	var created *TeamRun
	err = s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		existing, err := loadTeamRunByIdempotencyTx(ctx, tx, ownerID, keyHash)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.RequestDigest != requestDigest {
				return ErrTeamRunIdempotencyConflict
			}
			created = existing
			return nil
		}

		config := inlineConfig
		var definitionVersion *int64
		if definitionID != "" {
			definition, err := loadTeamDefinitionTx(ctx, tx, ownerID, definitionID)
			if err != nil {
				return err
			}
			if definition == nil {
				return ErrTeamDefinitionNotFound
			}
			if definition.ArchivedAt != nil {
				return ErrTeamDefinitionArchived
			}
			if definition.WorkspaceID != workspaceID {
				return ErrTeamDefinitionNotFound
			}
			if definition.Version != request.ExpectedDefinitionVersion {
				return fmt.Errorf("%w: expected %d, current %d", ErrTeamDefinitionVersionConflict, request.ExpectedDefinitionVersion, definition.Version)
			}
			config = definition.Config
			version := definition.Version
			definitionVersion = &version
		}

		snapshotEnvelope := teamRunSnapshotEnvelope{
			SchemaVersion:     TeamRunSnapshotSchemaVersion,
			OwnerID:           ownerID,
			WorkspaceID:       workspaceID,
			Goal:              goal,
			DefinitionID:      definitionID,
			DefinitionVersion: definitionVersion,
			Config:            config,
			RequestID:         requestID,
			IdempotencyHash:   keyHash,
		}
		snapshot, err := json.Marshal(snapshotEnvelope)
		if err != nil {
			return fmt.Errorf("encode immutable Team Run snapshot: %w", err)
		}
		if len(snapshot) > maxTeamRunSnapshotBytes {
			return fmt.Errorf("%w: Run snapshot exceeds %d bytes", ErrTeamResourceInvalid, maxTeamRunSnapshotBytes)
		}
		now := time.Now().UTC()
		runID := "trun_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_runs (
				id, owner_id, workspace_id, definition_id, definition_version, legacy_team_id,
				goal, status, phase, compatibility_mode, snapshot_schema_version,
				config_snapshot_json, snapshot_checksum, version, event_seq, summary_state,
				idempotency_key_hash, request_digest, request_id, created_at, started_at, terminal_at
			) VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, 1, 0, ?, ?, ?, ?, ?, NULL, NULL)
		`, runID, ownerID, workspaceID, nullString(definitionID), nullInt64(definitionVersion),
			goal, TeamRunStatusDraft, TeamRunPhaseDraft, TeamRunCompatibilityNone,
			TeamRunSnapshotSchemaVersion, string(snapshot), digestTeamResource(snapshot),
			TeamRunSummaryNotApplicable, keyHash, requestDigest, nullString(requestID), formatTime(now))
		if err != nil {
			return fmt.Errorf("insert Team Run draft: %w", err)
		}
		created, err = loadTeamRunTx(ctx, tx, ownerID, runID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *SQLiteStore) GetTeamRun(ctx context.Context, ownerID, runID string) (*TeamRun, error) {
	ownerID, err := validateTeamResourceOwner(ownerID)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrTeamResourceInvalid)
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return nil, nil
	}
	return loadTeamRun(ctx, s.db, ownerID, runID)
}

func (s *SQLiteStore) ListTeamRuns(ctx context.Context, ownerID, workspaceID string, limit int) ([]TeamRun, error) {
	ownerID, err := validateTeamResourceOwner(ownerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err = validateTeamResourceWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, fmt.Errorf("%w: limit cannot be negative", ErrTeamResourceInvalid)
	}
	if limit > 500 {
		limit = 500
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return []TeamRun{}, nil
	}
	query := `
		SELECT id
		FROM team_runs
		WHERE owner_id = ? AND workspace_id = ?
		ORDER BY created_at DESC, id ASC
	`
	args := []interface{}{ownerID, workspaceID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list Team Runs: %w", err)
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan Team Run id: %w", err)
		}
		runIDs = append(runIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	runs := make([]TeamRun, 0, len(runIDs))
	for _, id := range runIDs {
		run, err := loadTeamRun(ctx, s.db, ownerID, id)
		if err != nil {
			return nil, err
		}
		if run == nil {
			continue
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

type teamDefinitionRowScanner interface {
	Scan(dest ...any) error
}

func loadTeamDefinition(ctx context.Context, db *sql.DB, ownerID, definitionID string) (*TeamDefinition, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, owner_id, workspace_id, name, description, version, config_json,
			archived_at, created_at, updated_at
		FROM team_definitions WHERE owner_id = ? AND id = ?
	`, ownerID, definitionID)
	definition, err := scanTeamDefinition(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &definition, nil
}

func loadTeamDefinitionTx(ctx context.Context, tx *sql.Tx, ownerID, definitionID string) (*TeamDefinition, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, owner_id, workspace_id, name, description, version, config_json,
			archived_at, created_at, updated_at
		FROM team_definitions WHERE owner_id = ? AND id = ?
	`, ownerID, definitionID)
	definition, err := scanTeamDefinition(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &definition, nil
}

func scanTeamDefinition(row teamDefinitionRowScanner) (TeamDefinition, error) {
	var (
		definition    TeamDefinition
		config        string
		archivedAtRaw sql.NullString
		createdAtRaw  string
		updatedAtRaw  string
	)
	if err := row.Scan(
		&definition.ID, &definition.OwnerID, &definition.WorkspaceID,
		&definition.Name, &definition.Description, &definition.Version, &config,
		&archivedAtRaw, &createdAtRaw, &updatedAtRaw,
	); err != nil {
		return TeamDefinition{}, fmt.Errorf("scan Team Definition: %w", err)
	}
	definition.Config = json.RawMessage(config)
	definition.CreatedAt = parseTime(createdAtRaw)
	definition.UpdatedAt = parseTime(updatedAtRaw)
	if archivedAtRaw.Valid && strings.TrimSpace(archivedAtRaw.String) != "" {
		archived := parseTime(archivedAtRaw.String)
		definition.ArchivedAt = &archived
	}
	return definition, nil
}

func loadTeamRunByIdempotencyTx(ctx context.Context, tx *sql.Tx, ownerID, keyHash string) (*TeamRun, error) {
	var runID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM team_runs WHERE owner_id = ? AND idempotency_key_hash = ?
	`, ownerID, keyHash).Scan(&runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find Team Run idempotency record: %w", err)
	}
	return loadTeamRunTx(ctx, tx, ownerID, runID)
}

func loadTeamRun(ctx context.Context, db *sql.DB, ownerID, runID string) (*TeamRun, error) {
	return scanTeamRun(db.QueryRowContext(ctx, `
		SELECT id, owner_id, workspace_id, COALESCE(definition_id, ''), definition_version,
			COALESCE(legacy_team_id, ''), goal, status, phase, compatibility_mode,
			snapshot_schema_version, config_snapshot_json, snapshot_checksum, version,
			event_seq, summary_state, idempotency_key_hash, request_digest,
			COALESCE(request_id, ''), created_at, started_at, terminal_at,
			COALESCE(start_idempotency_key_hash, ''), COALESCE(start_request_digest, '')
		FROM team_runs WHERE owner_id = ? AND id = ?
	`, ownerID, runID))
}

func loadTeamRunTx(ctx context.Context, tx *sql.Tx, ownerID, runID string) (*TeamRun, error) {
	return scanTeamRun(tx.QueryRowContext(ctx, `
		SELECT id, owner_id, workspace_id, COALESCE(definition_id, ''), definition_version,
			COALESCE(legacy_team_id, ''), goal, status, phase, compatibility_mode,
			snapshot_schema_version, config_snapshot_json, snapshot_checksum, version,
			event_seq, summary_state, idempotency_key_hash, request_digest,
			COALESCE(request_id, ''), created_at, started_at, terminal_at,
			COALESCE(start_idempotency_key_hash, ''), COALESCE(start_request_digest, '')
		FROM team_runs WHERE owner_id = ? AND id = ?
	`, ownerID, runID))
}

type teamRunRowScanner interface {
	Scan(dest ...any) error
}

func scanTeamRun(row teamRunRowScanner) (*TeamRun, error) {
	var (
		run                TeamRun
		definitionID       sql.NullString
		definitionVersion  sql.NullInt64
		legacyTeamID       sql.NullString
		snapshot           string
		createdAtRaw       string
		startedAtRaw       sql.NullString
		terminalAtRaw      sql.NullString
		startKeyHash       string
		startRequestDigest string
	)
	err := row.Scan(
		&run.ID, &run.OwnerID, &run.WorkspaceID, &definitionID, &definitionVersion,
		&legacyTeamID, &run.Goal, &run.Status, &run.Phase, &run.CompatibilityMode,
		&run.SnapshotSchema, &snapshot, &run.SnapshotChecksum, &run.Version,
		&run.EventSeq, &run.SummaryState, &run.IdempotencyKeyHash, &run.RequestDigest,
		&run.RequestID, &createdAtRaw, &startedAtRaw, &terminalAtRaw,
		&startKeyHash, &startRequestDigest,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan Team Run: %w", err)
	}
	run.DefinitionID = definitionID.String
	if definitionVersion.Valid {
		version := definitionVersion.Int64
		run.DefinitionVersion = &version
	}
	run.LegacyTeamID = legacyTeamID.String
	run.StartIdempotencyKeyHash = startKeyHash
	run.StartRequestDigest = startRequestDigest
	run.Snapshot = json.RawMessage(snapshot)
	run.CreatedAt = parseTime(createdAtRaw)
	if startedAtRaw.Valid && strings.TrimSpace(startedAtRaw.String) != "" {
		started := parseTime(startedAtRaw.String)
		run.StartedAt = &started
	}
	if terminalAtRaw.Valid && strings.TrimSpace(terminalAtRaw.String) != "" {
		terminal := parseTime(terminalAtRaw.String)
		run.TerminalAt = &terminal
	}
	if len(run.Snapshot) == 0 || digestTeamResource(run.Snapshot) != run.SnapshotChecksum {
		return nil, ErrTeamRunSnapshotCorrupt
	}
	var envelope teamRunSnapshotEnvelope
	if err := json.Unmarshal(run.Snapshot, &envelope); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrTeamRunSnapshotCorrupt, err)
	}
	if envelope.SchemaVersion != run.SnapshotSchema ||
		envelope.SchemaVersion != TeamRunSnapshotSchemaVersion ||
		envelope.OwnerID != run.OwnerID ||
		envelope.WorkspaceID != run.WorkspaceID ||
		envelope.Goal != run.Goal ||
		envelope.DefinitionID != run.DefinitionID ||
		!sameOptionalInt64(envelope.DefinitionVersion, run.DefinitionVersion) ||
		envelope.IdempotencyHash != run.IdempotencyKeyHash ||
		envelope.RequestID != run.RequestID {
		return nil, fmt.Errorf("%w: snapshot metadata does not match Run row", ErrTeamRunSnapshotCorrupt)
	}
	if _, err := normalizeTeamResourceJSON(envelope.Config, maxTeamDefinitionConfigBytes); err != nil {
		return nil, fmt.Errorf("%w: invalid config in snapshot: %v", ErrTeamRunSnapshotCorrupt, err)
	}
	return &run, nil
}

func digestTeamResource(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bytesTrimSpace(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func nullString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullInt64(value *int64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

var _ TeamDefinitionRunStore = (*SQLiteStore)(nil)
