package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func memoryToolTestConfig(t *testing.T) *runtimecfg.RuntimeConfig {
	t.Helper()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.Memory.Mode = "on"
	cfg.Memory.Injection.Mode = "hybrid"
	cfg.Memory.Tools.Enabled = true
	cfg.Memory.Tools.AllowCorrect = true
	cfg.Memory.Tools.AllowForget = true
	cfg.Memory.Tools.AllowRollback = true
	return cfg
}

func TestMemoryToolsLifecycle(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := toolctx.WithSessionID(context.Background(), "session-tools")
	add := NewMemoryAddTool(cfg)
	result, err := add.Execute(ctx, map[string]interface{}{
		"text": "Use revisioned memory writes.", "title": "Revisioned writes",
		"summary": "Keep durable memory revisions auditable.", "canonical_id": "decision:revisioned-writes",
		"kind": "decision", "tags": []interface{}{"memory"},
	})
	if err != nil || !result.Success {
		t.Fatalf("memory_add = %+v, %v", result, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(result.Content), &note); err != nil || note.ID == "" {
		t.Fatalf("memory_add content = %q, %v", result.Content, err)
	}
	pinnedOff, err := NewMemoryPinTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "pinned": false,
	})
	if err != nil || !pinnedOff.Success || strings.Contains(pinnedOff.Content, `"pinned": true`) {
		t.Fatalf("memory_pin off = %+v, %v", pinnedOff, err)
	}
	pinnedOn, err := NewMemoryPinTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !pinnedOn.Success || !strings.Contains(pinnedOn.Content, `"pinned": true`) {
		t.Fatalf("memory_pin on = %+v, %v", pinnedOn, err)
	}

	searchResult, err := NewMemorySearchTool(cfg).Execute(ctx, map[string]interface{}{"query": "revisioned"})
	if err != nil || !searchResult.Success || !strings.Contains(searchResult.Content, note.ID) {
		t.Fatalf("memory_search = %+v, %v", searchResult, err)
	}
	readResult, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !readResult.Success {
		t.Fatalf("memory_read = %+v, %v", readResult, err)
	}
	var readPayload struct {
		Record memorystore.Record `json:"record"`
	}
	if err := json.Unmarshal([]byte(readResult.Content), &readPayload); err != nil ||
		readPayload.Record.ID != note.ID || readPayload.Record.Revision <= 0 {
		t.Fatalf("memory_read record = %q, %v", readResult.Content, err)
	}
	historyResult, err := NewMemoryHistoryTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !historyResult.Success ||
		!strings.Contains(historyResult.Content, `"versions"`) ||
		!strings.Contains(historyResult.Content, "Use revisioned memory writes.") ||
		strings.Contains(historyResult.Content, "source_quote") ||
		strings.Contains(historyResult.Content, `"metadata"`) {
		t.Fatalf("memory_history = %+v, %v", historyResult, err)
	}
	rollbackResult, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": 1, "expected_revision": readPayload.Record.Revision,
		"reason": "restore the original user-authored wording", "confirm": true,
	})
	if err != nil || !rollbackResult.Success || !strings.Contains(rollbackResult.Content, `"ok": true`) {
		t.Fatalf("memory_rollback = %+v, %v", rollbackResult, err)
	}
	var rolledBack struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal([]byte(rollbackResult.Content), &rolledBack); err != nil ||
		rolledBack.ID != note.ID || rolledBack.Revision <= readPayload.Record.Revision {
		t.Fatalf("memory_rollback record = %q, %v", rollbackResult.Content, err)
	}
	restoredRead, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !restoredRead.Success || !strings.Contains(restoredRead.Content, "Use revisioned memory writes.") {
		t.Fatalf("memory_read after rollback = %+v, %v", restoredRead, err)
	}
	staleRollback, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": 1, "expected_revision": readPayload.Record.Revision,
		"reason": "stale rollback must be rejected", "confirm": true,
	})
	if err != nil || staleRollback.Success {
		t.Fatalf("memory_rollback stale revision = %+v, %v", staleRollback, err)
	}
	crossSessionHistory, err := NewMemoryHistoryTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "other-session"),
		map[string]interface{}{"id": note.ID},
	)
	if err != nil || crossSessionHistory.Success {
		t.Fatalf("cross-session memory_history = %+v, %v", crossSessionHistory, err)
	}
	crossSession, err := NewMemorySessionContextTool(cfg).Execute(ctx, map[string]interface{}{"session_id": "other-session"})
	if err != nil || crossSession.Success {
		t.Fatalf("cross-session context = %+v, %v", crossSession, err)
	}
	ownSession, err := NewMemorySessionContextTool(cfg).Execute(ctx, map[string]interface{}{"session_id": "session-tools"})
	if err != nil || !ownSession.Success || !strings.Contains(ownSession.Content, note.ID) {
		t.Fatalf("own-session context = %+v, %v", ownSession, err)
	}
	correctResult, err := NewMemoryCorrectTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "text": "Use correction revisions.",
	})
	if err != nil || !correctResult.Success {
		t.Fatalf("memory_correct = %+v, %v", correctResult, err)
	}
	var correction memorystore.Record
	if err := json.Unmarshal([]byte(correctResult.Content), &correction); err != nil || correction.ID == "" {
		t.Fatalf("decode correction = %q, %v", correctResult.Content, err)
	}
	supersededRead, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !supersededRead.Success {
		t.Fatalf("memory_read superseded record = %+v, %v", supersededRead, err)
	}
	var supersededPayload struct {
		Record memorystore.Record `json:"record"`
	}
	if err := json.Unmarshal([]byte(supersededRead.Content), &supersededPayload); err != nil ||
		supersededPayload.Record.Status != memorystore.StatusSuperseded {
		t.Fatalf("decode superseded record = %q, %v", supersededRead.Content, err)
	}
	withoutRevertSupersede, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": rolledBack.Revision, "expected_revision": supersededPayload.Record.Revision,
		"reason": "supersede rollback requires explicit confirmation", "confirm": true,
	})
	if err != nil || withoutRevertSupersede.Success {
		t.Fatalf("memory_rollback without revert_supersede = %+v, %v", withoutRevertSupersede, err)
	}
	revertSupersede, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": rolledBack.Revision, "expected_revision": supersededPayload.Record.Revision,
		"reason": "restore the superseded record owned by this session", "confirm": true,
		"revert_supersede": true,
	})
	if err != nil || !revertSupersede.Success {
		t.Fatalf("memory_rollback with revert_supersede = %+v, %v", revertSupersede, err)
	}
	successorRead, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": correction.ID})
	if err != nil || !successorRead.Success || !strings.Contains(successorRead.Content, `"status": "superseded"`) {
		t.Fatalf("successor after memory_rollback = %+v, %v", successorRead, err)
	}

	statsResult, err := NewMemoryStatsTool(cfg).Execute(ctx, nil)
	if err != nil || !statsResult.Success || !strings.Contains(statsResult.Content, `"mode": "on"`) {
		t.Fatalf("memory_stats = %+v, %v", statsResult, err)
	}
	forgetResult, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "confirm": true,
	})
	if err != nil || !forgetResult.Success {
		t.Fatalf("memory_forget = %+v, %v", forgetResult, err)
	}
	afterForget, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || afterForget.Success {
		t.Fatalf("memory_read after forget = %+v, %v", afterForget, err)
	}
}

