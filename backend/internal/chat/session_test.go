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

