package chat

import (
	"context"
	"testing"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
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
