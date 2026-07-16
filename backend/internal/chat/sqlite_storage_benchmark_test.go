package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

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
