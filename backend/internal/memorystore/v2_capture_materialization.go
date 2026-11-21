package memorystore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// TurnCapture is the memory-store projection of a canonical turn capture
// receipt. It deliberately contains no transcript text.
type TurnCapture struct {
	ID                 string
	ProjectKey         string
	SessionID          string
	SessionGeneration  int64
	TurnID             string
	MessageSeqStart    int64
	MessageSeqEnd      int64
	CanonicalRevision  int64
	RangeHash          string
	TraceID            string
	UserMessageID      string
	AssistantMessageID string
	Outcome            string
	Steps              int
	UsageJSON          string
	CreatedAt          time.Time
}

type TurnCaptureMaterialization struct {
	DigestID string
	JobID    string
	JobKey   string
}

// TurnCaptureDigestText is the bounded, redacted text projection that may be
// stored alongside a capture-backed digest when the caller's text policy
// permits it. It is committed atomically with extraction output.
type TurnCaptureDigestText struct {
	UserText      string
	AssistantText string
	TextTruncated bool
}

// MaterializeTurnCapture atomically creates the digest and its executable
// phase-1 job. Replaying an unacknowledged canonical receipt reuses both rows.
func (s *Store) MaterializeTurnCapture(ctx context.Context, capture TurnCapture) (TurnCaptureMaterialization, error) {
	if s == nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return TurnCaptureMaterialization{}, err
	}
	capture.ProjectKey = strings.TrimSpace(capture.ProjectKey)
	if capture.ProjectKey == "" {
		capture.ProjectKey = strings.TrimSpace(s.cfg.ProjectKey)
	}
	capture.SessionID = strings.TrimSpace(capture.SessionID)
	capture.TurnID = strings.TrimSpace(capture.TurnID)
	capture.ID = strings.TrimSpace(capture.ID)
	capture.RangeHash = strings.TrimSpace(capture.RangeHash)
	if capture.ProjectKey == "" || capture.SessionID == "" || capture.TurnID == "" ||
		capture.ID == "" || capture.MessageSeqStart <= 0 || capture.MessageSeqEnd < capture.MessageSeqStart ||
		capture.CanonicalRevision <= 0 || capture.RangeHash == "" {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: incomplete turn capture receipt")
	}
	if capture.Outcome == "" {
		capture.Outcome = "success"
	}
	if capture.UsageJSON == "" {
		capture.UsageJSON = "{}"
	}
	if capture.CreatedAt.IsZero() {
		capture.CreatedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	digestID := "td_" + uuid.NewString()
	jobID := "job_" + uuid.NewString()
	jobKey := TurnCaptureJobKey(capture)
	digestHash := turnCaptureDigestHash(capture)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: begin turn capture materialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, capture.ProjectKey); err != nil {
		return TurnCaptureMaterialization{}, err
	}
	if err := checkSessionPurgeGenerationTx(ctx, tx, capture.ProjectKey,
		capture.SessionID, capture.SessionGeneration); err != nil {
		return TurnCaptureMaterialization{}, err
	}
	var existingDigestID, existingReceiptID, existingRangeHash, existingSessionID, existingTurnID string
	var existingSeqStart, existingSeqEnd, existingRevision, existingGeneration int64
	err = tx.QueryRowContext(ctx, `
SELECT d.id, COALESCE(d.capture_receipt_id, ''), d.message_seq_start, d.message_seq_end,
       d.source_revision, COALESCE(d.range_hash, ''), d.session_id, d.turn_id,
       COALESCE(g.generation, d.session_generation)
FROM memory_turn_digests
       AS d
LEFT JOIN memory_turn_capture_generations g
  ON g.project_key = d.project_key AND g.capture_receipt_id = d.capture_receipt_id
WHERE d.project_key = ? AND d.capture_receipt_id = ?
`, capture.ProjectKey, capture.ID).Scan(
		&existingDigestID, &existingReceiptID, &existingSeqStart, &existingSeqEnd,
		&existingRevision, &existingRangeHash, &existingSessionID, &existingTurnID,
		&existingGeneration,
	)
	if err != nil && err != sql.ErrNoRows {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: find turn digest: %w", err)
	}
	if existingDigestID != "" {
		if existingReceiptID != capture.ID || existingSessionID != capture.SessionID ||
			existingTurnID != capture.TurnID || existingGeneration != capture.SessionGeneration ||
			existingSeqStart != capture.MessageSeqStart ||
			existingSeqEnd != capture.MessageSeqEnd ||
			existingRevision != capture.CanonicalRevision ||
			existingRangeHash != capture.RangeHash {
			return TurnCaptureMaterialization{}, fmt.Errorf(
				"memorystore: capture receipt identity conflicts with existing turn digest for session %s turn %s",
				capture.SessionID, capture.TurnID,
			)
		}
		digestID = existingDigestID
	}
	// A session ID may be recreated, but one turn identity must never span
	// incarnations.  Receipt-backed revisions within the same generation are
	// allowed; a different generation is a hard identity conflict.
	var otherTurnGeneration int64
	err = tx.QueryRowContext(ctx, `
SELECT session_generation
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND turn_id = ?
  AND capture_receipt_id <> ?
LIMIT 1
`, capture.ProjectKey, capture.SessionID, capture.TurnID, capture.ID).Scan(&otherTurnGeneration)
	if err != nil && err != sql.ErrNoRows {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: check turn generation: %w", err)
	}
	if err == nil && otherTurnGeneration != capture.SessionGeneration {
		return TurnCaptureMaterialization{}, fmt.Errorf(
			"memorystore: capture receipt identity conflicts with existing turn digest for session %s turn %s",
			capture.SessionID, capture.TurnID,
		)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_turn_digests (
     id, scope, project_key, session_id, turn_id, trace_id,
     session_generation,
     parent_session_id, agent_type, user_message_id, assistant_message_id,
    message_seq_start, message_seq_end, capture_receipt_id, source_revision,
    range_hash, enqueued_at, user_text, assistant_text, text_truncated,
    outcome, steps, usage_json, metadata_json, digest_hash, digest_revision,
    status, process_job_id, last_error, rollup_status, rollup_window_id,
    rolled_up_at, created_at, updated_at, processed_at
 ) VALUES (?, 'project', ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, ?, ?, ?, '', '', 0,
           ?, ?, ?, ?, ?, 1, 'pending', NULL, NULL, 'pending', NULL, NULL, ?, ?, NULL)
ON CONFLICT(project_key, capture_receipt_id) WHERE capture_receipt_id <> '' DO NOTHING
	`, digestID, capture.ProjectKey, capture.SessionID, capture.TurnID,
		capture.TraceID, capture.SessionGeneration, capture.UserMessageID, capture.AssistantMessageID,
		capture.MessageSeqStart, capture.MessageSeqEnd, capture.ID,
		capture.CanonicalRevision, capture.RangeHash, now.Format(time.RFC3339Nano),
		capture.Outcome, capture.Steps, capture.UsageJSON, capture.UsageJSON,
		digestHash, capture.CreatedAt.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano)); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: materialize turn digest: %w", err)
	}
	// Resolve the ID from storage after INSERT so the caller always receives
	// the persisted row's ID, including when ON CONFLICT ignored our candidate.
	if err := tx.QueryRowContext(ctx, `
SELECT id, COALESCE(capture_receipt_id, ''), message_seq_start, message_seq_end,
       source_revision, COALESCE(range_hash, ''), session_id, turn_id, session_generation
FROM memory_turn_digests
WHERE project_key = ? AND capture_receipt_id = ?
`, capture.ProjectKey, capture.ID).Scan(
		&existingDigestID, &existingReceiptID, &existingSeqStart, &existingSeqEnd,
		&existingRevision, &existingRangeHash, &existingSessionID, &existingTurnID,
		&existingGeneration,
	); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: resolve turn digest: %w", err)
	}
	if existingReceiptID != capture.ID || existingSessionID != capture.SessionID ||
		existingTurnID != capture.TurnID || existingGeneration != capture.SessionGeneration ||
		existingSeqStart != capture.MessageSeqStart ||
		existingSeqEnd != capture.MessageSeqEnd ||
		existingRevision != capture.CanonicalRevision ||
		existingRangeHash != capture.RangeHash {
		return TurnCaptureMaterialization{}, fmt.Errorf(
			"memorystore: capture receipt identity conflicts with existing turn digest for session %s turn %s",
			capture.SessionID, capture.TurnID,
		)
	}
	digestID = existingDigestID
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_jobs (
    id, kind, job_key, scope, project_key, status, priority, attempt,
    max_attempts, retry_at, last_error, input_watermark, created_at, updated_at,
    session_id, session_generation, capture_receipt_id, source_start_seq,
    source_end_seq, source_revision, source_hash, available_at, repair_only
) VALUES (?, 'phase1_extract/v2', ?, 'project', ?, 'pending', 0, 0, 3,
           NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
`, jobID, jobKey, capture.ProjectKey, capture.ID,
		capture.CreatedAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		capture.SessionID, capture.SessionGeneration, capture.ID, capture.MessageSeqStart,
		capture.MessageSeqEnd, capture.CanonicalRevision, capture.RangeHash,
		now.Format(time.RFC3339Nano)); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: materialize turn job: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT id FROM memory_jobs
