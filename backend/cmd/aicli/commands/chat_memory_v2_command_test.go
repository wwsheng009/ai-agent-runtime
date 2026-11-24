package commands

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

func TestChatMemoryWhyNotUsesConfiguredInjectionBudgetsAndRedactsQuery(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  injection:
    mode: notes
    maxItems: 4
    tokenBudget: 300
    extendedPreset: true
    budgets:
      core: 0.2
      task: 0.2
      retrieved: 0.2
      procedure: 0.2
      reference: 0.2
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
		RuntimeSession:    &runtimechat.Session{ID: "memory-why-not-budget-test"},
	}

	limit, tokenBudget := chatMemoryInjectionDiagnosticsBudget(session)
	if limit != 4 || tokenBudget != 5000 {
		t.Fatalf("diagnostic budget=(%d,%d), want (4,5000)", limit, tokenBudget)
	}

	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	source, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "why-not-budget-source", Kind: "decision",
		Text: "reviewed source for project core", Confidence: 0.9,
		SourceHash: "why-not-budget-source", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("insert core source: %v", err)
	}
	active := memorystore.StatusActive
	if _, err := store.UpdateRecord(context.Background(), source.ID, memorystore.RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate core source: %v", err)
	}
	coreCandidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "Keep the reviewed core inside its category allocation.",
		SourceIDs: []string{source.ID},
	})
	if err != nil {
		t.Fatalf("create core candidate: %v", err)
	}
	if _, err := store.ActivateCore(context.Background(), coreCandidate.ID, "admin:test"); err != nil {
		t.Fatalf("activate core: %v", err)
	}
	for index, kind := range []string{"task", "procedure", "reference"} {
		candidate, insertErr := store.InsertCandidate(context.Background(), memorystore.Candidate{
			SessionID: "why-not-budget-fixture", Kind: kind,
			Text: "category fixture for " + kind, Confidence: 0.9,
			SourceHash: kind, SourceRevision: int64(index + 1),
		})
		if insertErr != nil {
			t.Fatalf("insert %s fixture: %v", kind, insertErr)
		}
		if _, updateErr := store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Status: &active}); updateErr != nil {
			t.Fatalf("activate %s fixture: %v", kind, updateErr)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/memory why-not alpha sk-abcdefghijklmnop")
	if err != nil || !handled {
		t.Fatalf("why-not handled=%t err=%v", handled, err)
	}
	rendered := ui.RenderDocumentPlain(result.Document())
	if strings.Contains(rendered, "sk-abcdefghijklmnop") {
		t.Fatalf("why-not query preview leaked a secret: %s", rendered)
	}
	if !strings.Contains(rendered, "category_budget_used:") ||
		!strings.Contains(rendered, "core=") ||
		!strings.Contains(rendered, "task=") ||
		!strings.Contains(rendered, "procedure=") ||
		!strings.Contains(rendered, "reference=") {
		t.Fatalf("why-not omitted category budget accounting:\n%s", rendered)
	}
}

func TestStructuredMemoryHistoryRollbackAndSafeProjection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	record, err := store.Append(memorystore.AppendNoteOptions{
		ID: "mem_cli_history", Text: "original CLI history content", Title: "Original title",
	})
	if err != nil {
		t.Fatalf("append history record: %v", err)
	}
	updatedText := "updated CLI history content"
	updated, err := store.UpdateRecord(context.Background(), record.ID, memorystore.RecordPatch{Text: &updatedText})
	if err != nil {
		t.Fatalf("update history record: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, runErr := tryExecuteStructuredChatCommand(session, command)
		if runErr != nil || !handled {
			t.Fatalf("%s handled=%t err=%v", command, handled, runErr)
		}
		return ui.RenderDocumentPlain(result.Document())
	}

	history := run("/memory history " + record.ID)
	if !strings.Contains(history, "v=1") ||
		!strings.Contains(history, "original CLI history content") ||
		!strings.Contains(history, "updated CLI history content") ||
		strings.Contains(history, "source_quote") || strings.Contains(history, "metadata") {
		t.Fatalf("history projection is incomplete or leaks internal fields:\n%s", history)
	}

	usage := run("/memory rollback " + record.ID + " 1 --expected-revision " +
		strconv.FormatInt(updated.Revision, 10) + ` --reason "restore original CLI history" --yes`)
	if !strings.Contains(usage, "revision=3") || !strings.Contains(usage, "original CLI history content") {
		t.Fatalf("rollback result = %s", usage)
	}

	missingRevision := run("/memory rollback " + record.ID + " 1 --reason test --yes")
	if !strings.Contains(missingRevision, "--expected-revision") {
		t.Fatalf("rollback without expected revision should show strict usage: %s", missingRevision)
	}
	missingConfirmation := run("/memory rollback " + record.ID + " 1 --expected-revision 3 --reason test")
	if !strings.Contains(missingConfirmation, "--yes") {
		t.Fatalf("rollback without --yes should show strict usage: %s", missingConfirmation)
	}
	stale := run("/memory rollback " + record.ID + " 1 --expected-revision 2 --reason stale --yes")
	if !strings.Contains(stale, "冲突") || strings.Contains(stale, "changed concurrently") {
		t.Fatalf("stale rollback should be safely classified: %s", stale)
	}

	secret := "sk-123456789012345678901234"
	safeDoc := ui.RenderDocumentPlain(buildChatMemoryHistoryDocument(record.ID, []memorystore.RecordVersion{{
		Version: 1, ChangeType: "update", ChangedBy: "operator",
		ChangeReason: "test reason", Record: memorystore.Record{
			ID: record.ID, Revision: 1, Status: memorystore.StatusActive,
			Text: "token " + secret, SourceQuote: "private source quote",
		},
	}}))
	if strings.Contains(safeDoc, secret) || strings.Contains(safeDoc, "private source quote") {
		t.Fatalf("history document leaked sensitive fields: %s", safeDoc)
	}
}

