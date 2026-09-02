package output

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ============================================================================
// B2 replay archive 序列化与校验测试
// ============================================================================

func TestReplayArchiveRoundTrip(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entries := []ReplayArchiveEntry{entry}

	dir := t.TempDir()
	path := filepath.Join(dir, "test-archive.json")

	if err := WriteReplayArchive(path, entries); err != nil {
		t.Fatalf("WriteReplayArchive: %v", err)
	}

	readBack, err := ReadReplayArchive(path)
	if err != nil {
		t.Fatalf("ReadReplayArchive: %v", err)
	}
	if len(readBack.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(readBack.Entries))
	}
	if readBack.FileSchemaVersion != ReplayArchiveFileSchemaVersion {
		t.Fatalf("schema version: %d", readBack.FileSchemaVersion)
	}
	got := readBack.Entries[0]
	if got.Record.RecordID != entry.Record.RecordID {
		t.Fatalf("record id: %q != %q", got.Record.RecordID, entry.Record.RecordID)
	}
	if string(got.Payload) != string(entry.Payload) {
		t.Fatalf("payload: %q != %q", string(got.Payload), string(entry.Payload))
	}
	if got.PayloadChecksum != entry.PayloadChecksum {
		t.Fatalf("checksum: %q != %q", got.PayloadChecksum, entry.PayloadChecksum)
	}
}

func TestReplayArchiveDecodeRejectsUnsupportedSchema(t *testing.T) {
	data := `{"file_schema_version":99,"entries":[]}`
	_, err := DecodeReplayArchive([]byte(data))
	if err == nil {
		t.Fatal("expected error for unsupported file schema")
	}
}

func TestReplayArchiveDecodeRejectsEmptyEntries(t *testing.T) {
	data := `{"file_schema_version":1,"entries":[]}`
	_, err := DecodeReplayArchive([]byte(data))
	if err == nil {
		t.Fatal("expected error for empty entries")
	}
}

func TestReplayArchiveVerifyCommittedWire(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	file := &ReplayArchiveFile{
		FileSchemaVersion: ReplayArchiveFileSchemaVersion,
		Entries:           []ReplayArchiveEntry{entry},
	}
	valid, err := VerifyReplayArchive(file, ReplayCommittedWire)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if valid != 1 {
		t.Fatalf("expected 1 valid, got %d", valid)
	}
}

func TestReplayArchiveVerifyRejectsTampered(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.PayloadChecksum = "deadbeef" // tampered checksum
	file := &ReplayArchiveFile{
		FileSchemaVersion: ReplayArchiveFileSchemaVersion,
		Entries:           []ReplayArchiveEntry{entry},
	}
	_, err := VerifyReplayArchive(file, ReplayCommittedWire)
	if err == nil {
		t.Fatal("expected verify error for tampered checksum")
	}
}

func TestReplayArchiveVerifyRejectsUnknownPartial(t *testing.T) {
	entry := replayFixture(t, DeliveryUnknownPartial, WriteCertaintyUnknown)
	file := &ReplayArchiveFile{
		FileSchemaVersion: ReplayArchiveFileSchemaVersion,
		Entries:           []ReplayArchiveEntry{entry},
	}
	_, err := VerifyReplayArchive(file, ReplayCommittedWire)
	if err == nil {
		t.Fatal("expected verify error for UnknownPartial in committed_wire mode")
	}
}

func TestReplayArchiveWriteReadMultipleEntries(t *testing.T) {
	e1 := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	e2 := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	e2.Record.RecordID = "rd-2"
	entries := []ReplayArchiveEntry{e1, e2}

	dir := t.TempDir()
	path := filepath.Join(dir, "multi-archive.json")

	if err := WriteReplayArchive(path, entries); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := ReadReplayArchive(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(file.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(file.Entries))
	}
}

func TestReplayArchiveWriteRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-archive.json")
	err := WriteReplayArchive(path, nil)
	if err == nil {
		t.Fatal("expected error for empty entries")
	}
}

func TestReplayArchiveReadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadReplayArchive(path)
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
}

func TestReplayArchiveVerifyNilFile(t *testing.T) {
	_, err := VerifyReplayArchive(nil, ReplayCommittedWire)
	if err == nil {
		t.Fatal("expected error for nil file")
	}
}

// TestReplayArchiveSealedAtRequired：未封存 record 的 entry 被 verify 拒绝。
func TestReplayArchiveSealedAtRequired(t *testing.T) {
	entry := replayFixture(t, DeliveryCommitted, WriteCertaintyFull)
	entry.Record.SealedAt = time.Time{}
	entry.PayloadChecksum = ReplayArchiveChecksum(entry) // re-checksum with zero sealedAt
	file := &ReplayArchiveFile{
		FileSchemaVersion: ReplayArchiveFileSchemaVersion,
		Entries:           []ReplayArchiveEntry{entry},
	}
	_, err := VerifyReplayArchive(file, ReplayCommittedWire)
	if err == nil {
		t.Fatal("expected error for unsealed record")
	}
}