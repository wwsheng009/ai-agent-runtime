package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/require"
)

// TestLoadPromptMessagesRawBytesKeepsEachMessageDistinct 锁定 sql.RawBytes 的
// 复用安全：驱动会在下一次 Next() 复用行缓冲，若实现把 payload 的引用留到
// 循环之后（或整体延迟解码），所有消息会变成同一份内容（典型表现为"全部
// 变成最后一条"）。
//
// 这是 `var payload []byte` → `sql.RawBytes` 改动必须守住的第一不变量。
func TestLoadPromptMessagesRawBytesKeepsEachMessageDistinct(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("rawbytes-user")
	session.History = nil
	for i := 0; i < 40; i++ {
		content := fmt.Sprintf("message-%02d-%s", i, strings.Repeat("x", 200+i))
		session.History = append(session.History, *runtimetypes.NewUserMessage(content))
	}
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, len(session.History))

	for i, got := range loaded.History {
		if got.Content != session.History[i].Content {
			t.Fatalf("message %d corrupted: got %q want %q",
				i, truncateForTest(got.Content), truncateForTest(session.History[i].Content))
		}
	}
}

// TestLoadPromptMessagesPreallocatesCapacity 验证预分配到位：
// 加载后的切片容量应不小于行数（否则 append 期间会反复扩容复制）。
func TestLoadPromptMessagesPreallocatesCapacity(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	const count = 64
	session := NewSession("prealloc-user")
	session.History = nil
	for i := 0; i < count; i++ {
		session.History = append(session.History, *runtimetypes.NewUserMessage(fmt.Sprintf("m-%03d", i)))
	}
	require.NoError(t, store.Save(ctx, session))

	history, err := store.loadPromptMessages(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, history, count)
	// 预分配后 len == cap；若退化为逐次 append，cap 通常会大于 len
	// （2 的幂增长）。这里只要求"装载后没有浪费性扩容"。
	require.LessOrEqual(t, cap(history), count*2,
		"history capacity %d suggests growth-loop reallocation (len=%d)", cap(history), len(history))
}

// TestHealLegacyTruncatedProjectionSkipsNonTail 锁定核心判定：
// 投影不是 canonical 尾部时不得自愈，且判定只需尾部比对（无需整段解码）。
func TestHealLegacyTruncatedProjectionSkipsNonTail(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("heal-non-tail")
	session.History = nil
	for i := 0; i < 200; i++ {
		session.History = append(session.History, *runtimetypes.NewUserMessage(fmt.Sprintf("m-%03d", i)))
	}
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.LoadMetadata(ctx, session.ID)
	require.NoError(t, err)
	projection, err := store.loadPromptMessages(ctx, session.ID)
	require.NoError(t, err)

	// 构造一个明显不是尾部的投影（改写中间一条内容）。
	modified := make([]runtimetypes.Message, len(projection))
	copy(modified, projection)
	modified[len(modified)/2] = *runtimetypes.NewUserMessage("REWRITTEN-MIDDLE")

	healed, err := store.healLegacyTruncatedProjection(ctx, loaded, modified)
	require.NoError(t, err)
	require.Nil(t, healed, "a rewritten middle is not a cap-trimmed tail")
}

// TestHealLegacyTruncatedProjectionStillHealsLargeGap 锁定前置判定不会误伤
// 真正的 legacy cap 残留：差值大且投影是 canonical 的尾部时必须自愈。
func TestHealLegacyTruncatedProjectionStillHealsTail(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	const total = 200
	const kept = 37 // 任意非 128 的长度：判定不该依赖具体 cap 值
	session := NewSession("heal-tail")
	session.History = nil
	for i := 0; i < total; i++ {
		session.History = append(session.History, *runtimetypes.NewUserMessage(fmt.Sprintf("m-%03d", i)))
	}
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.LoadMetadata(ctx, session.ID)
	require.NoError(t, err)
	// 模拟被 cap 截断的投影：只保留最新 kept 条（应当是 canonical 的精确尾部）。
	tail := session.History[total-kept:]
	loaded.CanonicalMessageCount = total

	healed, err := store.healLegacyTruncatedProjection(ctx, loaded, tail)
	require.NoError(t, err)
	require.NotNil(t, healed, "canonical tail must still be healed")
	require.Len(t, healed, total, "healed projection must be the full canonical transcript")
}

func truncateForTest(value string) string {
	if len(value) <= 60 {
		return value
	}
	return value[:60] + "..."
}