func TestStructuredMemoryListAndSearchIncludeAutomaticRecordsWithSourceFilter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	manual, err := store.Append(memorystore.AppendNoteOptions{
		Text: "shared manual preference for stable memory",
	})
	if err != nil {
		t.Fatalf("append manual record: %v", err)
	}
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "cli-auto-session", Kind: "fact",
		Text:       "shared extracted preference for stable memory",
		Confidence: 0.9, SourceHash: "cli-auto-source", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("insert automatic candidate: %v", err)
	}
	active := memorystore.StatusActive
	automatic, err := store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Status: &active})
	if err != nil {
		t.Fatalf("activate automatic candidate: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s handled=%t err=%v", command, handled, err)
		}
		return ui.RenderDocumentPlain(result.Document())
	}
	listed := run("/memory list 20")
	if !strings.Contains(listed, manual.ID) || !strings.Contains(listed, automatic.ID) ||
		!strings.Contains(listed, "source=session_extract") {
		t.Fatalf("unfiltered list should show manual and automatic active records:\n%s", listed)
	}
	autoList := run("/memory list 20 --source-kind session_extract")
	if !strings.Contains(autoList, automatic.ID) || strings.Contains(autoList, manual.ID) {
		t.Fatalf("source-filtered list returned wrong records:\n%s", autoList)
	}
	manualList := run("/memory list --source-kind=manual")
	if !strings.Contains(manualList, manual.ID) || strings.Contains(manualList, automatic.ID) {
		t.Fatalf("manual source filter returned wrong records:\n%s", manualList)
	}

	searched := run("/memory search shared stable memory")
	if !strings.Contains(searched, manual.ID) || !strings.Contains(searched, automatic.ID) {
		t.Fatalf("unfiltered search should include both sources:\n%s", searched)
	}
	autoSearch := run("/memory search shared stable memory --source-kind session_extract")
	if !strings.Contains(autoSearch, automatic.ID) || strings.Contains(autoSearch, manual.ID) {
		t.Fatalf("source-filtered search returned wrong records:\n%s", autoSearch)
	}
}

