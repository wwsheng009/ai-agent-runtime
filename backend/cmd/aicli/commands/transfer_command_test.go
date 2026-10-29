package commands

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/dsh"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func writeTransferDSHFixture(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", "--test--", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir DSH fixture: %v", err)
	}
	log := strings.Join([]string{
		`{"type":"session","version":4,"id":"` + id + `","createdAt":1,"cwd":"C:\\work","isSeeded":false,"delegationDepth":0,"agentPreset":"standard"}`,
		`{"type":"user/message","seq":0,"time":2,"data":{"id":"u1","role":"user","content":[{"type":"text","text":"hello"}],"source":{"kind":"user"}},"surfaceOp":"append"}`,
		`{"type":"assistant/message","seq":1,"time":3,"data":{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"text","text":"world"}],"source":{"kind":"model","provider":"mock","model":"mock"}},"stream":[]},"surfaceOp":"append"}`,
		`{"type":"session/title","seq":2,"time":4,"data":{"title":"fixture title","messageSeqs":[0],"source":{"kind":"fallback"}}}`,
	}, "\n") + "\n"
	path := filepath.Join(dir, "session.v4.jsonl")
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatalf("write DSH fixture: %v", err)
	}
	return path
}

func runTransferCommandForTest(t *testing.T, args ...string) (int, error) {
	t.Helper()
	code, _, err := runTransferCommandWithInputForTest(t, "", args...)
	return code, err
}

func runTransferCommandWithInputForTest(t *testing.T, input string, args ...string) (int, string, error) {
	t.Helper()
	original := transferExitHook
	code := transferExitOK
	called := false
	transferExitHook = func(exitCode int) {
		code = exitCode
		called = true
	}
	t.Cleanup(func() { transferExitHook = original })
	cmd := NewTransferCommand(nil)
	var output bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if !called {
		return transferExitOK, output.String(), err
	}
	return code, output.String(), err
}

func TestTransferImportDSHFixtureDryRun(t *testing.T) {
	dshRoot := t.TempDir()
	writeTransferDSHFixture(t, dshRoot, "session-fixture")
	targetDir := t.TempDir()
	code, err := runTransferCommandForTest(t,
		"import",
		"--from", "deepseek-harness",
		"--root", dshRoot,
		"--session", "session-fixture",
		"--session-dir", targetDir,
		"--user", "tester",
		"--dry-run",
		"--json",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer import: code=%d err=%v", code, err)
	}
	targetManager, targetUser, _, err := newChatSessionManager(targetDir)
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	defer targetManager.Stop()
	sessions, err := targetManager.List(context.Background(), targetUser)
	if err != nil {
		t.Fatalf("list target sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("dry-run wrote target sessions: %d", len(sessions))
	}
}

func TestTransferExportDSHBundleRoundTrip(t *testing.T) {
	sourceDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sourceDir)
	if err != nil {
		t.Fatalf("create source manager: %v", err)
	}
	session, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		manager.Stop()
		t.Fatalf("create source session: %v", err)
	}
	messages := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
		*runtimetypes.NewAssistantMessage("world"),
	}
	for index := range messages {
		if err := manager.AddMessage(context.Background(), session.ID, messages[index]); err != nil {
			manager.Stop()
			t.Fatalf("append source message: %v", err)
		}
	}
	sessionID := session.ID
	manager.Stop()

	bundleDir := t.TempDir()
	code, err := runTransferCommandForTest(t,
		"export",
		"--to", "deepseek-harness",
		"--session", sessionID,
		"--session-dir", sourceDir,
		"--user", userID,
		"--bundle-dir", bundleDir,
		"--compression", "zstd",
		"--json",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer export: code=%d err=%v", code, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		TargetSessionID string `json:"target_session_id"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	imported, err := dsh.Read(dsh.ImportOptions{Root: bundleDir, SessionID: manifest.TargetSessionID})
	if err != nil {
		t.Fatalf("read exported bundle: %v", err)
	}
	if got := len(imported.Session.History); got != 2 {
		t.Fatalf("round-trip messages = %d, want 2", got)
	}
	if imported.Ref.AgentID != string(sessionport.AgentDeepSeekHarness) {
		t.Fatalf("agent = %q", imported.Ref.AgentID)
	}
}

