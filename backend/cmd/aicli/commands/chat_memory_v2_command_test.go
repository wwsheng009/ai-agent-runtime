package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestStructuredMemoryV2Commands(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  injection:
    mode: summary
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
		RuntimeSession:    &runtimechat.Session{ID: "memory-v2-command-test"},
	}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	note, err := store.Append(memorystore.AppendNoteOptions{
		Text: "original durable memory", Kind: "decision", SessionID: "memory-v2-command-test",
	})
	if err != nil {
		t.Fatalf("append seed: %v", err)
	}
	_ = store.Close()

	for _, command := range []string{
		"/memory show " + note.ID,
		"/memory revalidate " + note.ID,
		"/memory why " + note.ID,
		"/memory why-not memory",
		"/memory correct " + note.ID + " corrected durable memory",
		"/memory stale",
		"/memory jobs",
		"/memory consolidate",
		"/memory export " + filepath.Join(root, "memory-export.jsonl"),
		"/memory forget " + note.ID + " --yes",
		"/memory reset --yes",
		"/memory mode",
		"/memory mode shadow",
	} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s structured match=(%t, %v)", command, handled, err)
		}
		if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) == "" {
			t.Fatalf("%s rendered an empty document", command)
		}
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read updated runtime config: %v", err)
	}
	if !strings.Contains(string(updated), "mode: shadow") {
		t.Fatalf("memory mode was not persisted: %s", updated)
	}
}

func TestChatMemoryStatusReportsRuntimePresence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	doc, ok := buildChatMemoryStatusDocument(store, chatMemoryRuntimeStatus{
		Ready: true, Trigger: "both", Generation: true, TurnDigest: true,
		Digest: memoryruntime.DigestStats{Written: 2, Skipped: 1, LastSkipReason: "workspace_mismatch"},
	})
	if !ok {
		t.Fatal("status document not ready")
	}
	rendered := ui.RenderDocumentPlain(doc)
	if !strings.Contains(rendered, "runtime=ready") ||
		!strings.Contains(rendered, "trigger=both") ||
		!strings.Contains(rendered, "digest=true") ||
		!strings.Contains(rendered, "digest_runtime: written=2 skipped=1 failed=0") ||
		!strings.Contains(rendered, "last_skip=workspace_mismatch") {
		t.Fatalf("status document missing runtime evidence:\n%s", rendered)
	}

	doc, ok = buildChatMemoryStatusDocument(store, chatMemoryRuntimeStatus{})
	if !ok {
		t.Fatal("status document not ready without runtime")
	}
	if rendered = ui.RenderDocumentPlain(doc); !strings.Contains(rendered, "runtime=not_initialized") {
		t.Fatalf("status document must expose missing runtime:\n%s", rendered)
	}
}

func TestStructuredMemoryPurgeTurnWindows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  injection:
    mode: summary
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
		RuntimeSession:    &runtimechat.Session{ID: "memory-purge-command-test"},
	}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	createRolledUp := func(sessionID, turnID string) {
		t.Helper()
		digest, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
			SessionID: sessionID, TurnID: turnID, Outcome: "success", MessageSeqEnd: 2,
		})
		if err != nil {
			t.Fatalf("record digest: %v", err)
		}
		if _, err := store.MarkTurnDigestsProcessed(context.Background(), sessionID, digest.DigestSeq, ""); err != nil {
			t.Fatalf("mark processed: %v", err)
		}
		window, _, err := store.EnsureTurnWindow(context.Background(), memorystore.TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: sessionID, WindowIndex: 1,
			StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			StartMessageSeq: 1, EndMessageSeq: 2, TurnCount: 1,
			SourceHash: "hash-" + sessionID, SourceRevision: digest.DigestSeq,
			Status: memorystore.TurnWindowPending, CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("ensure window: %v", err)
		}
		if _, err := store.CompleteTurnWindow(context.Background(), window.ID, memorystore.TurnWindowSummary{Text: "summary " + sessionID}); err != nil {
			t.Fatalf("complete window: %v", err)
		}
		if _, err := store.MarkTurnDigestsRolledUp(context.Background(), sessionID, digest.DigestSeq, digest.DigestSeq, window.ID); err != nil {
			t.Fatalf("mark rolled up: %v", err)
		}
	}
	createRolledUp("memory-purge-command-test", "turn-1")
	createRolledUp("memory-purge-other", "turn-2")
	_ = store.Close()

	result, handled, err := tryExecuteStructuredChatCommand(session, "/memory purge-windows --yes")
	if err != nil || !handled {
		t.Fatalf("missing scope match=(%t, %v)", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "错误:") {
		t.Fatalf("missing scope did not render an error: %s", ui.RenderDocumentPlain(result.Document()))
	}

	result, handled, err = tryExecuteStructuredChatCommand(session,
		"/memory purge-windows --yes --session memory-purge-command-test")
	if err != nil || !handled {
		t.Fatalf("session purge match=(%t, %v)", handled, err)
	}
	rendered := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(rendered, "windows=1") || !strings.Contains(rendered, "digests_released=1") {
		t.Fatalf("session purge result missing counts:\n%s", rendered)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/memory purge-windows --yes --all")
	if err != nil || !handled {
		t.Fatalf("project purge match=(%t, %v)", handled, err)
	}
	rendered = ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(rendered, "windows=1") || !strings.Contains(rendered, "scope=project") {
		t.Fatalf("project purge result missing counts:\n%s", rendered)
	}

	reopened, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	status, err := reopened.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.TurnWindows) != 0 {
		t.Fatalf("turn windows were not purged: %+v", status.TurnWindows)
	}
}

func TestStructuredMemorySessionModeAndSummaryActivate(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  injection:
    mode: summary
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
		RuntimeSession:    &runtimechat.Session{ID: "memory-session-mode-test"},
	}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	record, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "fact", Text: "summary source", Confidence: 0.9,
		SourceHash: "summary-source", SourceRevision: 1, RetentionClass: "standard",
	})
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	record, err = store.PromoteCandidate(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	first, err := store.CompileSummary(context.Background(), memorystore.Summary{Text: "first summary"}, []string{record.ID})
	if err != nil {
		t.Fatalf("compile first summary: %v", err)
	}
	if _, err := store.CompileSummary(context.Background(), memorystore.Summary{Text: "second summary"}, []string{record.ID}); err != nil {
		t.Fatalf("compile second summary: %v", err)
	}
	_ = store.Close()

	result, handled, err := tryExecuteStructuredChatCommand(session,
		"/memory session-mode memory-session-mode-test disabled")
	if err != nil || !handled {
		t.Fatalf("session-mode match=(%t, %v)", handled, err)
	}
	if rendered := ui.RenderDocumentPlain(result.Document()); !strings.Contains(rendered, "memory_mode=disabled") {
		t.Fatalf("session-mode result:\n%s", rendered)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/memory activate "+first.ID)
	if err != nil || !handled {
		t.Fatalf("activate match=(%t, %v)", handled, err)
	}
	if rendered := ui.RenderDocumentPlain(result.Document()); !strings.Contains(rendered, first.ID) || !strings.Contains(rendered, "revision=1") {
		t.Fatalf("activate result:\n%s", rendered)
	}
}