func TestStructuredMemoryListAndSearchUseMultiValueKindTagFilters(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	manual, err := store.Append(memorystore.AppendNoteOptions{
		Text: "shared multi value memory manual decision", Kind: "decision",
		Tags: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("append manual record: %v", err)
	}

	activateCandidate := func(key, text, kind string, tags []string) string {
		t.Helper()
		candidate, insertErr := store.InsertCandidate(context.Background(), memorystore.Candidate{
			SessionID: "cli-multi-value", Kind: kind, Text: text, Tags: tags,
			Confidence: 0.9, SourceHash: key, SourceRevision: 1,
		})
		if insertErr != nil {
			t.Fatalf("insert candidate %s: %v", key, insertErr)
		}
		active := memorystore.StatusActive
		record, updateErr := store.UpdateRecord(context.Background(), candidate.ID,
			memorystore.RecordPatch{Status: &active})
		if updateErr != nil {
			t.Fatalf("activate candidate %s: %v", key, updateErr)
		}
		return record.ID
	}
	factBoth := activateCandidate("cli-multi-fact", "shared multi value memory automatic fact", "fact", []string{"alpha", "beta"})
	decisionAlpha := activateCandidate("cli-multi-decision", "shared multi value memory automatic decision", "decision", []string{"alpha"})
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s handled=%t err=%v", command, handled, err)
		}
		return ui.RenderDocumentPlain(result.Document())
	}

	bothTags := run("/memory list 20 --kind decision --kind fact --tag alpha --tag beta")
	if !strings.Contains(bothTags, manual.ID) || !strings.Contains(bothTags, factBoth) ||
		strings.Contains(bothTags, decisionAlpha) {
		t.Fatalf("multi-value list with all tags returned wrong records:\n%s", bothTags)
	}
	commaSeparated := run("/memory list 20 --kind=decision,fact --tag=alpha")
	for _, id := range []string{manual.ID, factBoth, decisionAlpha} {
		if !strings.Contains(commaSeparated, id) {
			t.Fatalf("comma-separated multi-value list missing %s:\n%s", id, commaSeparated)
		}
	}
	factSearch := run("/memory search shared multi value memory --kind fact --tag beta")
	if !strings.Contains(factSearch, factBoth) ||
		strings.Contains(factSearch, manual.ID) || strings.Contains(factSearch, decisionAlpha) {
		t.Fatalf("multi-value search returned wrong records:\n%s", factSearch)
	}
}

func TestMemorySourceKindLabelsAreSafeForCLIOutput(t *testing.T) {
	if got := safeMemorySourceKindLabel("session_extract\x1b[31m secret=sk-123456789012345678901234\nnext"); strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("unsafe source kind label = %q", got)
	} else if strings.Contains(got, "sk-123456789012345678901234") {
		t.Fatalf("source kind label leaked secret = %q", got)
	}
}

func TestChatMemoryStatusReportsRuntimePresence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	for _, job := range []memorystore.MemoryJob{
		{
			Kind: "phase1_extract", JobKey: "cli-pending-legacy", SessionID: "cli-session",
			Status: memorystore.JobStatusPending, Attempt: 1, Priority: 10,
			CreatedAt: now.Add(-3 * time.Minute), AvailableAt: now.Add(-time.Minute),
		},
		{
			Kind: "phase1_extract/v2", JobKey: "cli-pending-v2", SessionID: "cli-session",
			SessionGeneration: 1, CaptureReceiptID: "cli-receipt", SourceStartSeq: 1,
			SourceEndSeq: 2, SourceRevision: 1, SourceHash: "cli-source-hash",
			Status: memorystore.JobStatusPending, Attempt: 0, Priority: 20,
			CreatedAt: now.Add(-2 * time.Minute), AvailableAt: now.Add(-time.Minute),
			DeadlineAt: now.Add(time.Minute),
		},
		{
			Kind: "phase1_extract", JobKey: "cli-dead-letter", SessionID: "cli-dead-session",
			Status: memorystore.JobStatusFailed, Attempt: 3, Priority: 0,
			CreatedAt: now.Add(-4 * time.Minute), DeadLetterAt: now.Add(-time.Minute),
			DeadLetterReason: "canonical source unavailable",
		},
	} {
		if _, err := store.EnsureJob(context.Background(), job); err != nil {
			t.Fatalf("ensure status job %s: %v", job.JobKey, err)
		}
	}

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
		!strings.Contains(rendered, "last_skip=workspace_mismatch") ||
		!strings.Contains(rendered, "search_backend=") ||
		!strings.Contains(rendered, "extraction_backlog: pending_jobs=2 pending_sessions=1") ||
		!strings.Contains(rendered, "dead_letter=1") ||
		!strings.Contains(rendered, "jobs_by_kind: kind=phase1_extract/v2 pending=1") ||
		!strings.Contains(rendered, "extraction_job: kind=phase1_extract/v2") ||
		!strings.Contains(rendered, "deadline_at=") {
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

func TestStructuredMemoryStatusAndJobsOmitRawDiagnostics(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	secret := "sk-123456789012345678901234"
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	job, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "cli-sensitive-diagnostic",
		Status:    memorystore.JobStatusDeadLetter,
		LastError: "provider returned " + secret, DeadLetterReason: "operator decision " + secret,
	})
	if err != nil {
		t.Fatalf("ensure job: %v", err)
	}
	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	statusDoc := memoryStoreStatusLines(status)
	statusText := strings.Join(statusDoc, "\n")
	if strings.Contains(statusText, secret) || !strings.Contains(statusText, "last_error=present") {
		t.Fatalf("status should summarize but not expose diagnostics:\n%s", statusText)
	}
	jobs, err := store.ListJobs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	jobsText := ui.RenderDocumentPlain(buildChatMemoryJobsDocument(jobs))
	if strings.Contains(jobsText, secret) || !strings.Contains(jobsText, "err=present") {
		t.Fatalf("jobs should summarize but not expose last_error:\n%s", jobsText)
	}
	if job.ID == "" {
		t.Fatal("test job ID is empty")
	}
}