func TestTransferInspectDSHFixture(t *testing.T) {
	dshRoot := t.TempDir()
	writeTransferDSHFixture(t, dshRoot, "session-inspect")

	code, err := runTransferCommandForTest(t,
		"inspect",
		"--agent", "deepseek-harness",
		"--root", dshRoot,
		"--json",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer inspect: code=%d err=%v", code, err)
	}
}

func TestTransferDetectNoInteractiveFixture(t *testing.T) {
	dshRoot := t.TempDir()
	writeTransferDSHFixture(t, dshRoot, "session-detect")

	code, err := runTransferCommandForTest(t,
		"detect",
		"--agent", "deepseek-harness",
		"--root", dshRoot,
		"--no-interactive",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer detect --no-interactive: code=%d err=%v", code, err)
	}
}

func TestTransferDetectInteractiveOptions(t *testing.T) {
	instance := sessionport.Instance{
		AgentID:         "deepseek-harness",
		InstanceID:      "deepseek-harness:default",
		Root:            `C:\Users\tester\.dsh`,
		FormatVersion:   4,
		FormatStatus:    "current",
		Backend:         "jsonl",
		Compression:     "zstd",
		SessionCount:    2,
		TranscriptCount: 2,
		Capabilities: sessionport.Capabilities{
			List: true, Read: true, WriteBundle: true, Apply: "none",
		},
	}
	instanceItems := transferDetectInstanceOptions([]sessionport.Instance{instance})
	if len(instanceItems.Items) != 1 {
		t.Fatalf("instance items = %d, want 1", len(instanceItems.Items))
	}
	if !strings.Contains(instanceItems.Items[0].Preview, `C:\Users\tester\.dsh`) {
		t.Fatalf("instance preview missing root: %q", instanceItems.Items[0].Preview)
	}

	refs := []sessionport.SessionRef{
		{SessionID: "session-ok", Title: "ok", FormatVersion: 4, FormatStatus: "current", Readable: true},
		{SessionID: "session-old", Title: "old", FormatVersion: 3, FormatStatus: "migration-required", Readable: false},
	}
	sessionOptions := transferDetectSessionOptions(instance, refs)
	if len(sessionOptions.Items) != 2 {
		t.Fatalf("session items = %d, want 2", len(sessionOptions.Items))
	}
	if !sessionOptions.Items[1].Disabled {
		t.Fatal("unreadable session must be disabled in interactive picker")
	}
}

func TestTransferRootFallsBackToHelp(t *testing.T) {
	original := transferRootUIAvailable
	transferRootUIAvailable = func() bool { return false }
	t.Cleanup(func() { transferRootUIAvailable = original })

	code, output, err := runTransferCommandWithInputForTest(t, "")
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer root: code=%d err=%v output=%s", code, err, output)
	}
	if !strings.Contains(output, "Usage:") || !strings.Contains(output, "detect") {
		t.Fatalf("expected transfer help output, got %q", output)
	}
}

func TestTransferRootMenuOptions(t *testing.T) {
	options := transferRootMenuOptions()
	if len(options.Items) != len(transferRootActions()) {
		t.Fatalf("menu items = %d, actions = %d", len(options.Items), len(transferRootActions()))
	}
	if options.ConfirmLabel != "执行" || options.Title != "会话迁移" {
		t.Fatalf("unexpected menu options: %#v", options)
	}
	joined := ""
	for _, item := range options.Items {
		joined += item.SearchText + "\n"
	}
	for _, want := range []string{"detect", "inspect", "import", "export", "help", "quit"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("menu search text missing %q: %q", want, joined)
		}
	}
}

