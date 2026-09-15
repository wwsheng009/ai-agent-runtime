package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testSessionID    = "session_20260915172141_ceJgeCGw"
	testOtherSession = "session_other"
)

var testBaseTime = time.Date(2026, 9, 15, 9, 24, 0, 0, time.UTC)

// createTestDB creates an empty session_messages table shaped like the runtime
// schema (primary key included) and returns its path.
func createTestDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "session_history.sqlite")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session_messages (
		session_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		payload_json BLOB,
		artifact_path TEXT,
		created_at TEXT NOT NULL,
		PRIMARY KEY(session_id, seq)
	)`)
	require.NoError(t, err)
	return path
}

func insertRow(t *testing.T, path, sessionID string, seq int, payload []byte, artifact any, createdAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(
		`INSERT INTO session_messages(session_id, seq, payload_json, artifact_path, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, seq, payload, artifact, createdAt.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func countRows(t *testing.T, path, sessionID string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM session_messages WHERE session_id = ?`, sessionID).Scan(&count))
	return count
}

func remainingSeqs(t *testing.T, path, sessionID string) []int64 {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT seq FROM session_messages WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	require.NoError(t, err)
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var seq int64
		require.NoError(t, rows.Scan(&seq))
		seqs = append(seqs, seq)
	}
	require.NoError(t, rows.Err())
	return seqs
}

func messagePayload(t *testing.T, role, content string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]interface{}{"role": role, "content": content})
	require.NoError(t, err)
	return encoded
}

// messagePayloadWithTurn carries the write identity the default rule keys on:
// the rows of the reproduced defect all belong to one turn.
func messagePayloadWithTurn(t *testing.T, role, content, turnID string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]interface{}{
		"role":     role,
		"content":  content,
		"metadata": map[string]interface{}{"turn_id": turnID},
	})
	require.NoError(t, err)
	return encoded
}

func TestParseOptionsRequiresSession(t *testing.T) {
	_, err := parseOptions([]string{"-db", filepath.Join(t.TempDir(), "missing.sqlite")}, io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "-session")
}

func TestParseOptionsRejectsNegativeWindow(t *testing.T) {
	_, err := parseOptions([]string{"-session", testSessionID, "-window", "-1"}, io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "window")
}

// TestRunDryRunKeepsEveryRow is the safety net of the default mode: the report
// claims the duplicates and the database is byte-for-byte untouched.
func TestRunDryRunKeepsEveryRow(t *testing.T) {
	path := createTestDB(t, t.TempDir())
	payload := messagePayloadWithTurn(t, "assistant", "duplicate", "turn_dup")
	for index := 1; index <= 3; index++ {
		insertRow(t, path, testSessionID, index, payload, nil, testBaseTime.Add(time.Duration(index-1)*time.Second))
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"-db", path, "-session", testSessionID, "-window", "3600"}, &stdout, &stderr)
	require.NoError(t, err)

	out := stdout.String()
	require.Contains(t, out, "session-dedupe dry-run")
	require.Contains(t, out, "scanned rows: 3")
	require.Contains(t, out, "duplicate groups: 1")
	require.Contains(t, out, "surplus rows (would be deleted): 2")
	require.Contains(t, out, "group 1: keep seq=1")
	require.Contains(t, out, "drop seq=[2 3]")
	require.Equal(t, 3, countRows(t, path, testSessionID))
}

// TestRunApplyDeletesSurplusRowsOfOneSessionOnly proves the delete path touches
// exactly the surplus rows of the requested session.
func TestRunApplyDeletesSurplusRowsOfOneSessionOnly(t *testing.T) {
	path := createTestDB(t, t.TempDir())
	payload := messagePayloadWithTurn(t, "assistant", "duplicate", "turn_dup")
	for index := 1; index <= 3; index++ {
		insertRow(t, path, testSessionID, index, payload, nil, testBaseTime.Add(time.Duration(index-1)*time.Second))
	}
	insertRow(t, path, testSessionID, 4, messagePayload(t, "assistant", "keep me"), nil, testBaseTime.Add(time.Minute))
	otherPayload := messagePayload(t, "user", "other session")
	insertRow(t, path, testOtherSession, 1, otherPayload, nil, testBaseTime)
	insertRow(t, path, testOtherSession, 2, otherPayload, nil, testBaseTime.Add(time.Second))

	var stdout, stderr bytes.Buffer
	err := run([]string{"-db", path, "-session", testSessionID, "-window", "3600", "-apply"}, &stdout, &stderr)
	require.NoError(t, err)

	out := stdout.String()
	require.Contains(t, out, "session-dedupe apply")
	require.Contains(t, out, "deleted rows: 2")
	require.Equal(t, []int64{1, 4}, remainingSeqs(t, path, testSessionID))
	require.Equal(t, 2, countRows(t, path, testOtherSession))
}

// TestRunJSONSummary checks the machine readable contract.
func TestRunJSONSummary(t *testing.T) {
	path := createTestDB(t, t.TempDir())
	payload := messagePayloadWithTurn(t, "assistant", "duplicate", "turn_dup")
	for index := 1; index <= 3; index++ {
		insertRow(t, path, testSessionID, index, payload, nil, testBaseTime.Add(time.Duration(index-1)*time.Second))
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"-db", path, "-session", testSessionID, "-json"}, &stdout, &stderr)
	require.NoError(t, err)

	var summary report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &summary))
	require.Equal(t, 3, summary.ScannedRows)
	require.Equal(t, 1, summary.DuplicateGroups)
	require.Equal(t, 2, summary.SurplusRows)
	require.Equal(t, 0, summary.DeletedRows)
	require.False(t, summary.Apply)
	require.False(t, summary.AnyTurn, "the default rule never folds across turns")
	require.Len(t, summary.Groups, 1)
	require.Equal(t, int64(1), summary.Groups[0].KeepSeq)
	require.Equal(t, []int64{2, 3}, summary.Groups[0].DropSeqs)
	require.Equal(t, 3, countRows(t, path, testSessionID))
}

// TestRunReportsOnlyGroupsWithSurplusRows pins the report projection: the
// planner keeps one entry per retained row so a run of duplicates can chain
// onto its anchor, and only anchors that actually carry surplus rows may be
// reported as duplicate groups. Projecting the anchors verbatim reported every
// row of a healthy transcript as a duplicate (2651 scanned rows claimed 2641
// groups next to the 10 real surplus rows).
func TestRunReportsOnlyGroupsWithSurplusRows(t *testing.T) {
	path := createTestDB(t, t.TempDir())
	for index := 1; index <= 4; index++ {
		insertRow(t, path, testSessionID, index,
			messagePayload(t, "assistant", fmt.Sprintf("unique %d", index)),
			nil, testBaseTime.Add(time.Duration(index)*time.Second))
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"-db", path, "-session", testSessionID, "-json"}, &stdout, &stderr))

	var summary report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &summary))
	require.Equal(t, 4, summary.ScannedRows)
	require.Zero(t, summary.SurplusRows)
	require.Zero(t, summary.DuplicateGroups, "a transcript without duplicates has no duplicate group")
	require.Empty(t, summary.Groups)

	stdout.Reset()
	require.NoError(t, run([]string{"-db", path, "-session", testSessionID}, &stdout, &stderr))
	out := stdout.String()
	require.Contains(t, out, "duplicate groups: 0")
	require.NotContains(t, out, "group 1:")
	require.Equal(t, 4, countRows(t, path, testSessionID))
}

// TestRunReadsSpilledArtifactPayload covers the runtime layout where a large
// body lives in an artifact file next to the database.
func TestRunReadsSpilledArtifactPayload(t *testing.T) {
	dir := t.TempDir()
	path := createTestDB(t, dir)
	payload := messagePayloadWithTurn(t, "assistant", "spilled body", "turn_spill")
	artifactRel := "message_artifacts/session_test/body.json"
	artifactPath := filepath.Join(dir, filepath.FromSlash(artifactRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(artifactPath), 0o755))
	require.NoError(t, os.WriteFile(artifactPath, payload, 0o644))
	insertRow(t, path, testSessionID, 1, nil, artifactRel, testBaseTime)
	insertRow(t, path, testSessionID, 2, payload, nil, testBaseTime.Add(time.Second))

	var stdout, stderr bytes.Buffer
	err := run([]string{"-db", path, "-session", testSessionID}, &stdout, &stderr)
	require.NoError(t, err)

	out := stdout.String()
	require.Contains(t, out, "missing payload rows (kept: artifact unavailable): 0")
	require.Contains(t, out, "surplus rows (would be deleted): 1")
	require.Equal(t, 2, countRows(t, path, testSessionID))
}

// TestRunKeepsRowWithUnreadableArtifact: a body that cannot be materialized is
// never folded, and it breaks the chain instead of exposing a neighbour.
func TestRunKeepsRowWithUnreadableArtifact(t *testing.T) {
	dir := t.TempDir()
	path := createTestDB(t, dir)
	payload := messagePayload(t, "assistant", "same")
	insertRow(t, path, testSessionID, 1, payload, nil, testBaseTime)
	insertRow(t, path, testSessionID, 2, nil, "message_artifacts/missing.json", testBaseTime.Add(time.Second))
	insertRow(t, path, testSessionID, 3, payload, nil, testBaseTime.Add(2*time.Second))

	var stdout, stderr bytes.Buffer
	err := run([]string{"-db", path, "-session", testSessionID}, &stdout, &stderr)
	require.NoError(t, err)

	out := stdout.String()
	require.Contains(t, out, "missing payload rows (kept: artifact unavailable): 1")
	require.Contains(t, out, "surplus rows (would be deleted): 0")
	require.Equal(t, 3, countRows(t, path, testSessionID))
}

// TestRunKeepsRepeatedPromptFromDifferentTurns is the safety property of the
// default rule: a prompt the user really sent twice (two turns, same text) is
// never deleted, because folding it would erase a real user action. -any-turn
// re-enables the loose rule and is therefore opt-in.
func TestRunKeepsRepeatedPromptFromDifferentTurns(t *testing.T) {
	path := createTestDB(t, t.TempDir())
	insertRow(t, path, testSessionID, 1, messagePayloadWithTurn(t, "user", "继续", "turn_1"), nil, testBaseTime)
	insertRow(t, path, testSessionID, 2, messagePayloadWithTurn(t, "user", "继续", "turn_2"), nil,
		testBaseTime.Add(6*time.Minute))

	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"-db", path, "-session", testSessionID}, &stdout, &stderr))
	out := stdout.String()
	require.Contains(t, out, "fold across turns: false")
	require.Contains(t, out, "surplus rows (would be deleted): 0")
	require.Equal(t, 2, countRows(t, path, testSessionID))

	stdout.Reset()
	require.NoError(t, run([]string{"-db", path, "-session", testSessionID, "-any-turn", "-apply"}, &stdout, &stderr))
	out = stdout.String()
	require.Contains(t, out, "fold across turns: true")
	require.Contains(t, out, "deleted rows: 1")
	require.Equal(t, []int64{1}, remainingSeqs(t, path, testSessionID))
}

// TestDefaultDatabasePathMatchesRuntimeDefault keeps the flag default in sync
// with the documented <home>/.aicli/sessions/session_history.sqlite location.
func TestDefaultDatabasePathMatchesRuntimeDefault(t *testing.T) {
	resolved, err := defaultDatabasePath()
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".aicli", "sessions", "session_history.sqlite"), resolved)
}
