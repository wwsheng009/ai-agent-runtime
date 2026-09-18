package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// newChatDebugStorageTestSession 造一个带真实 runtime store（可选批量缓冲）的会话，
// 复用生产装配形态：host.EventStore 是 *runtimechat.SQLiteRuntimeStore。
func newChatDebugStorageTestSession(t *testing.T, withBuffer bool) *ChatSession {
	t.Helper()
	store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "debug-storage.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	// 触发懒加载与 SQLite 能力探测：否则统计全零、sqlite_version/池统计不出现。
	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		SessionID: "debug-storage",
		Type:      "session.progress",
	})
	require.NoError(t, err)

	host := &localChatRuntimeHost{EventStore: store}
	if withBuffer {
		buffer := runtimechat.NewEventPersistBuffer(store, runtimechat.EventPersistBufferConfig{
			BatchSize:       8,
			FlushInterval:   20 * time.Millisecond,
			QueueLimit:      64,
			QueueBytesLimit: 1 << 20,
			ShutdownTimeout: 2 * time.Second,
			AsyncDispatch:   true,
		})
		t.Cleanup(func() { _ = buffer.Close(context.Background()) })
		host.runtimeEventBuffer = buffer
	}
	return &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}
}

// TestChatDebugDisplayShowsStorageSection 锁定"存储与持久化"区块的接线：
// /debug display（TUI）与 /web/api/status?format=text 共用同一文档。
func TestChatDebugDisplayShowsStorageSection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	session := newChatDebugStorageTestSession(t, true)
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	// 文档里 meta 标签按列宽补齐空格，断言前归一化空白。
	normalized := strings.Join(strings.Fields(output), " ")
	for _, expected := range []string{
		"存储与持久化:",
		"Runtime Store:",
		"Write Pool: open=1/1",
		"Read Pool: open=",
		"WAL:",
		"Append: path=",
		"Maintenance: runs=",
		"supports_returning=",
		"Contention: busy_retries=0 busy_snapshot_517=0 busy_exhausted=0",
		"批量落盘: enabled=true async=true",
	} {
		if !strings.Contains(normalized, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
}

// TestChatDebugDisplayStorageSectionHiddenWithoutStore 保证无 store 时保持既有
// 响应形状（不出现空区块）。
func TestChatDebugDisplayStorageSectionHiddenWithoutStore(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: &localChatRuntimeHost{}}
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})
	if strings.Contains(output, "存储与持久化:") {
		t.Fatalf("no event store/buffer but storage section rendered:\n%s", output)
	}
}

// TestChatDebugStorageSnapshotJSONContract 锁定 JSON 契约：字段名沿用
// internal/chat 的 tag（与 runtime-server health 的 persist/store_pools 同构），
// 并保证 chatDebugDisplaySnapshot 在无 store 时省略 storage 键。
func TestChatDebugStorageSnapshotJSONContract(t *testing.T) {
	session := newChatDebugStorageTestSession(t, true)

	info := chatDebugStorageSnapshot(session)
	require.NotNil(t, info)
	require.NotNil(t, info.Pool)
	require.NotNil(t, info.Append)
	require.NotNil(t, info.Contention)
	require.NotNil(t, info.Persist)

	raw, err := json.Marshal(info)
	require.NoError(t, err)
	for _, key := range []string{
		`"pool"`, `"write_wait_count"`, `"read_pool_open"`, `"wal_size_bytes"`,
		`"append"`, `"sqlite_version"`, `"busy_snapshot_errors"`,
		`"contention"`, `"persist"`, `"flush_p95_ns"`, `"async_dispatch"`,
	} {
		require.Contains(t, string(raw), key, "storage JSON must expose canonical key %s", key)
	}

	withStorage, err := json.Marshal(&chatDebugDisplaySnapshot{Storage: info})
	require.NoError(t, err)
	require.Contains(t, string(withStorage), `"storage"`)

	withoutStorage, err := json.Marshal(&chatDebugDisplaySnapshot{})
	require.NoError(t, err)
	require.NotContains(t, string(withoutStorage), `"storage"`, "无 store 时必须省略 storage 键")
}

// TestChatDebugStorageSnapshotNilWithoutProviders 保证 nil/空输入不产出空区块。
func TestChatDebugStorageSnapshotNilWithoutProviders(t *testing.T) {
	require.Nil(t, chatDebugStorageSnapshot(nil))
	require.Nil(t, chatDebugStorageSnapshotFrom(nil, nil, ""))
}
