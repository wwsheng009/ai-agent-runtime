package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func BenchmarkSQLiteSessionStorageAppendBounded(b *testing.B) {
	dir := b.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	cfg.HotHistoryMessages = 32
	cfg.HotHistoryBytes = 256 * 1024
	store, err := NewSQLiteSessionStorage(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.CloseStorage() })
	ctx := context.Background()
	session := NewSession("benchmark-user")
	if err := store.Save(ctx, session); err != nil {
		b.Fatal(err)
	}
	for index := 0; index < cfg.HotHistoryMessages; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("warmup-%d", index)))
		if err := store.Update(ctx, session); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("message-%d", index)))
		if err := store.Update(ctx, session); err != nil {
			b.Fatal(err)
		}
	}
}

// benchmarkUpdateSessionProjectionRebuild 构造与生产同形的 default 分支负载：
// 热投影在内存里被改写（payload 不再与存储逐字节相等）+ 追加新 turn，因此每个
// checkpoint 都落入 updateSessionTx 的 default 分支。
// forceLegacy=true 时跳过 identity_hash 快速路径，复刻 P0-3 之前的全量解码成本。
func benchmarkUpdateSessionProjectionRebuild(b *testing.B, forceLegacy bool) {
	dir := b.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	cfg.HotHistoryMessages = 128
	cfg.HotHistoryBytes = 2 * 1024 * 1024
	store, err := NewSQLiteSessionStorage(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.CloseStorage() })
	ctx := context.Background()
	const sessionID = "bench-projection-rebuild"

	session := &Session{ID: sessionID, UserID: "bench", State: StateActive, CreatedAt: time.Now().UTC()}
	// 生产会话量级（profile 会话 6472 条 canonical）。全量解码成本随它线性增长，
	// 快速路径的点查成本与之无关 —— 这正是 P0-3 的收益来源。
	const seedMessages = 4096
	history := make([]types.Message, 0, seedMessages)
	for index := 0; index < seedMessages; index++ {
		content := fmt.Sprintf("seed-message-%03d", index)
		if index%2 == 0 {
			history = append(history, *types.NewUserMessage(content))
		} else {
			history = append(history, *types.NewAssistantMessage(content))
		}
	}
	session.ReplaceHistory(history)
	if err := store.Save(ctx, session); err != nil {
		b.Fatal(err)
	}
	store.forceLegacyProjectionRebuild = forceLegacy

	loaded, err := store.Load(ctx, sessionID)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		loaded.History[0].Metadata = types.NewMetadata().With("bench-divergence", strconv.Itoa(index))
		loaded.AddMessage(*types.NewUserMessage(fmt.Sprintf("bench-turn-%d", index)))
		if err := store.Update(ctx, loaded); err != nil {
			b.Fatal(err)
		}
	}
	// 归因断言：两个基准都必须确实命中目标分支，否则基准毫无意义。
	stats := store.CanonicalRebuildStats()
	if forceLegacy {
		if stats.FullCanonicalLoads < int64(b.N) || stats.FastIdentityProofs != 0 {
			b.Fatalf("legacy benchmark did not force full rebuild: %+v", stats)
		}
	} else if stats.FastIdentityProofs < int64(b.N) || stats.FullCanonicalLoads != 0 {
		b.Fatalf("fast benchmark did not take identity fast path: %+v", stats)
	}
}

func BenchmarkUpdateSessionProjectionRebuildLegacy(b *testing.B) {
	benchmarkUpdateSessionProjectionRebuild(b, true)
}

func BenchmarkUpdateSessionProjectionRebuildFastPath(b *testing.B) {
	benchmarkUpdateSessionProjectionRebuild(b, false)
}
