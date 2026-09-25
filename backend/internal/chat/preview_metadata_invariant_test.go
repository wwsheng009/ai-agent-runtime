package chat

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 懒修复的硬约束（SessionStoragePreviewWriter 契约）：
//  1. 只改 title/title_source/summary；
//  2. 不改 updated_at —— 否则一次列表渲染就会让会话在侧栏里跳到最前；
//  3. 幂等 —— 列表每次渲染都可能调用，重复调用不得产生写入放大或值漂移。
func TestPersistPreviewMetadataKeepsUpdatedAtAndIsIdempotent(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			var (
				manager *SessionManager
				store   SessionStorage
			)
			switch backend {
			case "sqlite":
				sqliteStore := newTestSQLiteSessionStorage(t, nil)
				store = sqliteStore
				manager = NewSessionManager(sqliteStore, nil)
			default:
				memoryStore := NewInMemoryStorage()
				store = memoryStore
				manager = NewSessionManager(memoryStore, nil)
			}
			t.Cleanup(manager.Stop)

			session, err := manager.Create(ctx, "preview-user")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			session.AddMessage(*types.NewUserMessage("第一条用户消息"))
			session.AddMessage(*types.NewAssistantMessage("最后一条助手回复"))
			if err := store.Save(ctx, session); err != nil {
				t.Fatalf("Save: %v", err)
			}
			before, err := manager.GetMetadata(ctx, session.ID)
			if err != nil {
				t.Fatalf("GetMetadata before: %v", err)
			}

			loaded, err := manager.Get(ctx, session.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			// 模拟「元数据缺预览」的历史遗留行。
			loaded.Metadata.Title = ""
			loaded.Metadata.TitleSource = ""
			loaded.Metadata.Summary = ""
			if err := manager.PersistPreviewMetadata(ctx, loaded); err != nil {
				t.Fatalf("PersistPreviewMetadata: %v", err)
			}

			after, err := manager.GetMetadata(ctx, session.ID)
			if err != nil {
				t.Fatalf("GetMetadata after: %v", err)
			}
			if !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("updated_at changed by lazy repair: %s → %s", before.UpdatedAt, after.UpdatedAt)
			}
			if after.Metadata.Title == "" || after.Metadata.Summary == "" {
				t.Fatalf("repair did not persist preview: %#v", after.Metadata)
			}
			if after.PreviewNeedsHistory() {
				t.Fatalf("repaired row still needs history: title=%q source=%q summary=%q",
					after.Metadata.Title, after.Metadata.TitleSource, after.Metadata.Summary)
			}

			// 幂等：重复修复不得改变任何已持久化的值。
			if err := manager.PersistPreviewMetadata(ctx, loaded); err != nil {
				t.Fatalf("second PersistPreviewMetadata: %v", err)
			}
			again, err := manager.GetMetadata(ctx, session.ID)
			if err != nil {
				t.Fatalf("GetMetadata again: %v", err)
			}
			if again.Metadata.Title != after.Metadata.Title ||
				again.Metadata.TitleSource != after.Metadata.TitleSource ||
				again.Metadata.Summary != after.Metadata.Summary ||
				!again.UpdatedAt.Equal(after.UpdatedAt) {
				t.Fatalf("repair is not idempotent:\n first=%#v\nsecond=%#v", after.Metadata, again.Metadata)
			}
		})
	}
}
