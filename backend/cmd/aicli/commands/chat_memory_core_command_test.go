package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestChatMemoryCoreCommandsStructuredAndLegacy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newChatMemoryCoreCommandTestSession(t)
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	source, err := store.Append(memorystore.AppendNoteOptions{Text: "Core source for a reviewed CLI memory."})
	if err != nil {
		t.Fatalf("append source: %v", err)
	}
	candidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:     "Project memory keeps reviewed decisions stable.",
		SourceIDs:   []string{source.ID},
		GeneratedBy: "test:cli",
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, runErr := tryExecuteStructuredChatCommand(session, command)
		if runErr != nil || !handled {
			t.Fatalf("%s structured handled=%t err=%v", command, handled, runErr)
		}
		return ui.RenderDocumentPlain(result.Document())
	}

	overview := run("/memory core")
	if !strings.Contains(overview, candidate.ID) ||
		!strings.Contains(overview, "候选摘要") ||
		!strings.Contains(overview, "reviewed decisions stable") {
		t.Fatalf("core overview did not show candidate summary:\n%s", overview)
	}
	legacy := captureStdout(t, func() {
		if handleMemoryCommand(session, "/memory core") {
			t.Fatal("core query should not exit chat")
		}
	})
	if !strings.Contains(legacy, candidate.ID) || !strings.Contains(legacy, "候选摘要") {
		t.Fatalf("legacy core path did not share the candidate summary:\n%s", legacy)
	}

	listed := run("/memory core list candidate")
	if !strings.Contains(listed, candidate.ID) || !strings.Contains(listed, "count=1") {
		t.Fatalf("candidate list = %s", listed)
	}
	for _, command := range []string{
		"/memory core list unknown",
		"/memory core list candidate active",
		"/memory core activate " + candidate.ID,
		"/memory core activate " + candidate.ID + " --yes --unexpected",
		"/memory core archive " + candidate.ID + " --reason duplicate --yes --unexpected",
		"/memory core archive " + candidate.ID + " --reason missing-confirmation",
	} {
		output := run(command)
		if !strings.Contains(output, "用法:") {
			t.Fatalf("%s should fail with usage, got: %s", command, output)
		}
	}

	// Rejected arguments must not mutate the candidate.
	store, err = openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("reopen store after invalid commands: %v", err)
	}
	candidates, err := store.ListCore(context.Background(), memorystore.CoreMemoryFilter{
		Status: memorystore.CoreStatusCandidate,
	})
	if err != nil || len(candidates) != 1 || candidates[0].ID != candidate.ID {
		t.Fatalf("invalid commands changed candidate state: candidates=%+v err=%v", candidates, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store after argument checks: %v", err)
	}

	activated := run("/memory core activate " + candidate.ID + " --yes")
	if !strings.Contains(activated, "已激活 project Core") || !strings.Contains(activated, candidate.ID) {
		t.Fatalf("activation result = %s", activated)
	}
	active := run("/memory core")
	if !strings.Contains(active, "status=active") ||
		!strings.Contains(active, candidate.ID) ||
		!strings.Contains(active, "reviewed decisions stable") {
		t.Fatalf("active core overview = %s", active)
	}
	archived := run("/memory core archive " + candidate.ID + ` --reason "replace after review" --yes`)
	if !strings.Contains(archived, "已归档 project Core") || !strings.Contains(archived, candidate.ID) {
		t.Fatalf("archive result = %s", archived)
	}
	if got := run("/memory core list archived"); !strings.Contains(got, candidate.ID) {
		t.Fatalf("archived core was not retained in history: %s", got)
	}
	if got := run("/memory core"); !strings.Contains(got, "没有 active Core 或候选") {
		t.Fatalf("overview after archive = %s", got)
	}
	if got := run("/memory core rebuild"); !strings.Contains(got, "已创建待审核 project Core 候选") ||
		!strings.Contains(got, "不会自动激活") {
		t.Fatalf("core_rebuild should create a reviewable candidate: %s", got)
	}
	assertChatMemoryCoreDatabaseIsUnderTempRoot(t, root, session)
}

