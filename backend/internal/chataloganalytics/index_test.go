package chataloganalytics

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrichRollupTitlesFromSessionHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	root := filepath.Join(home, ".aicli", "chat-logs")
	sessionsDir := filepath.Join(home, ".aicli", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(sessionsDir, "session_history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, created_at TEXT)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 27, 2, 0, 0, 90*int(time.Millisecond), time.UTC)
	if _, err := db.Exec("INSERT INTO sessions(id, title, created_at) VALUES(?, ?, ?)", "session_runtime", "Durable session title", createdAt.Format(time.RFC3339Nano)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sessions(id, title, created_at) VALUES(?, ?, ?)", "actor-audit", "Actor task title", createdAt.Format(time.RFC3339Nano)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rollups := []SessionRollup{{SessionID: "chat-session", StartTime: createdAt.Add(-90 * time.Millisecond)}}
	enrichRollupTitlesFromSessionHistory(root, rollups)
	if rollups[0].RuntimeSessionID != "session_runtime" || rollups[0].Title != "Durable session title" || rollups[0].TitleSource != "session_history_time_match" {
		t.Fatalf("unexpected title enrichment: %+v", rollups[0])
	}
}

func TestMatchSessionTitleByStartRejectsAmbiguousCandidates(t *testing.T) {
	start := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	records := []sessionTitleRecord{
		{SessionID: "session_nearest", Title: "Nearest", CreatedAt: start.Add(90 * time.Millisecond), TimeMatchEligible: true},
		{SessionID: "session_other", Title: "Other", CreatedAt: start.Add(450 * time.Millisecond), TimeMatchEligible: true},
	}
	matched, ok := matchSessionTitleByStart(start, records)
	if !ok || matched.SessionID != "session_nearest" {
		t.Fatalf("expected unique nearest match, got ok=%v record=%+v", ok, matched)
	}

	records[1].CreatedAt = start.Add(150 * time.Millisecond)
	if _, ok := matchSessionTitleByStart(start, records); ok {
		t.Fatal("expected close candidates to be treated as ambiguous")
	}
}

func TestDeriveSessionHistoryTitleSkipsToolTranscripts(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE session_messages (session_id TEXT, seq INTEGER, role TEXT, payload_json BLOB)"); err != nil {
		t.Fatal(err)
	}
	messages := []struct {
		seq     int
		role    string
		payload string
	}{
		{1, "system", `{"role":"system","content":"Shell guidance: use pwsh"}`},
		{2, "user", `{"role":"user","content":"\u2022 Running shell commands=[3] workdir: E:\\\\projects"}`},
		{3, "assistant", `{"role":"assistant","content":"Exit code: 1 Shell: pwsh"}`},
		{4, "user", `{"role":"user","content":"分析真实的用户请求"}`},
	}
	for _, message := range messages {
		if _, err := db.Exec("INSERT INTO session_messages(session_id, seq, role, payload_json) VALUES(?, ?, ?, ?)", "session_runtime", message.seq, message.role, message.payload); err != nil {
			t.Fatal(err)
		}
	}
	if got := deriveSessionHistoryTitle(db, "session_runtime"); got != "分析真实的用户请求" {
		t.Fatalf("unexpected repaired title: %q", got)
	}
}

func TestAnalyticsIndexInitializationAndUpsertAreIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	root := filepath.Join(home, ".aicli", "chat-logs")

	db, enabled, err := openAnalyticsIndex(root)
	if err != nil || !enabled {
		t.Fatalf("open analytics index: enabled=%v err=%v", enabled, err)
	}
	for _, table := range []string{"analytics_sessions", "analytics_turns", "analytics_llm_requests"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if count != 1 {
			db.Close()
			t.Fatalf("expected table %q to be initialized", table)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, enabled, err = openAnalyticsIndex(root)
	if err != nil || !enabled {
		t.Fatalf("reopen analytics index: enabled=%v err=%v", enabled, err)
	}
	defer db.Close()

	dir := SessionDir{Path: filepath.Join(root, "session-1"), SessionID: "session-1", RelPath: "session-1"}
	rollup := SessionRollup{SessionID: dir.SessionID, TotalTokens: 10, PartialReasons: []string{}}
	steps := []StepUsage{{TraceID: "trace-1", Step: 1, TotalTokens: 10}}
	turns := []TurnUsage{{TraceID: "trace-1", Ordinal: 1, Usage: TokenTotals{TotalTokens: 10}}}
	if err := upsertAnalyticsFacts(db, dir, "parser:test:first", rollup, steps, turns); err != nil {
		t.Fatal(err)
	}
	rollup.TotalTokens = 20
	steps[0].TotalTokens = 20
	turns[0].Usage.TotalTokens = 20
	if err := upsertAnalyticsFacts(db, dir, "parser:test:second", rollup, steps, turns); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"analytics_sessions", "analytics_turns", "analytics_llm_requests"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected one idempotent row in %s, got %d", table, count)
		}
	}
	indexedSteps, err := readIndexedSteps(db, dir.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(indexedSteps) != 1 || indexedSteps[0].TotalTokens != 20 {
		t.Fatalf("expected updated request fact, got %+v", indexedSteps)
	}

	if _, err := os.Stat(filepath.Join(home, ".aicli", "sessions", "runtime", "usage_analytics.sqlite")); err != nil {
		t.Fatalf("analytics index was not persisted: %v", err)
	}
}
