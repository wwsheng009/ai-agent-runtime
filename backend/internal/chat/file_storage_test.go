package chat

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionBuildPreviewDerivesTitleAndSummary(t *testing.T) {
	session := NewSession("preview-user")
	session.AddMessage(*types.NewUserMessage("   这是一个很长的会话标题，用来验证自动标题截断能力和摘要更新。   "))
	session.AddMessage(*types.NewAssistantMessage("这是助手的最新回复，会成为摘要内容。"))

	preview := session.BuildPreview()
	if preview == nil {
		t.Fatal("expected preview")
	}
	if preview.Title == "" {
		t.Fatal("expected derived title")
	}
	if preview.Summary != "这是助手的最新回复，会成为摘要内容。" {
		t.Fatalf("unexpected summary: %q", preview.Summary)
	}
	if preview.MessageCount != 2 {
		t.Fatalf("expected message count 2, got %d", preview.MessageCount)
	}
}

func TestFileStorageLoadRepairsLegacyInstructionDerivedTitle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	storage, err := NewFileStorage(dir)
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}

	session := NewSession("preview-user")
	session.Metadata.Title = "Shell guidance: - Detected operating system: ..."
	session.History = []types.Message{*types.NewUserMessage("恢复旧会话标题")}
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	loaded, err := storage.Load(ctx, session.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got := loaded.Metadata.Title; got != "恢复旧会话标题" {
		t.Fatalf("expected repaired loaded title, got %q", got)
	}
}

func TestFileStorageRoundTripAndLatest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	storage, err := NewFileStorage(dir)
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	manager := NewSessionManager(storage, &SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      10,
		CleanupInterval: 0,
		AutoArchive:     false,
	})

	first, err := manager.Create(ctx, "user-a")
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	if err := manager.AddMessage(ctx, first.ID, *types.NewUserMessage("hello")); err != nil {
		t.Fatalf("add first message: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	second, err := manager.Create(ctx, "user-a")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	if err := manager.SetTitle(ctx, second.ID, "named session"); err != nil {
		t.Fatalf("set title: %v", err)
	}
	if err := manager.AddMessage(ctx, second.ID, *types.NewAssistantMessage("latest content")); err != nil {
		t.Fatalf("add second message: %v", err)
	}

	loaded, err := manager.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("load second session: %v", err)
	}
	if loaded.Metadata.Title != "named session" {
		t.Fatalf("unexpected loaded title: %q", loaded.Metadata.Title)
	}

	latest, err := manager.GetLatest(ctx, "user-a")
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("expected latest session %s, got %s", second.ID, latest.ID)
	}

	previews, err := manager.ListPreviews(ctx, "user-a", 10, 0)
	if err != nil {
		t.Fatalf("list previews: %v", err)
	}
	if len(previews) != 2 {
		t.Fatalf("expected 2 previews, got %d", len(previews))
	}
	if previews[0].ID != second.ID {
		t.Fatalf("expected newest preview first, got %s", previews[0].ID)
	}
}

func TestFileStorageLoadReturnsOwnedDecodedSession(t *testing.T) {
	storage, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	session := NewSession("owner-test")
	session.ReplaceHistory([]types.Message{*types.NewUserMessage("original")})
	if err := storage.Save(context.Background(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	first, err := storage.Load(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	first.History[0].Content = "mutated"
	second, err := storage.Load(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.History[0].Content != "original" {
		t.Fatalf("disk session changed through loaded object: %q", second.History[0].Content)
	}
}

func TestFileStorageStreamingEncodeRemovesFailedTempFile(t *testing.T) {
	storage, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	session := NewSession("encode-failure")
	session.Metadata.Context["unsupported"] = make(chan struct{})
	if err := storage.Save(context.Background(), session); err == nil {
		t.Fatal("expected unsupported metadata encode to fail")
	}
	if _, err := os.Stat(storage.sessionPath(session.ID) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("failed temp file was not removed: %v", err)
	}
}

func TestFileStorageMetadataListingScansHistoryWithoutRetainingIt(t *testing.T) {
	storage, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	legacy := NewSession("metadata-user")
	legacy.History = []types.Message{
		*types.NewSystemMessage("instructions"),
		*types.NewUserMessage("derived legacy title"),
		*types.NewAssistantMessage("latest legacy summary"),
	}
	legacy.Metadata.Title = ""
	legacy.Metadata.TitleSource = ""
	legacy.Metadata.Summary = ""
	legacy.CanonicalMessageCount = 0
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy session: %v", err)
	}
	if err := os.WriteFile(storage.sessionPath(legacy.ID), payload, 0644); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	metadata, err := storage.ListMetadataPage(context.Background(), "metadata-user", 10, 0)
	if err != nil {
		t.Fatalf("list metadata: %v", err)
	}
	if len(metadata) != 1 {
		t.Fatalf("expected one metadata row, got %d", len(metadata))
	}
	item := metadata[0]
	if item.HistoryLoaded || len(item.History) != 0 {
		t.Fatalf("metadata listing retained history: loaded=%v len=%d", item.HistoryLoaded, len(item.History))
	}
	if item.CanonicalMessageCount != 3 || item.Metadata.Title != "derived legacy title" || item.Metadata.Summary != "latest legacy summary" {
		t.Fatalf("unexpected scanned metadata: %#v", item)
	}
	previews, err := storage.ListPreviews(context.Background(), "metadata-user", 10, 0)
	if err != nil {
		t.Fatalf("list previews: %v", err)
	}
	if len(previews) != 1 || previews[0].MessageCount != 3 || previews[0].Title != "derived legacy title" {
		t.Fatalf("unexpected scanned preview: %#v", previews)
	}
}