func TestChatMemoryCoreActivationAndReadFailClosedWhenSourcesExpire(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, root := newChatMemoryCoreCommandTestSession(t)
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	expiringSource, err := store.Append(memorystore.AppendNoteOptions{Text: "source expires before review"})
	if err != nil {
		t.Fatalf("append expiring source: %v", err)
	}
	rejectedCandidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "This candidate must not become active.",
		SourceIDs: []string{expiringSource.ID},
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	expired := memorystore.StatusExpired
	if _, err := store.UpdateRecord(context.Background(), expiringSource.ID, memorystore.RecordPatch{Status: &expired}); err != nil {
		t.Fatalf("expire candidate source: %v", err)
	}

	validSource, err := store.Append(memorystore.AppendNoteOptions{Text: "source expires after activation"})
	if err != nil {
		t.Fatalf("append active source: %v", err)
	}
	activeCandidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "This active core must not be displayed after source expiry.",
		SourceIDs: []string{validSource.ID},
	})
	if err != nil {
		t.Fatalf("create active candidate: %v", err)
	}
	if _, err := store.ActivateCore(context.Background(), activeCandidate.ID, "test:cli"); err != nil {
		t.Fatalf("activate core before source expiry: %v", err)
	}
	if _, err := store.UpdateRecord(context.Background(), validSource.ID, memorystore.RecordPatch{Status: &expired}); err != nil {
		t.Fatalf("expire active source: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, runErr := tryExecuteStructuredChatCommand(session, command)
		if runErr != nil || !handled {
			t.Fatalf("%s structured handled=%t err=%v", command, handled, runErr)
		}
		return ui.RenderDocumentPlain(result.Document())
	}
	activation := run("/memory core activate " + rejectedCandidate.ID + " --yes")
	if !strings.Contains(activation, "来源已失效") ||
		strings.Contains(activation, "This candidate must not become active") {
		t.Fatalf("activation with expired source did not fail closed: %s", activation)
	}
	for _, command := range []string{"/memory core", "/memory core list active"} {
		output := run(command)
		if !strings.Contains(output, "来源已失效") ||
			strings.Contains(output, "This active core must not be displayed") {
			t.Fatalf("%s did not fail closed after source expiry: %s", command, output)
		}
	}

	store, err = openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	if _, err := store.GetActiveCore(context.Background(), memorystore.ScopeProject, ""); err == nil {
		t.Fatal("active core with expired source should not be readable")
	}
	if _, err := store.ActivateCore(context.Background(), rejectedCandidate.ID, "test:cli"); err == nil {
		t.Fatal("candidate with expired source should not activate")
	}
	activeRows, err := store.ListCore(context.Background(), memorystore.CoreMemoryFilter{
		Status: memorystore.CoreStatusActive,
	})
	if err != nil || len(activeRows) != 1 || activeRows[0].ID != activeCandidate.ID {
		t.Fatalf("source failure changed active status unexpectedly: cores=%+v err=%v", activeRows, err)
	}
	// The active row remains in storage, but its read path is fail-closed.
	if _, err := store.GetCore(context.Background()); err == nil || !strings.Contains(err.Error(), "no longer eligible") {
		t.Fatalf("expected ineligible active source error, got %v", err)
	}
	assertChatMemoryCoreDatabaseIsUnderTempRoot(t, root, session)
}

func TestChatMemoryCoreRebuildUsesRuntimeConfigLimits(t *testing.T) {
	session, root := newChatMemoryCoreCommandTestSession(t)
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  core:
    rebuild:
      maxRecords: 12
      maxUsageRows: 34
      tokenBudget: 567
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	cfg := chatMemoryCoreRebuildConfig(session, store)
	if cfg.CoreRebuildMaxRecords != 12 ||
		cfg.CoreRebuildMaxUsageRows != 34 ||
		cfg.CoreRebuildTokenBudget != 567 {
		t.Fatalf("core rebuild config = %+v", cfg)
	}
}

func newChatMemoryCoreCommandTestSession(t *testing.T) (*ChatSession, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	return &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
	}, root
}

func assertChatMemoryCoreDatabaseIsUnderTempRoot(t *testing.T, root string, session *ChatSession) {
	t.Helper()
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store to verify temp DB location: %v", err)
	}
	defer store.Close()
	relative, err := filepath.Rel(root, store.DBPath())
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("memory database escaped test temp directory: root=%s db=%s relative=%s err=%v",
			root, store.DBPath(), relative, err)
	}
}
