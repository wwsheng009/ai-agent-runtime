package chat

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionCreation(t *testing.T) {
	session := NewSession("test-user-id")

	if session.ID == "" {
		t.Error("Expected non-empty session ID")
	}

	if session.UserID != "test-user-id" {
		t.Errorf("Expected user ID 'test-user-id', got '%s'", session.UserID)
	}

	if len(session.History) != 0 {
		t.Errorf("Expected 0 messages, got %d", len(session.History))
	}

	if len(session.Metadata.Context) != 0 {
		t.Errorf("Expected empty context, got %d items", len(session.Metadata.Context))
	}
}

func TestSessionAddMessage(t *testing.T) {
	session := NewSession("test-user")

	msg := types.NewUserMessage("Hello, AI!")
	session.AddMessage(*msg)

	if len(session.History) != 1 {
		t.Errorf("Expected 1 message, got %d", len(session.History))
	}

	retrieved := session.History[0]
	if retrieved.Content != "Hello, AI!" {
		t.Errorf("Expected message content 'Hello, AI!', got '%s'", retrieved.Content)
	}
	if types.MessageID(retrieved) == "" {
		t.Fatal("expected AddMessage to assign message_id")
	}
	if types.TurnID(retrieved) == "" {
		t.Fatal("expected AddMessage to assign turn_id for user message")
	}
}

func TestSessionAddMessageInheritsTurnID(t *testing.T) {
	session := NewSession("test-user")
	session.AddMessage(*types.NewUserMessage("prompt"))
	session.AddMessage(*types.NewAssistantMessage("reply"))
	session.AddMessage(*types.NewToolMessage("tc1", "tool"))

	userTurn := types.TurnID(session.History[0])
	if userTurn == "" {
		t.Fatal("expected user turn_id")
	}
	if types.TurnID(session.History[1]) != userTurn {
		t.Fatalf("assistant turn_id=%q want %q", types.TurnID(session.History[1]), userTurn)
	}
	if types.TurnID(session.History[2]) != userTurn {
		t.Fatalf("tool turn_id=%q want %q", types.TurnID(session.History[2]), userTurn)
	}
	ids := map[string]struct{}{}
	for _, msg := range session.History {
		id := types.MessageID(msg)
		if id == "" {
			t.Fatal("expected message_id on every history entry")
		}
		if _, exists := ids[id]; exists {
			t.Fatalf("duplicate message_id %q", id)
		}
		ids[id] = struct{}{}
	}
}

func TestSessionEnsureMessageIdentitiesBackfillsLegacyHistory(t *testing.T) {
	session := NewSession("test-user")
	// Bypass AddMessage to simulate pre-Phase-6 persisted history.
	session.History = []types.Message{
		{Role: "user", Content: "old user", Metadata: types.NewMetadata()},
		{Role: "assistant", Content: "old assistant", Metadata: types.NewMetadata()},
	}
	session.HistoryLoaded = true

	if !session.EnsureMessageIdentities() {
		t.Fatal("expected legacy history to need identity backfill")
	}
	if types.MessageID(session.History[0]) == "" || types.TurnID(session.History[0]) == "" {
		t.Fatal("expected user message identity after backfill")
	}
	if types.MessageID(session.History[1]) == "" {
		t.Fatal("expected assistant message_id after backfill")
	}
	if types.TurnID(session.History[1]) != types.TurnID(session.History[0]) {
		t.Fatalf("assistant should inherit user turn_id, got %q vs %q",
			types.TurnID(session.History[1]), types.TurnID(session.History[0]))
	}
	if session.EnsureMessageIdentities() {
		t.Fatal("second EnsureMessageIdentities should be idempotent")
	}
}