WHERE kind = 'phase1_extract/v2' AND scope = 'project' AND project_key = ? AND job_key = ?
`, capture.ProjectKey, jobKey).Scan(&jobID); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: resolve turn job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET process_job_id = ?, enqueued_at = COALESCE(enqueued_at, ?), updated_at = ?
WHERE id = ? AND project_key = ? AND capture_receipt_id = ?
`, jobID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		digestID, capture.ProjectKey, capture.ID); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: link turn job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
 INSERT INTO memory_turn_capture_generations(
     project_key, capture_receipt_id, session_id, turn_id, generation, created_at
 ) VALUES (?, ?, ?, ?, ?, ?)
 ON CONFLICT(project_key, capture_receipt_id) DO NOTHING
 `, capture.ProjectKey, capture.ID, capture.SessionID, capture.TurnID,
		capture.SessionGeneration, now.Format(time.RFC3339Nano)); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: bind turn capture generation: %w", err)
	}
	var storedCaptureSession, storedCaptureTurn string
	var storedCaptureGeneration int64
	if err := tx.QueryRowContext(ctx, `
 SELECT session_id, turn_id, generation
 FROM memory_turn_capture_generations
 WHERE project_key = ? AND capture_receipt_id = ?
 `, capture.ProjectKey, capture.ID).Scan(
		&storedCaptureSession, &storedCaptureTurn, &storedCaptureGeneration,
	); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: resolve turn capture generation: %w", err)
	}
	if storedCaptureSession != capture.SessionID || storedCaptureTurn != capture.TurnID ||
		storedCaptureGeneration != capture.SessionGeneration {
		return TurnCaptureMaterialization{}, fmt.Errorf(
			"memorystore: turn capture generation identity conflicts for receipt %s",
			capture.ID)
	}
	if err := tx.Commit(); err != nil {
		return TurnCaptureMaterialization{}, fmt.Errorf("memorystore: commit turn capture materialization: %w", err)
	}
	return TurnCaptureMaterialization{DigestID: digestID, JobID: jobID, JobKey: jobKey}, nil
}

func candidateRecord(candidate Candidate) (Record, error) {
	text := strings.TrimSpace(candidate.Text)
	if text == "" {
		return Record{}, fmt.Errorf("memorystore: candidate text is required")
	}
	projectKey := strings.TrimSpace(candidate.ProjectKey)
	kind := strings.TrimSpace(candidate.Kind)
	if kind == "" {
		return Record{}, fmt.Errorf("memorystore: candidate kind is required")
	}
	now := time.Now().UTC()
	sourceRefs := append([]string(nil), candidate.SourceRefs...)
	if len(sourceRefs) == 0 {
		sourceRefs = []string{"session:" + strings.TrimSpace(candidate.SessionID)}
	}
	metadata := map[string]interface{}{
		"model":       candidate.Model,
		"token_usage": candidate.TokenUsage,
		"sensitivity": firstNonEmpty(candidate.Sensitivity, "internal"),
	}
	for key, value := range candidate.Metadata {
		metadata[key] = value
	}
	record := Record{
		ID:                      "mem_" + uuid.NewString(),
		Scope:                   ScopeProject,
		ProjectKey:              projectKey,
		Kind:                    kind,
		Status:                  StatusCandidate,
		Text:                    text,
		NormalizedText:          normalizeMemoryText(text),
		Tags:                    normalizeTags(candidate.Tags),
		Confidence:              candidate.Confidence,
		Sensitivity:             normalizeSensitivity(firstNonEmpty(candidate.Sensitivity, "internal")),
		Pinned:                  false,
		RetentionClass:          firstNonEmpty(candidate.RetentionClass, "standard"),
		SourceKind:              firstNonEmpty(candidate.SourceKind, "session_extract"),
		SourceSessionID:         strings.TrimSpace(candidate.SessionID),
		SourceSessionGeneration: candidate.SessionGeneration,
		SourceTurnID:            strings.TrimSpace(candidate.TurnID),
		SourceWindow:            strings.TrimSpace(candidate.SourceWindow),
		SourceRefs:              sourceRefs,
		SourceHash:              candidate.SourceHash,
		SourceRevision:          candidate.SourceRevision,
		SourceUpdatedAt:         candidate.SourceUpdatedAt,
		SourceObservedAt:        now,
		SourceMessageIDs:        append([]string(nil), candidate.SourceMessageIDs...),
		ContentHash:             memoryContentHash(string(ScopeProject), projectKey, kind, text),
		Revision:                1,
		CreatedAt:               now,
		UpdatedAt:               now,
		Freshness:               FreshnessFresh,
		LastVerifiedAt:          now,
		RevalidateAfter:         candidate.RevalidateAfter,
		Metadata:                metadata,
	}
	return record, nil
}

// CommitTurnCaptureExtraction commits provenance records, the per-turn output
// receipt, digest completion and job success in one transaction. It rejects a
// stale job owner or a changed source identity before any candidate is visible.
func (s *Store) CommitTurnCaptureExtraction(
	ctx context.Context,
	jobID, workerID string,
	claimAttempt int,
	capture TurnCapture,
	digestID, outputHash string,
	candidates []Candidate,
) (int, error) {
	return s.commitTurnCaptureExtraction(ctx, jobID, workerID, claimAttempt, capture,
		digestID, outputHash, TurnCaptureDigestText{}, candidates)
}

// CommitTurnCaptureExtractionWithDigestText commits extraction output and its
// optional bounded digest text in the same transaction. The text must already
// be redacted and truncated by the runtime.
func (s *Store) CommitTurnCaptureExtractionWithDigestText(
	ctx context.Context,
	jobID, workerID string,
	claimAttempt int,
	capture TurnCapture,
	digestID, outputHash string,
	digestText TurnCaptureDigestText,
	candidates []Candidate,
) (int, error) {
	return s.commitTurnCaptureExtraction(ctx, jobID, workerID, claimAttempt, capture,
		digestID, outputHash, digestText, candidates)
}

func (s *Store) commitTurnCaptureExtraction(
	ctx context.Context,
	jobID, workerID string,
	claimAttempt int,
	capture TurnCapture,
	digestID, outputHash string,
	digestText TurnCaptureDigestText,
	candidates []Candidate,
) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	jobID = strings.TrimSpace(jobID)
	workerID = strings.TrimSpace(workerID)
	digestID = strings.TrimSpace(digestID)
	if jobID == "" || workerID == "" || claimAttempt <= 0 || digestID == "" {
		return 0, fmt.Errorf("memorystore: turn extraction commit requires job, worker, claim attempt and digest ids")
	}
	outputHash = strings.TrimSpace(outputHash)
	if outputHash == "" {
		return 0, fmt.Errorf("memorystore: turn extraction commit requires output hash")
	}
	capture.ProjectKey = strings.TrimSpace(capture.ProjectKey)
	if capture.ProjectKey == "" {
		capture.ProjectKey = strings.TrimSpace(s.cfg.ProjectKey)
	}
	if capture.ProjectKey == "" {
		return 0, fmt.Errorf("memorystore: turn extraction commit requires project key")
	}
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, fmt.Errorf("memorystore: begin turn extraction commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, capture.ProjectKey); err != nil {
		return 0, err
	}
	if err := checkSessionPurgeGenerationTx(ctx, tx, capture.ProjectKey,
		capture.SessionID, capture.SessionGeneration); err != nil {
		return 0, err
	}
	// Every phase-1/v2 commit must be backed by the receipt-to-incarnation
	// binding written by MaterializeTurnCapture.  Do not silently accept a
	// migrated or manually damaged digest/job pair without that binding:
	// otherwise a stale worker could commit provenance after the binding that
	// scopes purge fencing has disappeared.
	var boundSessionID, boundTurnID string
	var boundGeneration int64
	err = tx.QueryRowContext(ctx, `
	SELECT session_id, turn_id, generation
	FROM memory_turn_capture_generations
	WHERE project_key = ? AND capture_receipt_id = ?
	`, capture.ProjectKey, capture.ID).Scan(
		&boundSessionID, &boundTurnID, &boundGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("memorystore: turn capture generation binding missing")
	}
	if err != nil {
		return 0, fmt.Errorf("memorystore: read turn capture generation: %w", err)
	}
	if boundSessionID != capture.SessionID || boundTurnID != capture.TurnID ||
		boundGeneration != capture.SessionGeneration {
		return 0, fmt.Errorf("memorystore: turn capture generation identity changed before commit")
	}
	var outputSessionID, outputTurnID, outputReceiptID, outputDigestID, outputJobID string
	var outputRevision, outputCandidateCount int64
	var outputRangeHash, storedOutputHash string
	err = tx.QueryRowContext(ctx, `
	SELECT session_id, turn_id, capture_receipt_id, digest_id, job_id,
	       source_revision, range_hash, output_hash, candidate_count
	FROM memory_turn_output_receipts
	WHERE project_key = ? AND capture_receipt_id = ?
	  AND source_revision = ? AND range_hash = ?
	`, capture.ProjectKey, capture.ID, capture.CanonicalRevision, capture.RangeHash).Scan(
		&outputSessionID, &outputTurnID, &outputReceiptID, &outputDigestID, &outputJobID,
		&outputRevision, &outputRangeHash, &storedOutputHash, &outputCandidateCount,
	)
	if err == nil {
		if outputSessionID != capture.SessionID || outputTurnID != capture.TurnID ||
			outputReceiptID != capture.ID || outputDigestID != digestID || outputJobID != jobID ||
			outputRevision != capture.CanonicalRevision || outputRangeHash != capture.RangeHash ||
			storedOutputHash != outputHash {
			return 0, fmt.Errorf("memorystore: existing turn output receipt conflicts with extraction replay")
		}
		return int(outputCandidateCount), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("memorystore: read turn output receipt: %w", err)
	}
	var jobStatus, jobWorker, jobLeaseUntil string
	var jobSessionID, jobReceiptID, jobSourceHash string
	var jobGeneration, jobSourceStart, jobSourceEnd, jobSourceRevision int64
	var storedAttempt int
	if err := tx.QueryRowContext(ctx, `
	SELECT status, COALESCE(worker_id, ''), COALESCE(lease_until, ''), attempt,
	       session_id, session_generation, capture_receipt_id, source_start_seq,
	       source_end_seq, source_revision, source_hash
	FROM memory_jobs
	WHERE id = ? AND kind = 'phase1_extract/v2' AND project_key = ?
	`, jobID, capture.ProjectKey).Scan(
		&jobStatus, &jobWorker, &jobLeaseUntil, &storedAttempt,
		&jobSessionID, &jobGeneration, &jobReceiptID, &jobSourceStart,
		&jobSourceEnd, &jobSourceRevision, &jobSourceHash,
	); err != nil {
		return 0, fmt.Errorf("memorystore: read turn extraction job: %w", err)
	}
	if jobSessionID != capture.SessionID || jobGeneration != capture.SessionGeneration ||
		jobReceiptID != capture.ID || jobSourceStart != capture.MessageSeqStart ||
		jobSourceEnd != capture.MessageSeqEnd || jobSourceRevision != capture.CanonicalRevision ||
		jobSourceHash != capture.RangeHash {
		return 0, fmt.Errorf("memorystore: turn extraction job source identity changed")
	}
	if jobStatus != string(JobStatusRunning) || jobWorker != workerID || storedAttempt != claimAttempt || jobLeaseUntil == "" ||
		jobLeaseUntil <= now.Format(time.RFC3339Nano) {
		return 0, fmt.Errorf("memorystore: turn extraction job %s lease claim is no longer owned", jobID)
	}
	var storedDigestID, storedReceiptID, storedProcessJobID, status, storedRangeHash string
	var storedRevision, storedGeneration, storedSeqStart, storedSeqEnd int64
	if err := tx.QueryRowContext(ctx, `
	SELECT id, COALESCE(capture_receipt_id, ''), source_revision, COALESCE(range_hash, ''),
	       COALESCE(process_job_id, ''), status, session_generation,
	       message_seq_start, message_seq_end
	FROM memory_turn_digests
	WHERE project_key = ? AND session_id = ? AND session_generation = ?
	  AND turn_id = ? AND capture_receipt_id = ?
	`, capture.ProjectKey, capture.SessionID, capture.SessionGeneration, capture.TurnID, capture.ID).Scan(
		&storedDigestID, &storedReceiptID, &storedRevision, &storedRangeHash,
		&storedProcessJobID, &status, &storedGeneration, &storedSeqStart, &storedSeqEnd,
	); err != nil {
		return 0, fmt.Errorf("memorystore: read turn digest for extraction: %w", err)
	}
	if storedDigestID != digestID || storedReceiptID != capture.ID ||
		storedProcessJobID != jobID ||
		storedGeneration != capture.SessionGeneration ||
		storedSeqStart != capture.MessageSeqStart || storedSeqEnd != capture.MessageSeqEnd ||
		storedRevision != capture.CanonicalRevision || storedRangeHash != capture.RangeHash ||
		(status != string(TurnDigestPending) && status != string(TurnDigestProcessing)) {
		return 0, fmt.Errorf("memorystore: turn digest source identity changed before commit")
	}
	inserted := 0
	for _, candidate := range candidates {
		record, err := candidateRecord(candidate)
		if err != nil {
			return 0, err
		}
		if record.ProjectKey != capture.ProjectKey || record.SourceSessionID != capture.SessionID ||
			candidate.SessionGeneration != capture.SessionGeneration ||
			record.SourceTurnID != capture.TurnID || record.SourceRevision != capture.CanonicalRevision ||
			record.SourceHash != capture.RangeHash {
			return 0, fmt.Errorf("memorystore: candidate provenance does not match capture receipt")
		}
		if err := s.insertRecordSQLTx(ctx, tx, record, "agent", capture.SessionID); err != nil {
			if isUniqueConstraint(err) || strings.Contains(strings.ToLower(err.Error()), "suppressed") ||
				strings.Contains(strings.ToLower(err.Error()), "secret-shaped") {
				continue
			}
			return 0, err
		}
		inserted++
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_turn_output_receipts (
    id, project_key, session_id, turn_id, capture_receipt_id, digest_id, job_id,
    source_revision, range_hash, output_hash, candidate_count, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(project_key, capture_receipt_id, source_revision, range_hash) DO NOTHING
`, "tout_"+turnCaptureOutputID(capture), capture.ProjectKey, capture.SessionID, capture.TurnID,
		capture.ID, digestID, jobID, capture.CanonicalRevision, capture.RangeHash,
		outputHash, inserted, now.Format(time.RFC3339Nano)); err != nil {
		return 0, fmt.Errorf("memorystore: write turn output receipt: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_jobs
	SET status = 'succeeded', worker_id = NULL, lease_until = NULL,
	    deadline_at = NULL, heartbeat_at = NULL, finished_at = ?,
    last_success_watermark = ?, updated_at = ?
WHERE id = ? AND status = 'running' AND worker_id = ? AND attempt = ? AND kind = 'phase1_extract/v2'
	`, now.Format(time.RFC3339Nano), nullableString(outputHash), now.Format(time.RFC3339Nano), jobID, workerID, claimAttempt)
	if err != nil {
		return 0, fmt.Errorf("memorystore: complete turn extraction job: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return 0, fmt.Errorf("memorystore: turn extraction job %s lost ownership", jobID)
	}
	res, err = tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET user_text = ?, assistant_text = ?, text_truncated = ?,
    status = 'done', process_job_id = ?, last_error = NULL,
    processed_at = ?, updated_at = ?
WHERE id = ? AND project_key = ? AND capture_receipt_id = ?
  AND source_revision = ? AND range_hash = ?
  AND status IN ('pending', 'processing')
`, digestText.UserText, digestText.AssistantText, boolInt(digestText.TextTruncated),
		jobID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		digestID, capture.ProjectKey, capture.ID, capture.CanonicalRevision, capture.RangeHash)
	if err != nil {
		return 0, fmt.Errorf("memorystore: complete turn digest: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return 0, fmt.Errorf("memorystore: turn digest changed during extraction commit")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("memorystore: commit turn extraction result: %w", err)
	}
	return inserted, nil
}

func turnCaptureDigestHash(capture TurnCapture) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		capture.ProjectKey, capture.SessionID, capture.TurnID,
		fmt.Sprintf("%d", capture.MessageSeqStart),
		fmt.Sprintf("%d", capture.MessageSeqEnd),
		fmt.Sprintf("%d", capture.CanonicalRevision), capture.RangeHash,
		capture.TraceID, capture.Outcome,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func turnCaptureOutputID(capture TurnCapture) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(capture.ProjectKey),
		strings.TrimSpace(capture.ID),
		fmt.Sprintf("%d", capture.CanonicalRevision),
		strings.TrimSpace(capture.RangeHash),
	}, "\x00")))
	return "tout_" + hex.EncodeToString(sum[:16])
}

// TurnCaptureJobKey is the stable, revision-qualified identity for the
// receipt-backed extraction job.  It intentionally includes the session
// incarnation and exact canonical range so a replay of the same turn after a
// rewrite cannot alias an older job.
func TurnCaptureJobKey(capture TurnCapture) string {
	return fmt.Sprintf(
		"project:%s:session:%s:generation:%d:turn:%s:revision:%d:range:%d-%d:%s",
		strings.TrimSpace(capture.ProjectKey),
		strings.TrimSpace(capture.SessionID),
		capture.SessionGeneration,
		strings.TrimSpace(capture.TurnID),
		capture.CanonicalRevision,
		capture.MessageSeqStart,
		capture.MessageSeqEnd,
		strings.TrimSpace(capture.RangeHash),
	)
}
