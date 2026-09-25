package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Web 侧栏会话列表的性能基线：100 个会话 × 40 条 ~4KB 消息，对标真实库
// （top100 会话约 21MB prompt 投影）。
//
// 两个子基准用同一份数据比对两条路径：
//   - metadata-only：当前实现（listResumeCandidateChatSessionMetadata，零完整 Load）；
//   - full-load：旧实现（listResumeCandidateChatSessions，逐候选 Get 反序列化历史）。
//
// 运行：go test ./cmd/aicli/commands/ -run '^$' -bench BenchmarkChatWebSessions -benchtime 5x
func BenchmarkChatWebSessions(b *testing.B) {
	manager, session := seedWebSessionBenchStore(b)

	b.Run("metadata-only", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?sort=created_at&scope=all", nil)
			rec := httptest.NewRecorder()
			HandleChatWebAPISessions(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("status = %d", rec.Code)
			}
		}
	})

	b.Run("full-load", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			candidates, err := listResumeCandidateChatSessions(
				manager,
				session.SessionUserID,
				session.SessionFilter,
				currentRuntimeSessionID(session),
			)
			if err != nil {
				b.Fatalf("listResumeCandidateChatSessions: %v", err)
			}
			items := make([]chatWebSessionListItem, 0, len(candidates))
			for _, candidate := range candidates {
				items = append(items, buildChatWebSessionListItem(candidate, false))
			}
		}
	})

	// resume 的目标校验：先前的 manager.Get（整段历史反序列化）对比现在的
	// manager.GetMetadata（sessions 单行读取）。
	currentID := currentRuntimeSessionID(session)
	b.Run("resume-lookup-metadata-only", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := manager.GetMetadata(context.Background(), currentID); err != nil {
				b.Fatalf("GetMetadata: %v", err)
			}
		}
	})
	b.Run("resume-lookup-full-load", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := manager.Get(context.Background(), currentID); err != nil {
				b.Fatalf("Get: %v", err)
			}
		}
	})
}

// seedWebSessionBenchStore 建立 100 个会话、每个 40 条 ~4KB 消息的 SQLite 库，
// 并把最后一个会话设为当前会话返回。
func seedWebSessionBenchStore(b *testing.B) (*runtimechat.SessionManager, *ChatSession) {
	b.Helper()
	dir := b.TempDir()
	cfg := runtimechat.DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	store, err := runtimechat.NewSQLiteSessionStorage(cfg)
	if err != nil {
		b.Fatalf("NewSQLiteSessionStorage: %v", err)
	}
	manager := runtimechat.NewSessionManager(store, nil)
	b.Cleanup(manager.Stop)

	ctx := context.Background()
	payload := strings.Repeat("会话内容 ", 300)
	var current *runtimechat.Session
	for i := 0; i < 100; i++ {
		created, err := manager.Create(ctx, "bench-user")
		if err != nil {
			b.Fatalf("manager.Create: %v", err)
		}
		created.Metadata.Title = fmt.Sprintf("会话-%03d", i)
		for j := 0; j < 40; j++ {
			created.AddMessage(*runtimetypes.NewUserMessage(fmt.Sprintf("%s 第%d条", payload, j)))
		}
		if err := manager.Update(ctx, created); err != nil {
			b.Fatalf("manager.Update: %v", err)
		}
		current = created
	}

	session := newWebTestSession()
	session.SessionManager = manager
	session.SessionUserID = "bench-user"
	session.RuntimeSession = current
	session.Interaction = &chatInteractionCoordinator{}

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	b.Cleanup(func() { chatDebugDisplaySessionProvider = old })
	return manager, session
}