func TestStructuredMemoryJobOperations(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	makeJob := func(key string, status memorystore.JobStatus) memorystore.MemoryJob {
		t.Helper()
		job, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
			Kind: "phase1_extract", JobKey: key, Status: status, Attempt: 1,
			CreatedAt: now, AvailableAt: now, LastError: map[bool]string{true: "failed"}[status == memorystore.JobStatusFailed],
		})
		if err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
		return job
	}
	failed := makeJob("cli-retry", memorystore.JobStatusFailed)
	pending := makeJob("cli-pending", memorystore.JobStatusPending)
	freshPending := makeJob("cli-fresh-pending", memorystore.JobStatusPending)
	succeeded := makeJob("cli-succeeded", memorystore.JobStatusSucceeded)
	_ = store.Close()
	run := func(command string) string {
		t.Helper()
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s handled=%t err=%v", command, handled, err)
		}
		return ui.RenderDocumentPlain(result.Document())
	}
	if got := run("/memory jobs retry " + failed.ID); !strings.Contains(got, "--yes") {
		t.Fatalf("retry without confirmation must show usage: %s", got)
	}
	if got := run("/memory jobs retry " + failed.ID + " --yes --reason operator"); !strings.Contains(got, "已完成") {
		t.Fatalf("retry: %s", got)
	}
	if got := run("/memory jobs dead-letter " + pending.ID); !strings.Contains(got, "--yes") {
		t.Fatalf("dead-letter without confirmation must show usage: %s", got)
	}
	if got := run("/memory jobs dead-letter " + pending.ID + " --yes"); !strings.Contains(got, "已完成") {
		t.Fatalf("dead-letter: %s", got)
	}
	if got := run("/memory jobs deadline-repair 5"); !strings.Contains(got, "dry-run") {
		t.Fatalf("deadline repair must default to dry-run: %s", got)
	}
	if got := run("/memory jobs deadline-repair --apply"); !strings.Contains(got, "--yes") {
		t.Fatalf("deadline repair apply must require confirmation: %s", got)
	}
	if got := run("/memory jobs deadline-repair 5 --apply --yes --reason operator"); !strings.Contains(got, "applied") {
		t.Fatalf("deadline repair apply: %s", got)
	}
	for _, tc := range []struct{ verb, id string }{{"retry", freshPending.ID}, {"dead-letter", succeeded.ID}} {
		if got := run("/memory jobs " + tc.verb + " " + tc.id + " --yes"); !strings.Contains(got, "冲突") {
			t.Fatalf("%s conflict: %s", tc.verb, got)
		}
	}
	secret := "sk-123456789012345678901234"
	if got := run("/memory jobs dead-letter " + failed.ID + " --yes --reason " + secret); strings.Contains(got, secret) {
		t.Fatalf("reason leaked: %s", got)
	}
	if got := run("/memory jobs"); !strings.Contains(got, "memory job summary:") ||
		!strings.Contains(got, "jobs_by_kind:") ||
		!strings.Contains(got, "jobs_by_priority:") {
		t.Fatalf("jobs summary missing from CLI output: %s", got)
	}
}

