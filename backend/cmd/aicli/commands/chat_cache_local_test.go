package commands

import (
	"path/filepath"
	"testing"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestLocalCacheCollectorEagerAttachAndReplay 固化本次修复的两段契约：
//  1. 本地 runtime host 初始化末尾（initializeLocalChatRuntimeHost →
//     ensureLocalCacheService）即挂载 collector，不依赖任何
//     /web/api/cache/* 查询：启动后产生的 llm.request.* 事件全部入账；
//  2. 终态记录落库 cache_requests，重开 host（等价进程重启 / 重新打开
//     缓存页）时由 LiveSource 惰性回放，历史明细与 live 记录同源可见。
//
// 回归背景：此前 collector 仅在首次缓存查询时惰性构建，EventBus 订阅
// 不可回溯，导致"打开缓存页只有 live 记录、没有历史请求"。
func TestLocalCacheCollectorEagerAttachAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session_runtime.sqlite")
	store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteRuntimeStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "sess-eager-cache"

	// 进程内首个 host：启动即挂载，之后才产生 LLM 请求事件。
	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, RuntimeStore: store}
	svc := ensureLocalCacheService(host)
	if svc == nil {
		t.Fatal("启动期挂载失败：ensureLocalCacheService 返回 nil")
	}
	if !svc.Source().Capabilities().Persisted {
		t.Fatal("RuntimeStore 未挂上 RequestStore：Persisted=false")
	}

	publishCacheStarted(t, bus, sessionID, "req-1", nil)
	publishCacheFinished(t, bus, sessionID, "req-1", map[string]interface{}{
		"usage_cache_read_tokens": 100,
	})
	publishCacheStarted(t, bus, sessionID, "req-2", nil)
	publishCacheFinished(t, bus, sessionID, "req-2", nil)

	// 关键断言：未发起任何 HTTP 查询，记录也必须已捕获且已持久化。
	assertLocalCacheRequestCount(t, svc, sessionID, 2)
	assertPersistedRequestIDs(t, store, sessionID, "req-1", "req-2")

	// 新 host 复用同一 DB：等价重启进程/重新打开缓存页，历史靠回放补齐。
	bus2 := runtimeevents.NewBus()
	host2 := &localChatRuntimeHost{EventBus: bus2, RuntimeStore: store}
	svc2 := ensureLocalCacheService(host2)
	if svc2 == nil {
		t.Fatal("重开 host 时挂载失败")
	}
	assertLocalCacheRequestCount(t, svc2, sessionID, 2)
}

func assertLocalCacheRequestCount(t *testing.T, svc *cacheanalytics.Service, sessionID string, want int) {
	t.Helper()
	resp, err := svc.Source().Requests(sessionID, cacheanalytics.RequestQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Requests(%s): %v", sessionID, err)
	}
	if resp.Total != want || len(resp.Requests) != want {
		t.Fatalf("缓存请求数 = total %d / rows %d, want %d", resp.Total, len(resp.Requests), want)
	}
}

func assertPersistedRequestIDs(t *testing.T, store cacheanalytics.RequestStore, sessionID string, wantIDs ...string) {
	t.Helper()
	records, err := store.LoadSessionRequests(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionRequests(%s): %v", sessionID, err)
	}
	if len(records) != len(wantIDs) {
		t.Fatalf("持久化记录数 = %d, want %d", len(records), len(wantIDs))
	}
	got := make(map[string]bool, len(records))
	for i := range records {
		got[records[i].LLMRequestID] = true
	}
	for _, id := range wantIDs {
		if !got[id] {
			t.Fatalf("持久化记录缺少 %s（got=%v）", id, got)
		}
	}
}