func TestTransferExportWizardDefaults(t *testing.T) {
	cmd := newTransferExportCommand(nil)
	defaults, err := transferExportWizardDefaultsFromFlags(cmd)
	if err != nil {
		t.Fatalf("default export wizard defaults: %v", err)
	}
	if defaults.Target != "deepseek-harness" || defaults.Compression != "zstd" {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}

	cmd = newTransferExportCommand(nil)
	for name, value := range map[string]string{
		"to":          "workbuddy",
		"compression": "none",
		"session":     "latest",
		"bundle-dir":  `C:\tmp\bundle`,
		"cwd":         `C:\work`,
		"session-dir": `C:\sessions`,
		"user":        "alice",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	defaults, err = transferExportWizardDefaultsFromFlags(cmd)
	if err != nil {
		t.Fatalf("configured export wizard defaults: %v", err)
	}
	if defaults.Target != "workbuddy" || defaults.Compression != "none" || defaults.SessionID != "" {
		t.Fatalf("unexpected configured defaults: %#v", defaults)
	}
	if defaults.BundleDir != `C:\tmp\bundle` || defaults.CWD != `C:\work` ||
		defaults.SessionDir != `C:\sessions` || defaults.User != "alice" {
		t.Fatalf("unexpected configured paths: %#v", defaults)
	}

	cmd = newTransferExportCommand(nil)
	if err := cmd.Flags().Set("to", "workbuddy"); err != nil {
		t.Fatalf("set --to: %v", err)
	}
	if err := cmd.Flags().Set("compression", "zstd"); err != nil {
		t.Fatalf("set --compression: %v", err)
	}
	if _, err := transferExportWizardDefaultsFromFlags(cmd); err == nil {
		t.Fatal("expected WorkBuddy zstd to fail")
	}
}

func TestTransferExportWizardRootDefaults(t *testing.T) {
	root := NewTransferCommand(nil)
	defaults, err := transferExportWizardDefaultsFromFlags(root)
	if err != nil {
		t.Fatalf("root export wizard defaults: %v", err)
	}
	if defaults.Target != "deepseek-harness" || defaults.Compression != "zstd" {
		t.Fatalf("unexpected root defaults: %#v", defaults)
	}
}

func TestTransferExportWizardRejectsInvalidTarget(t *testing.T) {
	cmd := newTransferExportCommand(nil)
	if err := cmd.Flags().Set("to", "unknown"); err != nil {
		t.Fatalf("set --to: %v", err)
	}
	if _, err := transferExportWizardDefaultsFromFlags(cmd); err == nil {
		t.Fatal("expected invalid target to fail")
	}
}

func TestTransferExportWizardSessions(t *testing.T) {
	sourceDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sourceDir)
	if err != nil {
		t.Fatalf("create source manager: %v", err)
	}
	session, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		manager.Stop()
		t.Fatalf("create source session: %v", err)
	}
	if err := manager.AddMessage(context.Background(), session.ID, *runtimetypes.NewUserMessage("hello")); err != nil {
		manager.Stop()
		t.Fatalf("append source message: %v", err)
	}
	sessionID := session.ID
	manager.Stop()

	sessions, err := transferExportWizardSessions(context.Background(), nil, sourceDir, userID)
	if err != nil {
		t.Fatalf("list wizard sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sessionID {
		t.Fatalf("unexpected wizard sessions: %#v", sessions)
	}
}

func TestTransferExportWizardRejectsMissingSession(t *testing.T) {
	sessions := []*runtimechat.Session{
		{ID: "session-known", Metadata: runtimechat.SessionMetadata{Title: "known"}},
	}
	if _, _, err := transferExportWizardSelectSession(context.Background(), nil, sessions, "session-missing"); err == nil {
		t.Fatal("expected missing default session to fail")
	}
}

func TestTransferInspectDSHZipFixture(t *testing.T) {
	root := t.TempDir()
	fixture := writeTransferDSHFixture(t, root, "session-zip-inspect")
	zipPath := filepath.Join(root, "dsh-export.zip")
	writeTransferZipForTest(t, zipPath, fixture)

	code, err := runTransferCommandForTest(t,
		"inspect",
		"--zip", zipPath,
		"--json",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer inspect zip: code=%d err=%v", code, err)
	}
}