func TestSessionDerivedTitleSkipsInstructionMessages(t *testing.T) {
	session := NewSession("test-user")
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("Shell guidance:\n- Detected operating system: windows."),
		*types.NewDeveloperMessage("Always answer tersely."),
		*types.NewUserMessage("检查登录流程为什么失败"),
		*types.NewAssistantMessage("我会检查登录链路。"),
	})

	if got := session.Metadata.Title; got != "检查登录流程为什么失败" {
		t.Fatalf("expected title from first user message, got %q", got)
	}
	if got := session.Metadata.TitleSource; got != sessionTitleSourceDerived {
		t.Fatalf("expected derived title source, got %q", got)
	}
}

func TestSessionSystemOnlyDoesNotDeriveInstructionTitle(t *testing.T) {
	session := NewSession("test-user")
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("Shell guidance:\n- Detected operating system: windows."),
	})

	if got := session.Metadata.Title; got != "" {
		t.Fatalf("expected empty title for system-only session, got %q", got)
	}
	preview := session.BuildPreview()
	if preview == nil {
		t.Fatal("expected preview")
	}
	if got := preview.Title; got != "" {
		t.Fatalf("expected empty preview title for system-only session, got %q", got)
	}

	legacy := NewSession("test-user")
	legacy.Metadata.Title = "Shell guidance: - Detected operating system: ..."
	legacy.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("Shell guidance:\n- Detected operating system: windows."),
	})
	if got := legacy.Metadata.Title; got != "" {
		t.Fatalf("expected legacy instruction-only title to be cleared, got %q", got)
	}
}

func TestSessionRepairsLegacyInstructionDerivedTitle(t *testing.T) {
	session := NewSession("test-user")
	session.Metadata.Title = "Shell guidance: - Detected operating system: ..."
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("写一个提交说明"),
		*types.NewAssistantMessage("提交说明如下。"),
	})

	if got := session.Metadata.Title; got != "写一个提交说明" {
		t.Fatalf("expected repaired title from user message, got %q", got)
	}
	if got := session.Metadata.TitleSource; got != sessionTitleSourceDerived {
		t.Fatalf("expected repaired title to be marked derived, got %q", got)
	}
}

func TestSessionManualTitleIsNotOverwrittenByDerivedTitle(t *testing.T) {
	session := NewSession("test-user")
	session.UpdateTitle("保留的手动标题")
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("Shell guidance:\n- Detected operating system: windows."),
		*types.NewUserMessage("这条用户消息不应覆盖标题"),
	})

	if got := session.Metadata.Title; got != "保留的手动标题" {
		t.Fatalf("expected manual title to be preserved, got %q", got)
	}
	if got := session.Metadata.TitleSource; got != sessionTitleSourceManual {
		t.Fatalf("expected manual title source, got %q", got)
	}
}

func TestSessionApplyCompactTitleLineageInheritsRootTitle(t *testing.T) {
	session := NewSession("test-user")
	session.ID = "session-a"
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("检查登录流程为什么失败"),
		*types.NewAssistantMessage("我会检查登录链路。"),
	})
	if got := session.Metadata.Title; got != "检查登录流程为什么失败" {
		t.Fatalf("expected derived root title, got %q", got)
	}

	rootHint := session.CompactRootTitleCandidate()
	session.ReplaceHistory([]types.Message{
		*types.NewSystemMessage("system"),
		*types.NewUserMessage("Compaction summary: login flow failed due to token expiry."),
	})
	session.ApplyCompactTitleLineage(session.ID, rootHint)

	if got := session.Metadata.Title; got != "检查登录流程为什么失败 · compact #1" {
		t.Fatalf("expected compact child title, got %q", got)
	}
	if got := session.Metadata.TitleSource; got != sessionTitleSourceCompact {
		t.Fatalf("expected compact title source, got %q", got)
	}
	if got := contextStringValue(session.Metadata.Context, ContextCompactRootTitle); got != "检查登录流程为什么失败" {
		t.Fatalf("expected root title context, got %q", got)
	}
	if got := contextIntValue(session.Metadata.Context, ContextCompactGeneration); got != 1 {
		t.Fatalf("expected generation 1, got %d", got)
	}
	if got := contextStringValue(session.Metadata.Context, ContextCompactParentSessionID); got != "session-a" {
		t.Fatalf("expected parent session id, got %q", got)
	}

	// Multi-round compact: generation increments; root title stays stable.
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("Second compaction summary should not become the title."),
	})
	session.ApplyCompactTitleLineage(session.ID, session.CompactRootTitleCandidate())
	if got := session.Metadata.Title; got != "检查登录流程为什么失败 · compact #2" {
		t.Fatalf("expected second compact child title, got %q", got)
	}
	if got := contextIntValue(session.Metadata.Context, ContextCompactGeneration); got != 2 {
		t.Fatalf("expected generation 2, got %d", got)
	}
	if got := contextStringValue(session.Metadata.Context, ContextCompactRootTitle); got != "检查登录流程为什么失败" {
		t.Fatalf("expected stable root title, got %q", got)
	}

	// Compact titles are sticky across later ReplaceHistory.
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("later user message"),
	})
	if got := session.Metadata.Title; got != "检查登录流程为什么失败 · compact #2" {
		t.Fatalf("expected sticky compact title after ReplaceHistory, got %q", got)
	}
}