func TestMemoryCoreGetToolUsesReviewedActiveCoreAndRechecksSources(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := context.Background()

	empty, err := NewMemoryCoreGetTool(cfg).Execute(ctx, nil)
	if err != nil || !empty.Success || !strings.Contains(empty.Content, `"status": "not_found"`) {
		t.Fatalf("memory_core_get without active core = %+v, %v", empty, err)
	}

	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: cfg.Workspace.Root,
		Mode:        cfg.Memory.Mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text: "Reviewed project memory source.",
		Kind: "decision",
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	candidate, err := store.CreateCoreCandidate(ctx, memorystore.CoreMemoryCandidateOptions{
		Content:   "Reviewed project Core Memory.",
		SourceIDs: []string{source.ID},
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.ActivateCore(ctx, candidate.ID, "admin:test"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	activeCtx := toolctx.WithTurnID(toolctx.WithSessionID(ctx, "session-core-read"), "turn-core-read")
	active, err := NewMemoryCoreGetTool(cfg).Execute(activeCtx, nil)
	if err != nil || !active.Success ||
		!strings.Contains(active.Content, `"status": "active"`) ||
		!strings.Contains(active.Content, "Reviewed project Core Memory.") ||
		!strings.Contains(active.Content, source.ID) {
		t.Fatalf("memory_core_get active = %+v, %v", active, err)
	}

	store, err = memorystore.New(memorystore.Config{
		ProjectRoot: cfg.Workspace.Root,
		Mode:        cfg.Memory.Mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	expired := memorystore.StatusExpired
	if _, err := store.UpdateRecord(ctx, source.ID, memorystore.RecordPatch{Status: &expired}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = memorystore.New(memorystore.Config{
		ProjectRoot: cfg.Workspace.Root,
		Mode:        cfg.Memory.Mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreUsage, err := store.ListCoreUsage(context.Background(), memorystore.CoreUsageFilter{
		CoreID: candidate.ID, Limit: 10,
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if len(coreUsage) != 1 || coreUsage[0].UseKind != memorystore.UsageRead {
		_ = store.Close()
		t.Fatalf("memory_core_get usage = %+v", coreUsage)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	invalidated, err := NewMemoryCoreGetTool(cfg).Execute(ctx, nil)
	if err != nil || invalidated.Success ||
		!strings.Contains(invalidated.Content, "no longer eligible") {
		t.Fatalf("memory_core_get invalidated source = %+v, %v", invalidated, err)
	}
}

func TestMemoryToolsForgetRequiresConfirmAndConfigGate(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := context.Background()
	addResult, err := NewMemoryAddTool(cfg).Execute(ctx, map[string]interface{}{"text": "forget gate"})
	if err != nil || !addResult.Success {
		t.Fatalf("memory_add = %+v, %v", addResult, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(addResult.Content), &note); err != nil {
		t.Fatalf("decode note: %v", err)
	}
	withoutConfirm, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || withoutConfirm.Success {
		t.Fatalf("memory_forget without confirm = %+v, %v", withoutConfirm, err)
	}
	cfg.Memory.Tools.AllowForget = false
	disabled, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID, "confirm": true})
	if err != nil || disabled.Success {
		t.Fatalf("memory_forget disabled = %+v, %v", disabled, err)
	}
}

func TestMemoryRollbackRequiresConfirmOwnershipAndConfigGate(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := toolctx.WithSessionID(context.Background(), "rollback-owner")
	addResult, err := NewMemoryAddTool(cfg).Execute(ctx, map[string]interface{}{"text": "rollback ownership gate"})
	if err != nil || !addResult.Success {
		t.Fatalf("memory_add = %+v, %v", addResult, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(addResult.Content), &note); err != nil || note.ID == "" {
		t.Fatalf("decode note = %q, %v", addResult.Content, err)
	}
	readResult, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !readResult.Success {
		t.Fatalf("memory_read = %+v, %v", readResult, err)
	}
	var payload struct {
		Record memorystore.Record `json:"record"`
	}
	if err := json.Unmarshal([]byte(readResult.Content), &payload); err != nil {
		t.Fatalf("decode record = %v", err)
	}

	withoutConfirm, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": 1, "expected_revision": payload.Record.Revision,
		"reason": "missing explicit confirmation",
	})
	if err != nil || withoutConfirm.Success {
		t.Fatalf("memory_rollback without confirm = %+v, %v", withoutConfirm, err)
	}
	crossSession, err := NewMemoryRollbackTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "other-session"),
		map[string]interface{}{
			"id": note.ID, "target_version": 1, "expected_revision": payload.Record.Revision,
			"reason": "cross-session rollback", "confirm": true,
		},
	)
	if err != nil || crossSession.Success {
		t.Fatalf("cross-session memory_rollback = %+v, %v", crossSession, err)
	}
	cfg.Memory.Tools.AllowRollback = false
	disabled, err := NewMemoryRollbackTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "target_version": 1, "expected_revision": payload.Record.Revision,
		"reason": "disabled rollback", "confirm": true,
	})
	if err != nil || disabled.Success {
		t.Fatalf("memory_rollback disabled = %+v, %v", disabled, err)
	}
}

func TestMemoryToolsHidePrivateRecordsFromAgentSurface(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: cfg.Workspace.Root, Mode: "on"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "fact", Text: "private agent-only record",
		Confidence: 0.9, Sensitivity: "private", SourceHash: "private-tool", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	active := memorystore.StatusActive
	if _, err := store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	archivedCandidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "fact", Text: "archived agent-only record",
		Confidence: 0.9, Archived: true, SourceHash: "archived-tool", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("Insert archived candidate: %v", err)
	}
	if _, err := store.UpdateRecord(context.Background(), archivedCandidate.ID, memorystore.RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate archived: %v", err)
	}
	metadataCandidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "fact", Text: "safe visible text", Confidence: 0.9,
		SourceHash: "secret-metadata-tool", SourceRevision: 1,
		Metadata: map[string]interface{}{"credential": "sk-123456789012345678901234"},
	})
	if err != nil {
		t.Fatalf("Insert metadata candidate: %v", err)
	}
	if _, err := store.UpdateRecord(context.Background(), metadataCandidate.ID, memorystore.RecordPatch{Status: &active}); err != nil {
		t.Fatalf("activate metadata candidate: %v", err)
	}
	_ = store.Close()

	searchResult, err := NewMemorySearchTool(cfg).Execute(context.Background(), map[string]interface{}{"query": "private"})
	if err != nil || !searchResult.Success || strings.Contains(searchResult.Content, candidate.ID) {
		t.Fatalf("memory_search leaked private record: %+v, %v", searchResult, err)
	}
	readResult, err := NewMemoryReadTool(cfg).Execute(context.Background(), map[string]interface{}{"id": candidate.ID})
	if err != nil || readResult.Success {
		t.Fatalf("memory_read private record = %+v, %v", readResult, err)
	}
	privateHistory, err := NewMemoryHistoryTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "s1"),
		map[string]interface{}{"id": candidate.ID},
	)
	if err != nil || privateHistory.Success {
		t.Fatalf("memory_history private record = %+v, %v", privateHistory, err)
	}
	privateRollback, err := NewMemoryRollbackTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "s1"),
		map[string]interface{}{
			"id": candidate.ID, "target_version": 1, "expected_revision": 2,
			"reason": "private record must remain inaccessible", "confirm": true,
		},
	)
	if err != nil || privateRollback.Success {
		t.Fatalf("memory_rollback private record = %+v, %v", privateRollback, err)
	}
	archivedSearch, err := NewMemorySearchTool(cfg).Execute(context.Background(), map[string]interface{}{"query": "archived"})
	if err != nil || !archivedSearch.Success || strings.Contains(archivedSearch.Content, archivedCandidate.ID) {
		t.Fatalf("memory_search leaked archived record: %+v, %v", archivedSearch, err)
	}
	archivedRead, err := NewMemoryReadTool(cfg).Execute(context.Background(), map[string]interface{}{"id": archivedCandidate.ID})
	if err != nil || archivedRead.Success {
		t.Fatalf("memory_read archived record = %+v, %v", archivedRead, err)
	}
	archivedHistory, err := NewMemoryHistoryTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "s1"),
		map[string]interface{}{"id": archivedCandidate.ID},
	)
	if err != nil || archivedHistory.Success {
		t.Fatalf("memory_history archived record = %+v, %v", archivedHistory, err)
	}
	archivedRollback, err := NewMemoryRollbackTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "s1"),
		map[string]interface{}{
			"id": archivedCandidate.ID, "target_version": 1, "expected_revision": 2,
			"reason": "archived record must remain inaccessible", "confirm": true,
		},
	)
	if err != nil || archivedRollback.Success {
		t.Fatalf("memory_rollback archived record = %+v, %v", archivedRollback, err)
	}
	secretMetadataRollback, err := NewMemoryRollbackTool(cfg).Execute(
		toolctx.WithSessionID(context.Background(), "s1"),
		map[string]interface{}{
			"id": metadataCandidate.ID, "target_version": 1, "expected_revision": 2,
			"reason": "metadata containing a secret-shaped value must be rejected", "confirm": true,
		},
	)
	if err != nil || secretMetadataRollback.Success {
		t.Fatalf("memory_rollback secret metadata = %+v, %v", secretMetadataRollback, err)
	}
	archivedPin, err := NewMemoryPinTool(cfg).Execute(context.Background(), map[string]interface{}{"id": archivedCandidate.ID})
	if err != nil || archivedPin.Success {
		t.Fatalf("memory_pin archived record = %+v, %v", archivedPin, err)
	}
	archivedWhyNot, err := NewMemoryWhyNotTool(cfg).Execute(context.Background(), map[string]interface{}{"query": "archived"})
	if err != nil || !archivedWhyNot.Success || strings.Contains(archivedWhyNot.Content, archivedCandidate.ID) {
		t.Fatalf("memory_why_not leaked archived record: %+v, %v", archivedWhyNot, err)
	}
}

