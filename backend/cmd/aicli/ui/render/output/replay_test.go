package output

import (
	"testing"
	"time"
)

// ============================================================================
// Phase 5 replay decoder 测试（9.5/17.5 fail-closed）
// ============================================================================

// replayFixture 构造一个已封存 record + payload source。
func replayFixture(t *testing.T, status DeliveryStatus, certainty WriteCertainty) ReplayArchiveEntry {
	t.Helper()
	rec := DeliveryRecord{
		RecordID:      "rd-1",
		SchemaVersion: SchemaVersion,
		Batch: RecordedBatch{
			SessionID:             "src-session",
			BatchID:               "src-batch",
			Sequence:              7,
			RouteEpoch:            3,
			ProjectionTargetID:    "pt-primary",
			ProjectionTargetClass: TargetClassPhysical,
			Kind:                  TransactionFrame,
			Terminal: RenderTerminalContext{
				Geometry: TerminalGeometry{Width: 20, Height: 6},
				Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
			},
		},
		Output: RecordedOutputReceipt{
			BatchID:               "src-batch",
			Sequence:              7,
			RouteEpoch:            3,
			ProjectionTargetID:    "pt-primary",
			ProjectionTargetClass: TargetClassPhysical,
			Primary: &RecordedTargetReceipt{
				BatchID:            "src-batch",
				Sequence:           7,
				Status:             status,
				Certainty:          certainty,
				ProjectionTargetID: "pt-primary",
			},
		},
		SealedAt: time.Now(),
	}
	source := CapturedDelivery{
		SchemaVersion:      SchemaVersion,
		CaptureEntryID:     "ce-1",
		BatchID:            "src-batch",
		Sequence:           7,
		ProjectionTargetID: "pt-capture",
		Mode:               RecordedFullAvailable,
		BytesLength:        len("hello replay"),
	}
	entry := ReplayArchiveEntry{
		SchemaMajor:   ReplayArchiveSchemaMajor,
		SchemaMinor:   ReplayArchiveSchemaMinor,
		Record:        rec,
		PayloadSource: source,
		Payload:       []byte("hello replay"),
	}
	entry.PayloadChecksum = ReplayArchiveChecksum(entry)
	return entry
}

// TestReplayCommittedWireOK：committed/full 可重放，identity/provenance 一致。
func TestReplayCommittedWireOK(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	env, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if env.NonAuthoritative {
		t.Fatal("committed replay must not be non-authoritative")
	}
	if err := ValidateProvenanceMatchesRecord(env.Provenance, entry.Record); err != nil {
		t.Fatalf("provenance mismatch: %v", err)
	}
	if env.Provenance.SourceBatchID != "src-batch" || env.Provenance.SourceSequence != 7 {
		t.Fatalf("provenance: %+v", env.Provenance)
	}
	if string(env.Payload) != "hello replay" {
		t.Fatalf("payload: %q", env.Payload)
	}
	// replay 不复用 source identity：ReplayBatchFromEnvelope 生成新 intent。
	intent := env.ReplayBatchFromEnvelope("replay-intent-1")
	if intent.IntentID == "" || intent.Bytes == nil || string(intent.Bytes) != "hello replay" {
		t.Fatalf("replay intent: %+v", intent)
	}
	if intent.Source != "replay" {
		t.Fatalf("replay source: %s", intent.Source)
	}
}

// TestReplaySchemaRejected：未知 major fail closed；minor 超限拒绝。
func TestReplaySchemaRejected(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.SchemaMajor = 99
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("unknown major must be rejected")
	}
	entry = replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.SchemaMinor = 99
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("unsupported minor must be rejected")
	}
}

// TestReplayChecksumMismatch：checksum 不符 fail closed。
func TestReplayChecksumMismatch(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.PayloadChecksum = "deadbeef"
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("checksum mismatch must be rejected")
	}
	// 篡改 payload 后 checksum 不匹配。
	entry = replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.Payload[0] = 'X'
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("tampered payload must be rejected")
	}
}

// TestReplayUnknownPartialBlocks：UnknownPartial 默认阻断连续 replay。
func TestReplayUnknownPartialBlocks(t *testing.T) {
	entry := replayFixture(t, DeliveryUnknownPartial, WriteCertaintyUnknown)
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("UnknownPartial must block committed replay")
	}
}

// TestReplayAttemptedNonAuthoritative：attempted 例外——replay 允许但结果
// 标 non-authoritative。
func TestReplayAttemptedNonAuthoritative(t *testing.T) {
	entry := replayFixture(t, DeliveryUnknownPartial, WriteCertaintyUnknown)
	env, err := ReplayEnvelopeFromArchive(entry, ReplayAttemptedIntent)
	if err != nil {
		t.Fatalf("attempted replay: %v", err)
	}
	if !env.NonAuthoritative {
		t.Fatal("attempted intent replay must be non-authoritative")
	}
	// zero-proof attempted 无意义 → 拒绝。
	entry2 := replayFixture(t, DeliveryFailedZeroBytes, WriteCertaintyZero)
	if _, err := ReplayEnvelopeFromArchive(entry2, ReplayAttemptedIntent); err == nil {
		t.Fatal("zero-proof attempted replay must be rejected")
	}
}

// TestReplayNotSealedRejected：未封存 record fail closed。
func TestReplayNotSealedRejected(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.Record.SealedAt = time.Time{}
	entry.PayloadChecksum = ReplayArchiveChecksum(entry)
	if _, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire); err == nil {
		t.Fatal("unsealed record must be rejected")
	}
}

// TestReplayProvenanceMismatch：外部伪造 provenance 与 record 不符被拒。
func TestReplayProvenanceMismatch(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	env, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	bad := env.Provenance
	bad.SourceBatchID = "other-batch"
	if err := ValidateProvenanceMatchesRecord(bad, entry.Record); err == nil {
		t.Fatal("batch mismatch must fail validation")
	}
	bad = env.Provenance
	bad.SourceKind = TransactionHistoryHandoff
	if err := ValidateProvenanceMatchesRecord(bad, entry.Record); err == nil {
		t.Fatal("kind mismatch must fail validation")
	}
	bad = env.Provenance
	bad.SourceTerminal.Geometry = TerminalGeometry{Width: 1, Height: 1}
	if err := ValidateProvenanceMatchesRecord(bad, entry.Record); err == nil {
		t.Fatal("terminal mismatch must fail validation")
	}
}

// TestReplayIdentityNonReuse：replay identity 不复用——每次
// ReplayBatchFromEnvelope 生成新 intent id，source identity 只作审计。
func TestReplayIdentityNonReuse(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	env, err := ReplayEnvelopeFromArchive(entry, ReplayCommittedWire)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	i1 := env.ReplayBatchFromEnvelope("replay-1")
	i2 := env.ReplayBatchFromEnvelope("replay-2")
	if i1.IntentID == i2.IntentID {
		t.Fatal("replay intents must not reuse identity")
	}
	// source mirror 不成为 primary：envelope 不携带 primary sink 引用。
	if env.Record.Output.Primary == nil {
		t.Fatal("provenance requires record primary for audit")
	}
	// identity 链（审计用）。
	id := env.Identity()
	if id.BatchID != "src-batch" || id.Sequence != 7 {
		t.Fatalf("identity: %+v", id)
	}
}