func TestFormatCompactChildTitleAndStripMarker(t *testing.T) {
	if got := formatCompactChildTitle("Root", 1); got != "Root · compact #1" {
		t.Fatalf("unexpected child title: %q", got)
	}
	if got := stripCompactTitleMarker("Root · compact #3"); got != "Root" {
		t.Fatalf("expected marker strip, got %q", got)
	}
	if got := stripCompactTitleMarker("plain title"); got != "plain title" {
		t.Fatalf("expected unchanged title, got %q", got)
	}
}

func TestSessionManualTitleClearIsPreserved(t *testing.T) {
	session := NewSession("test-user")
	session.UpdateTitle("")
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("这条消息不应重新生成标题"),
	})

	if got := session.Metadata.Title; got != "" {
		t.Fatalf("expected cleared manual title to remain empty, got %q", got)
	}
	if got := session.Metadata.TitleSource; got != sessionTitleSourceManual {
		t.Fatalf("expected cleared title to remain manual, got %q", got)
	}
	if got := session.BuildPreview().Title; got != "" {
		t.Fatalf("expected preview title to remain empty, got %q", got)
	}
}

func TestSessionCloneWithoutHistoryPreservesMetadataOnly(t *testing.T) {
	session := NewSession("test-user")
	session.AddTag("memory")
	session.SetContext("mode", "bounded")
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("first"),
		*types.NewAssistantMessage("second"),
	})
	session.CanonicalMessageCount = 42
	session.SetHeadOffset(1)
	session.SetTTL(time.Hour)

	clone := session.CloneWithoutHistory()
	if clone == nil {
		t.Fatal("expected metadata-only clone")
	}
	if len(clone.History) != 0 || clone.HistoryLoaded {
		t.Fatalf("expected unloaded history, got len=%d loaded=%v", len(clone.History), clone.HistoryLoaded)
	}
	if clone.CanonicalMessageCount != 42 || clone.HeadOffset != 1 {
		t.Fatalf("session cursors changed: canonical=%d head=%d", clone.CanonicalMessageCount, clone.HeadOffset)
	}
	if clone.Metadata.Title != session.Metadata.Title || clone.Metadata.Summary != session.Metadata.Summary {
		t.Fatalf("derived metadata changed: %#v", clone.Metadata)
	}
	clone.Metadata.Tags[0] = "changed"
	clone.Metadata.Context["mode"] = "changed"
	if session.Metadata.Tags[0] != "memory" || session.Metadata.Context["mode"] != "bounded" {
		t.Fatal("metadata-only clone shares mutable metadata containers")
	}
	if clone.ExpiresAt == session.ExpiresAt {
		t.Fatal("metadata-only clone shares expiration pointer")
	}
}

