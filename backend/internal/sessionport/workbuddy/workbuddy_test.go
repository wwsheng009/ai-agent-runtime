package workbuddy

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func appendFixtureRecord(t *testing.T, builder *strings.Builder, value map[string]interface{}) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture record: %v", err)
	}
	builder.Write(encoded)
	builder.WriteByte('\n')
}

func writeWorkBuddyFixture(t *testing.T, root, sessionID string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "projects", "demo"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "workbuddy.db"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE sessions (
  id TEXT PRIMARY KEY, cwd TEXT NOT NULL, user_id TEXT NOT NULL, title TEXT,
  custom_title TEXT, status TEXT NOT NULL, created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL, last_activity_at INTEGER, deleted_at INTEGER,
  is_playground INTEGER NOT NULL DEFAULT 0, model TEXT, transport TEXT NOT NULL DEFAULT 'local',
  permission_mode TEXT, conversation_origin TEXT
);
CREATE TABLE __workbuddy_drizzle_migrations (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO __workbuddy_drizzle_migrations (id, name) VALUES (1, 'fixture_migration');
`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (id,cwd,user_id,title,status,created_at,updated_at,last_activity_at,is_playground,model,transport) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		sessionID, `C:\work`, "user-1", "fixture title", "completed", int64(1000), int64(2000), int64(2000), 0, "mock", "local")
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert fixture session: %v", err)
	}
	_ = db.Close()

	var builder strings.Builder
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "session-meta", "id": "meta-1", "sessionId": sessionID, "timestamp": 1000,
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "message", "id": "u1", "timestamp": 1100, "role": "user", "sessionId": sessionID,
		"content": []map[string]interface{}{{
			"type": "input_text",
			"text": "<system-reminder data-role=\"user-context\">\n<user_info>OS: test</user_info>\n</system-reminder>\n<user_query>run pwd</user_query>",
		}},
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "reasoning", "id": "r1", "parentId": "u1", "timestamp": 1150,
		"rawContent": []map[string]interface{}{{"type": "reasoning_text", "text": "think first"}},
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "message", "id": "a1", "parentId": "u1", "timestamp": 1200, "role": "assistant",
		"content":      []map[string]interface{}{{"type": "output_text", "text": "working"}},
		"providerData": map[string]interface{}{"model": "mock"},
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "function_call", "id": "fc1", "parentId": "a1", "timestamp": 1300,
		"callId": "call-1", "name": "Bash", "arguments": `{"command":"pwd"}`,
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "function_call_result", "id": "fr1", "parentId": "fc1", "timestamp": 1400,
		"callId": "call-1", "name": "Bash", "status": "completed",
		"output": map[string]interface{}{"type": "text", "text": "/work"},
	})
	appendFixtureRecord(t, &builder, map[string]interface{}{
		"type": "ai-title", "id": "title-1", "timestamp": 1500, "aiTitle": "fixture title",
	})
	path := filepath.Join(root, "projects", "demo", sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write fixture jsonl: %v", err)
	}
	return path
}

func TestDetectListAndRead(t *testing.T) {
	root := t.TempDir()
	writeWorkBuddyFixture(t, root, "session-1")
	instances, err := Detect([]string{root}, "workbuddy")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(instances) != 1 || instances[0].SessionCount != 1 || !instances[0].Capabilities.Read {
		t.Fatalf("unexpected instances: %#v", instances)
	}
	refs, err := List(instances[0])
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 1 || refs[0].Title != "fixture title" || !refs[0].Readable {
		t.Fatalf("unexpected refs: %#v", refs)
	}
	imported, err := Read(ImportOptions{Root: root, SessionID: "session-1", View: sessionport.ViewSurface})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(imported.Session.History) != 3 {
		t.Fatalf("history len = %d, want 3", len(imported.Session.History))
	}
	if imported.Session.History[0].Content != "run pwd" {
		t.Fatalf("user text = %q", imported.Session.History[0].Content)
	}
	if len(imported.Session.History[1].ToolCalls) != 1 || imported.Session.History[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool calls = %#v", imported.Session.History[1].ToolCalls)
	}
	if imported.Session.History[2].ToolCallID != "call-1" {
		t.Fatalf("tool result = %#v", imported.Session.History[2])
	}
}

func TestResendForkSurfaceAndTranscript(t *testing.T) {
	root := t.TempDir()
	sessionID := "fork-session"
	if err := os.MkdirAll(filepath.Join(root, "projects", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "workbuddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, cwd TEXT, user_id TEXT, title TEXT, custom_title TEXT, status TEXT, created_at INTEGER, updated_at INTEGER, last_activity_at INTEGER, deleted_at INTEGER, is_playground INTEGER, model TEXT, transport TEXT, permission_mode TEXT, conversation_origin TEXT); CREATE TABLE __workbuddy_drizzle_migrations (id INTEGER PRIMARY KEY, name TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions (id,cwd,user_id,title,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, sessionID, `C:\work`, "u", "fork", "completed", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var builder strings.Builder
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "u-first", "timestamp": 1, "role": "user", "content": []map[string]interface{}{{"type": "input_text", "text": "first"}}})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "a-first", "parentId": "u-first", "timestamp": 2, "role": "assistant", "content": []map[string]interface{}{{"type": "output_text", "text": "first answer"}}})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "u-old", "parentId": "a-first", "timestamp": 3, "role": "user", "content": []map[string]interface{}{{"type": "input_text", "text": "old"}}})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "a-old", "parentId": "u-old", "timestamp": 4, "role": "assistant", "content": []map[string]interface{}{{"type": "output_text", "text": "old answer"}}})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "resend-fork-notice", "id": "fork", "parentId": "a-first", "editedUserItemId": "u-old", "timestamp": 5})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "u-new", "parentId": "a-first", "timestamp": 6, "role": "user", "content": []map[string]interface{}{{"type": "input_text", "text": "new"}}})
	appendFixtureRecord(t, &builder, map[string]interface{}{"type": "message", "id": "a-new", "parentId": "u-new", "timestamp": 7, "role": "assistant", "content": []map[string]interface{}{{"type": "output_text", "text": "new answer"}}})
	path := filepath.Join(root, "projects", "demo", sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	surface, err := Read(ImportOptions{Root: root, SessionID: sessionID, View: sessionport.ViewSurface})
	if err != nil {
		t.Fatal(err)
	}
	if len(surface.Session.History) != 4 || surface.Session.History[2].Content != "new" || surface.Session.History[3].Content != "new answer" {
		t.Fatalf("surface history = %#v", surface.Session.History)
	}
	transcript, err := Read(ImportOptions{Root: root, SessionID: sessionID, View: sessionport.ViewTranscript})
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.Session.History) != 6 {
		t.Fatalf("transcript len = %d, want 6", len(transcript.Session.History))
	}
}

func TestExportBundleRoundTripAndSlug(t *testing.T) {
	history := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "working",
			ToolCalls: []runtimetypes.ToolCall{{
				ID: "call-1", Name: "Bash", Args: map[string]interface{}{"command": "pwd"},
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		*runtimetypes.NewToolMessage("call-1", "/work"),
		*runtimetypes.NewAssistantMessage("done"),
	}
	result, err := ExportBundle(ExportOptions{
		BundleDir: filepath.Join(t.TempDir(), "bundle"),
		CWD:       `C:\work`,
		Title:     "roundtrip",
		Model:     "mock",
		History:   history,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(result.SessionFile, "c--work") {
		t.Fatalf("project slug should not duplicate drive separator: %s", result.SessionFile)
	}
	imported, err := Read(ImportOptions{FilePath: result.SessionFile, View: sessionport.ViewSurface})
	if err != nil {
		t.Fatalf("read exported bundle: %v", err)
	}
	if len(imported.Session.History) != 4 {
		t.Fatalf("round-trip messages = %d, want 4", len(imported.Session.History))
	}
	if len(imported.Session.History[1].ToolCalls) != 1 || imported.Session.History[2].ToolCallID != "call-1" {
		t.Fatalf("round-trip tool chain lost: %#v", imported.Session.History)
	}
	if imported.Session.Metadata.Title != "roundtrip" {
		t.Fatalf("title = %q", imported.Session.Metadata.Title)
	}
}

func TestExportRejectsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	options := ExportOptions{
		SessionID: "fixed-id",
		BundleDir: dir,
		CWD:       `C:\work`,
		History:   []runtimetypes.Message{*runtimetypes.NewUserMessage("hello")},
	}
	if _, err := ExportBundle(options); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if _, err := ExportBundle(options); err == nil {
		t.Fatal("expected second export to reject overwrite")
	}
}

func TestProjectSlugMatchesWorkBuddyShape(t *testing.T) {
	if got := projectSlug(`C:\Users\vince\WorkBuddy`); got != "c-Users-vince-WorkBuddy" {
		t.Fatalf("projectSlug = %q", got)
	}
	if got := projectSlug(`E:\projects\ai`); got != "e-projects-ai" {
		t.Fatalf("projectSlug = %q", got)
	}
}