func TestTransferInteractiveImportCancellation(t *testing.T) {
	dshRoot := t.TempDir()
	writeTransferDSHFixture(t, dshRoot, "session-interactive")
	targetDir := t.TempDir()

	code, output, err := runTransferCommandWithInputForTest(t, "n\n",
		"import",
		"--root", dshRoot,
		"--session-dir", targetDir,
		"--user", "tester",
		"--interactive",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("interactive import: code=%d err=%v output=%s", code, err, output)
	}
	if !strings.Contains(output, "已取消") {
		t.Fatalf("expected cancellation output, got %q", output)
	}
	targetManager, targetUser, _, err := newChatSessionManager(targetDir)
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	defer targetManager.Stop()
	sessions, err := targetManager.List(context.Background(), targetUser)
	if err != nil {
		t.Fatalf("list target sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("cancelled interactive import wrote %d sessions", len(sessions))
	}
}

func TestTransferInteractiveZipImportCancellation(t *testing.T) {
	root := t.TempDir()
	fixture := writeTransferDSHFixture(t, root, "session-zip-interactive")
	zipPath := filepath.Join(root, "dsh-export.zip")
	writeTransferZipForTest(t, zipPath, fixture)
	targetDir := t.TempDir()

	code, output, err := runTransferCommandWithInputForTest(t, "n\n",
		"import",
		"--zip", zipPath,
		"--session-dir", targetDir,
		"--user", "tester",
		"--interactive",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("interactive zip import: code=%d err=%v output=%s", code, err, output)
	}
	if !strings.Contains(output, "已取消") {
		t.Fatalf("expected cancellation output, got %q", output)
	}
}

func writeTransferZipForTest(t *testing.T, zipPath, logPath string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read DSH fixture for zip: %v", err)
	}
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("session.v4.jsonl")
	if err != nil {
		_ = file.Close()
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write(data); err != nil {
		_ = file.Close()
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

func writeTransferWorkBuddyFixture(t *testing.T, root, id string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "projects", "demo"), 0o755); err != nil {
		t.Fatalf("mkdir WorkBuddy fixture: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "workbuddy.db"))
	if err != nil {
		t.Fatalf("open WorkBuddy fixture db: %v", err)
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
`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create WorkBuddy fixture schema: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (id,cwd,user_id,title,status,created_at,updated_at,last_activity_at,is_playground,model,transport) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, `C:\work`, "user-1", "WB fixture", "completed", int64(1), int64(2), int64(2), 0, "mock", "local")
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert WorkBuddy fixture session: %v", err)
	}
	_ = db.Close()
	records := []map[string]interface{}{
		{"type": "session-meta", "id": "meta", "sessionId": id, "timestamp": 1},
		{"id": "u1", "type": "message", "role": "user", "timestamp": 2, "sessionId": id, "content": []map[string]interface{}{{"type": "input_text", "text": "<user_query>hello</user_query>"}}},
		{"id": "a1", "parentId": "u1", "type": "message", "role": "assistant", "timestamp": 3, "sessionId": id, "content": []map[string]interface{}{{"type": "output_text", "text": "world"}}},
		{"type": "ai-title", "id": "title", "timestamp": 4, "aiTitle": "WB fixture"},
	}
	var builder strings.Builder
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal WorkBuddy fixture: %v", err)
		}
		builder.Write(data)
		builder.WriteByte('\n')
	}
	path := filepath.Join(root, "projects", "demo", id+".jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write WorkBuddy fixture jsonl: %v", err)
	}
	return path
}

func TestTransferImportWorkBuddyFixtureDryRun(t *testing.T) {
	root := t.TempDir()
	writeTransferWorkBuddyFixture(t, root, "wb-session")
	targetDir := t.TempDir()
	code, err := runTransferCommandForTest(t,
		"import",
		"--from", "workbuddy",
		"--root", root,
		"--session", "wb-session",
		"--session-dir", targetDir,
		"--user", "tester",
		"--dry-run",
		"--json",
	)
	if err != nil || code != transferExitOK {
		t.Fatalf("transfer import WorkBuddy: code=%d err=%v", code, err)
	}
	targetManager, targetUser, _, err := newChatSessionManager(targetDir)
	if err != nil {
		t.Fatalf("open WorkBuddy target store: %v", err)
	}
	defer targetManager.Stop()
	sessions, err := targetManager.List(context.Background(), targetUser)
	if err != nil {
		t.Fatalf("list WorkBuddy target sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("dry-run wrote WorkBuddy target sessions: %d", len(sessions))
	}
}