func TestMemoryToolsRecentWhyAndWhyNot(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := toolctx.WithSessionID(context.Background(), "session-diagnostics")
	add := NewMemoryAddTool(cfg)
	first, err := add.Execute(ctx, map[string]interface{}{"text": "alpha diagnostic memory", "kind": "fact"})
	if err != nil || !first.Success {
		t.Fatalf("memory_add first = %+v, %v", first, err)
	}
	second, err := add.Execute(ctx, map[string]interface{}{"text": "beta diagnostic memory", "kind": "fact"})
	if err != nil || !second.Success {
		t.Fatalf("memory_add second = %+v, %v", second, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(first.Content), &note); err != nil || note.ID == "" {
		t.Fatalf("decode note = %q, %v", first.Content, err)
	}

	recent, err := NewMemoryRecentTool(cfg).Execute(ctx, map[string]interface{}{"limit": 1})
	if err != nil || !recent.Success || !strings.Contains(recent.Content, `"count": 1`) {
		t.Fatalf("memory_recent = %+v, %v", recent, err)
	}
	why, err := NewMemoryWhyTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !why.Success || !strings.Contains(why.Content, note.ID) || !strings.Contains(why.Content, `"usage"`) {
		t.Fatalf("memory_why = %+v, %v", why, err)
	}
	whyNot, err := NewMemoryWhyNotTool(cfg).Execute(ctx, map[string]interface{}{"query": "alpha"})
	if err != nil || !whyNot.Success || !strings.Contains(whyNot.Content, "selected_ids") || !strings.Contains(whyNot.Content, "dropped") {
		t.Fatalf("memory_why_not = %+v, %v", whyNot, err)
	}
}