func TestSessionCloneWithoutHistoryAvoidsHistoryAllocations(t *testing.T) {
	session := NewSession("test-user")
	session.History = make([]types.Message, 128)
	for index := range session.History {
		session.History[index] = *types.NewToolMessage("call", "payload")
		session.History[index].Metadata.Set("index", index)
	}

	fullAllocs := testing.AllocsPerRun(10, func() {
		if session.Clone() == nil {
			panic("nil full clone")
		}
	})
	metadataAllocs := testing.AllocsPerRun(10, func() {
		if session.CloneWithoutHistory() == nil {
			panic("nil metadata clone")
		}
	})
	if metadataAllocs >= fullAllocs/4 {
		t.Fatalf("metadata clone still scales with history: full=%.0f metadata=%.0f", fullAllocs, metadataAllocs)
	}
}

func TestSessionSetGetContext(t *testing.T) {
	session := NewSession("test-user")

	session.SetContext("key1", "value1")
	session.SetContext("key2", 123)

	val1, ok := session.GetContext("key1")
	if !ok || val1 != "value1" {
		t.Errorf("Expected context key1='value1', got %v", val1)
	}

	val2, ok := session.GetContext("key2")
	if !ok || val2 != 123 {
		t.Errorf("Expected context key2=123, got %v", val2)
	}

	_, ok = session.GetContext("nonexistent")
	if ok {
		t.Error("Expected context key 'nonexistent' to not exist")
	}
}

func TestSessionAddTags(t *testing.T) {
	session := NewSession("test-user")

	session.AddTags("conversation", "support")

	if len(session.Metadata.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(session.Metadata.Tags))
	}

	if !session.HasTag("conversation") {
		t.Error("Expected tag 'conversation' to exist")
	}

	if !session.HasTag("support") {
		t.Error("Expected tag 'support' to exist")
	}
}

func TestSessionSetTTL(t *testing.T) {
	session := NewSession("test-user")
	ttl := 1 * time.Hour
	session.SetTTL(ttl)

	if session.ExpiresAt == nil {
		t.Error("Expected ExpiresAt to be set")
	}

	if time.Until(*session.ExpiresAt) > 1*time.Hour {
		t.Error("Expected ExpiresAt to be approximately 1 hour from now")
	}
}

func TestSessionIsExpired(t *testing.T) {
	session := NewSession("test-user")

	// Empty ExpiresAt means never expires
	if session.IsExpired() {
		t.Error("Expected session with no ExpiresAt to not be expired")
	}

	// Set ExpiresAt to past
	expiresAt := time.Now().Add(-1 * time.Hour)
	session.ExpiresAt = &expiresAt

	if !session.IsExpired() {
		t.Error("Expected expired session to be expired")
	}
}

func TestInMemoryStorage(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()

	session := NewSession("test-user")

	// Test Save
	err := storage.Save(ctx, session)
	if err != nil {
		t.Fatalf("Failed to save session: %v", err)
	}

	// Test Get
	retrieved, err := storage.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrieved.ID != session.ID {
		t.Errorf("Expected session ID '%s', got '%s'", session.ID, retrieved.ID)
	}

	// Test Exists
	exists, err := storage.Exists(ctx, session.ID)
	if err != nil {
		t.Fatalf("Failed to check existence: %v", err)
	}
	if !exists {
		t.Error("Expected session to exist")
	}

	// Test Delete
	err = storage.Delete(ctx, session.ID)
	if err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}

	_, err = storage.Get(ctx, session.ID)
	if err == nil {
		t.Error("Expected error when getting deleted session")
	}
}