func TestStructuredMemoryDigestReplayPreviewRequiresBoundRuntime(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		ProjectKey: store.ProjectKey(), SessionID: "cli-replay-session",
		SessionGeneration: 1, TurnID: "cli-replay-turn",
		Status: memorystore.TurnDigestPending, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	runtime := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: root, ProjectKey: store.ProjectKey(),
	})
	session.LocalRuntimeHost = &localChatRuntimeHost{MemoryRuntime: runtime}
	t.Cleanup(func() { _ = store.Close() })

	result, handled, err := tryExecuteStructuredChatCommand(
		session, "/memory jobs replay-preview 999",
	)
	if err != nil || !handled {
		t.Fatalf("replay preview handled=%t err=%v", handled, err)
	}
	rendered := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(rendered, "dry-run") ||
		!strings.Contains(rendered, "count=1") ||
		!strings.Contains(rendered, "limit=500") ||
		!strings.Contains(rendered, "missing_receipt") {
		t.Fatalf("replay preview missing safe report:\n%s", rendered)
	}
	if strings.Contains(rendered, "provider") || strings.Contains(rendered, "sqlite") {
		t.Fatalf("replay preview exposed diagnostics:\n%s", rendered)
	}

	result, handled, err = tryExecuteStructuredChatCommand(
		session, "/memory jobs replay-preview invalid",
	)
	if err != nil || !handled ||
		!strings.Contains(ui.RenderDocumentPlain(result.Document()), "用法") {
		t.Fatalf("invalid limit must show usage, handled=%t err=%v result=%s",
			handled, err, ui.RenderDocumentPlain(result.Document()))
	}

	otherStore, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	session.LocalRuntimeHost = &localChatRuntimeHost{MemoryRuntime: memoryruntime.New(
		otherStore, nil, nil, nil, memoryruntime.Config{
			Mode: "on", ProjectKey: otherStore.ProjectKey(),
		},
	)}
	result, handled, err = tryExecuteStructuredChatCommand(
		session, "/memory jobs replay-preview",
	)
	if err != nil || !handled ||
		!strings.Contains(ui.RenderDocumentPlain(result.Document()), "workspace 不匹配") {
		t.Fatalf("cross-workspace preview must fail closed, handled=%t err=%v result=%s",
			handled, err, ui.RenderDocumentPlain(result.Document()))
	}

	session.LocalRuntimeHost = nil
	result, handled, err = tryExecuteStructuredChatCommand(
		session, "/memory jobs replay-preview",
	)
	if err != nil || !handled {
		t.Fatalf("unbound replay preview handled=%t err=%v", handled, err)
	}
	if rendered = ui.RenderDocumentPlain(result.Document()); !strings.Contains(rendered, "需要已绑定") {
		t.Fatalf("unbound replay preview should fail closed:\n%s", rendered)
	}
}

func TestStructuredMemoryDeadlineRequeue(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	makeJob := func(key string, status memorystore.JobStatus, attempt int, lastError string) memorystore.MemoryJob {
		t.Helper()
		job, ensureErr := store.EnsureJob(context.Background(), memorystore.MemoryJob{
			Kind: "phase1_extract", JobKey: key, Status: status, Attempt: attempt,
			CreatedAt: now.Add(-time.Hour), DeadlineAt: now.Add(-time.Minute),
			LastError: lastError,
		})
		if ensureErr != nil {
			t.Fatalf("ensure %s: %v", key, ensureErr)
		}
		return job
	}
	deadline0 := makeJob("deadline-0", memorystore.JobStatusFailed, 0, "job deadline exceeded")
	deadline1 := makeJob("deadline-1", memorystore.JobStatusFailed, 1, "job deadline exceeded")
	other := makeJob("provider-failed", memorystore.JobStatusFailed, 0, "provider failed")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	run := func(command string) string {
		t.Helper()
		result, handled, runErr := tryExecuteStructuredChatCommand(session, command)
		if runErr != nil || !handled {
			t.Fatalf("%s handled=%t err=%v", command, handled, runErr)
		}
		return ui.RenderDocumentPlain(result.Document())
	}
	dryRun := run("/memory jobs deadline-requeue 10")
	if !strings.Contains(dryRun, "eligible=1") || !strings.Contains(dryRun, "dry-run") {
		t.Fatalf("dry-run output = %s", dryRun)
	}
	if got := run("/memory jobs deadline-requeue --apply"); !strings.Contains(got, "--yes") {
		t.Fatalf("apply without confirmation must show usage: %s", got)
	}
	applied := run("/memory jobs deadline-requeue 10 --apply --yes --reason operator")
	if !strings.Contains(applied, "requeued=1") || !strings.Contains(applied, deadline0.ID) {
		t.Fatalf("apply output = %s", applied)
	}

	reopened, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	requeued, err := reopened.GetJob(context.Background(), deadline0.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != memorystore.JobStatusPending || requeued.DeadlineAt.Before(time.Now().UTC()) {
		t.Fatalf("requeued deadline job = %+v", requeued)
	}
	for _, id := range []string{deadline1.ID, other.ID} {
		job, getErr := reopened.GetJob(context.Background(), id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Status != memorystore.JobStatusFailed {
			t.Fatalf("unrelated failed job %s status = %s", id, job.Status)
		}
	}
}

func TestStructuredMemoryJobsModeOffDoesNotCreateDatabase(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("memory:\n  mode: off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{WorkspaceAnchor: root, RuntimeConfigPath: configPath}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/memory jobs retry missing --yes")
	if err != nil || handled || strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) != "" {
		t.Fatalf("mode=off handled=%t err=%v", handled, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".aicli", "memory-v2.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("mode=off created database (stat err=%v)", err)
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