func TestInMemoryStorageListAndSearch(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()

	// Create multiple sessions
	session1 := NewSession("user-1")
	session1.SetContext("category", "support")
	session1.AddTags("priority")

	session2 := NewSession("user-2")
	session2.SetContext("category", "sales")

	session3 := NewSession("user-1")
	session3.SetContext("category", "support")

	storage.Save(ctx, session1)
	storage.Save(ctx, session2)
	storage.Save(ctx, session3)

	// Test ListAll
	sessions, err := storage.ListAll(ctx, 10, 0)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}

	// Test ListByUser
	userSessions, err := storage.ListByUser(ctx, "user-1", 10, 0)
	if err != nil {
		t.Fatalf("Failed to list by user: %v", err)
	}
	if len(userSessions) != 2 {
		t.Errorf("Expected 2 sessions for user-1, got %d", len(userSessions))
	}

	// Test SearchContext
	supportSessions, err := storage.SearchContext(ctx, "category", "support")
	if err != nil {
		t.Fatalf("Failed to search context: %v", err)
	}
	if len(supportSessions) != 2 {
		t.Errorf("Expected 2 sessions with category=support, got %d", len(supportSessions))
	}

	// Test SearchTags
	prioritySessions, err := storage.SearchTags(ctx, "priority")
	if err != nil {
		t.Fatalf("Failed to search tags: %v", err)
	}
	if len(prioritySessions) != 1 {
		t.Errorf("Expected 1 session with priority tag, got %d", len(prioritySessions))
	}
}

func TestSessionManager(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// Create session
	session, err := manager.CreateSession(ctx, "manager-test-user")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session.UserID != "manager-test-user" {
		t.Errorf("Expected user ID 'manager-test-user', got '%s'", session.UserID)
	}

	if session.ID == "" {
		t.Error("Expected non-empty session ID")
	}

	// Get session
	retrieved, err := manager.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrieved.ID != session.ID {
		t.Errorf("Expected same session ID")
	}

	// Add message
	msg := types.NewUserMessage("Test message")
	err = manager.AddMessage(ctx, session.ID, *msg)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	updatedSession, _ := manager.GetSession(ctx, session.ID)
	if len(updatedSession.History) != 1 {
		t.Errorf("Expected 1 message, got %d", len(updatedSession.History))
	}
}

func TestSessionManagerCleanup(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// Create session with short TTL
	session, _ := manager.CreateSession(ctx, "cleanup-test-user")
	manager.UpdateContext(ctx, session.ID, "test", "cleanup")
	session.SetTTL(10 * time.Millisecond)
	storage.Save(ctx, session)

	// Wait for session to expire
	time.Sleep(50 * time.Millisecond)

	// Run cleanup
	count, err := manager.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}

	if count == 0 {
		t.Error("Expected at least 1 expired session to be cleaned up")
	}

	// Verify session is deleted
	_, err = manager.GetSession(ctx, session.ID)
	if err == nil {
		t.Error("Expected error when getting cleaned up session")
	}
}

func TestSessionManagerSearch(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// Create sessions with tags
	session1, _ := manager.CreateSession(ctx, "search-user-1")
	manager.AddTags(ctx, session1.ID, "support", "high-priority")

	session2, _ := manager.CreateSession(ctx, "search-user-2")
	manager.AddTags(ctx, session2.ID, "sales")

	session3, _ := manager.CreateSession(ctx, "search-user-1")
	manager.AddTags(ctx, session3.ID, "support")

	// Search sessions
	results, err := manager.SearchSessions(ctx, &SessionSearchOptions{
		UserID: "search-user-1",
		Tags:   []string{"support"},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("Failed to search: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(results))
	}
}

func TestSessionManagerArchive(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// Create and archive session
	session, _ := manager.CreateSession(ctx, "archive-user")
	manager.UpdateContext(ctx, session.ID, "test", "archive")
	manager.AddTags(ctx, session.ID, "archived")

	// Archive
	err := manager.ArchiveSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("Failed to archive: %v", err)
	}

	// Verify archived state
	retrieved, _ := manager.GetSession(ctx, session.ID)
	if retrieved.State != StateArchived {
		t.Error("Expected state to be archived")
	}
}
