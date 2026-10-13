package connectors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// hasLogEntry 报告日志集合中是否存在指定级别/方向且 summary 含子串的条目。
func hasLogEntry(entries []LogEntry, level, direction, contains string) bool {
	for _, entry := range entries {
		if entry.Level == level && entry.Direction == direction && strings.Contains(entry.Summary, contains) {
			return true
		}
	}
	return false
}

// TestConnectorLogRingCapacityAndOrder 覆盖日志环容量上限与倒序（最新在前）。
func TestConnectorLogRingCapacityAndOrder(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, &fakeBackend{}, nil, func(deps *Deps) {
		deps.LogBufferSize = 3
	})
	for i := 1; i <= 5; i++ {
		service.recordLog(LogEntry{
			ConnectorID: "c1",
			Level:       LogLevelInfo,
			Direction:   LogDirectionIn,
			Summary:     fmt.Sprintf("event-%d", i),
		}, logCounters{})
	}

	entries, ok := service.Logs("c1", 10)
	if !ok {
		t.Fatal("已配置连接器应返回 ok=true")
	}
	if len(entries) != 3 {
		t.Fatalf("ring capacity 3 should keep 3 entries, got %d (%+v)", len(entries), entries)
	}
	for i, want := range []string{"event-5", "event-4", "event-3"} {
		if entries[i].Summary != want {
			t.Fatalf("entries[%d] = %q, want %q（倒序最新在前）", i, entries[i].Summary, want)
		}
	}
	if entries[0].Time.IsZero() || entries[0].ConnectorID != "c1" {
		t.Fatalf("时间/连接器 id 应被补全：%+v", entries[0])
	}
}

// TestLogSummaryTruncation 覆盖 Summary 截断到 ~200 字符（rune，不截断多字节字符）。
func TestLogSummaryTruncation(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, nil)
	service.recordLog(LogEntry{
		ConnectorID: "c1", Level: LogLevelInfo, Direction: LogDirectionSystem,
		Summary: strings.Repeat("密", 260),
	}, logCounters{})
	service.recordLog(LogEntry{
		ConnectorID: "c1", Level: LogLevelInfo, Direction: LogDirectionSystem,
		Summary: "  " + strings.Repeat("a", 500) + "  ",
	}, logCounters{})

	entries, _ := service.Logs("c1", 10)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// 最新在前：ASCII 条目。
	if got := utf8.RuneCountInString(entries[0].Summary); got != 200 {
		t.Fatalf("ASCII summary length = %d, want 200", got)
	}
	if got := utf8.RuneCountInString(entries[1].Summary); got != 200 {
		t.Fatalf("多字节 summary 应按 rune 截断到 200，got %d", got)
	}
}

// TestServiceLogsLimitConvergence 覆盖 limit 收敛：<=0→50、>200→200、其余按值。
func TestServiceLogsLimitConvergence(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, nil, func(deps *Deps) {
		deps.LogBufferSize = 260
	})
	for i := 0; i < 260; i++ {
		service.recordLog(LogEntry{
			ConnectorID: "c1", Level: LogLevelDebug, Direction: LogDirectionIn,
			Summary: fmt.Sprintf("e-%d", i),
		}, logCounters{})
	}

	cases := []struct {
		limit int
		want  int
	}{
		{limit: 0, want: 50},
		{limit: -7, want: 50},
		{limit: 60, want: 60},
		{limit: 1000, want: 200},
	}
	for _, tc := range cases {
		entries, ok := service.Logs("c1", tc.limit)
		if !ok {
			t.Fatalf("limit=%d: 应返回 ok=true", tc.limit)
		}
		if len(entries) != tc.want {
			t.Fatalf("limit=%d: entries = %d, want %d", tc.limit, len(entries), tc.want)
		}
	}
}

// TestServiceLogsUnknownConnector 覆盖未知连接器的 (nil,false) 语义。
func TestServiceLogsUnknownConnector(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, nil)
	if entries, ok := service.Logs("ghost", 10); ok || entries != nil {
		t.Fatalf("未知连接器 logs = (%v, %v), want (nil,false)", entries, ok)
	}
	if entries, ok := service.Logs("", 10); ok || entries != nil {
		t.Fatalf("空 id logs = (%v, %v), want (nil,false)", entries, ok)
	}
	if stats, ok := service.Stats("ghost"); ok || stats != (ConnectorStats{}) {
		t.Fatalf("未知连接器 stats = (%+v, %v), want (零值,false)", stats, ok)
	}
	// 已知连接器但尚无日志：ok=true + 空切片。
	entries, ok := service.Logs("c1", 10)
	if !ok || entries == nil || len(entries) != 0 {
		t.Fatalf("已知连接器无日志 = (%v, %v), want (非 nil 空切片,true)", entries, ok)
	}
}

// TestServiceStatsCountersAndLastError 覆盖计数递增与 LastError/LastActivityAt 更新规则。
func TestServiceStatsCountersAndLastError(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, nil)
	now := time.Now().UTC()
	service.recordLog(LogEntry{ConnectorID: "c1", Time: now, Level: LogLevelDebug, Direction: LogDirectionIn, Summary: "enqueue"}, logCounters{inbound: true})
	service.recordLog(LogEntry{ConnectorID: "c1", Time: now.Add(time.Second), Level: LogLevelInfo, Direction: LogDirectionOut, Summary: "reply"}, logCounters{outbound: true})
	service.recordLog(LogEntry{ConnectorID: "c1", Time: now.Add(2 * time.Second), Level: LogLevelInfo, Direction: LogDirectionIn, Summary: "dup"}, logCounters{dedupSkipped: true})
	// warn 级不更新 LastError/Errors（只有 error 级更新）。
	service.recordLog(LogEntry{ConnectorID: "c1", Time: now.Add(3 * time.Second), Level: LogLevelWarn, Direction: LogDirectionSystem, Summary: "warn only"}, logCounters{})

	stats, ok := service.Stats("c1")
	if !ok {
		t.Fatal("已配置连接器应返回 ok=true")
	}
	if stats.Inbound != 1 || stats.Outbound != 1 || stats.DedupSkipped != 1 || stats.QueueFull != 0 {
		t.Fatalf("counters = %+v", stats)
	}
	if stats.Errors != 0 || stats.LastError != "" || !stats.LastErrorAt.IsZero() {
		t.Fatalf("warn 级不得更新最近错误：%+v", stats)
	}
	if !stats.LastActivityAt.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("last_activity_at = %v, want %v（最近一次入/出站方向日志）", stats.LastActivityAt, now.Add(2*time.Second))
	}

	// error 级：errors++、LastError/LastErrorAt 更新、queue_full 计数。
	errorAt := now.Add(4 * time.Second)
	service.recordLog(LogEntry{ConnectorID: "c1", Time: errorAt, Level: LogLevelError, Direction: LogDirectionIn, Summary: "queue full"}, logCounters{queueFull: true})
	stats, _ = service.Stats("c1")
	if stats.Errors != 1 || stats.QueueFull != 1 || stats.LastError != "queue full" || !stats.LastErrorAt.Equal(errorAt) {
		t.Fatalf("error 级统计 = %+v", stats)
	}
}

// TestLogRingConcurrentAccess 覆盖日志环的并发读写（-race 下必须无数据竞争）。
func TestLogRingConcurrentAccess(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, nil, func(deps *Deps) {
		deps.LogBufferSize = 64
	})

	const writers, perWriter, readers = 8, 50, 4
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				service.recordLog(LogEntry{
					ConnectorID: "c1", Level: LogLevelDebug, Direction: LogDirectionIn,
					Summary: fmt.Sprintf("w%d-%d", w, i),
				}, logCounters{inbound: true})
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				entries, ok := service.Logs("c1", 20)
				if !ok {
					t.Errorf("Logs ok=false during concurrent access")
					return
				}
				if len(entries) > 20 {
					t.Errorf("entries = %d, want <=20", len(entries))
					return
				}
				if _, ok := service.Stats("c1"); !ok {
					t.Errorf("Stats ok=false during concurrent access")
					return
				}
			}
		}()
	}
	wg.Wait()

	entries, _ := service.Logs("c1", 1000)
	if len(entries) != 64 {
		t.Fatalf("ring should be full (64), got %d", len(entries))
	}
	stats, _ := service.Stats("c1")
	if stats.Inbound != writers*perWriter {
		t.Fatalf("stats.inbound = %d, want %d", stats.Inbound, writers*perWriter)
	}
}

// TestHandleInboundLogsAndCounters 覆盖 HandleInbound 写入点：白名单拒绝、入队成功、
// 开始处理、处理成功（回复长度）、重复投递去重跳过，以及对应计数。
func TestHandleInboundLogsAndCounters(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "答复"}}}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.AllowedUsers = []string{"u1"}
	}))
	service := newTestService(t, store, backend, provider)

	intruder := inbound("c1", "chat-1", "m0", "hi")
	intruder.UserID = "u2"
	if err := service.HandleInbound(context.Background(), intruder); err != nil {
		t.Fatal(err)
	}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply delivered", func() bool {
		stats, _ := service.Stats("c1")
		return stats.Outbound == 1
	})
	if final := provider.finalMessages(); len(final) != 1 || final[0].Text != "答复" {
		t.Fatalf("stream final = %+v", final)
	}
	// 重复投递（同 chat + 同 messageID）：账本跳过并计数。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dedupe skipped", func() bool {
		stats, _ := service.Stats("c1")
		return stats.DedupSkipped == 1
	})

	stats, ok := service.Stats("c1")
	if !ok {
		t.Fatal("已配置连接器应返回 ok=true")
	}
	if stats.Inbound != 2 || stats.Outbound != 1 || stats.Errors != 0 || stats.DedupSkipped != 1 || stats.QueueFull != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.LastActivityAt.IsZero() {
		t.Fatalf("入/出站后 last_activity_at 应更新：%+v", stats)
	}

	entries, _ := service.Logs("c1", 50)
	if !hasLogEntry(entries, LogLevelWarn, LogDirectionIn, "白名单") {
		t.Fatalf("缺少白名单拒绝日志：%+v", entries)
	}
	if !hasLogEntry(entries, LogLevelDebug, LogDirectionIn, "入站消息已入队") {
		t.Fatalf("缺少入队成功日志：%+v", entries)
	}
	if !hasLogEntry(entries, LogLevelDebug, LogDirectionIn, "开始处理") {
		t.Fatalf("缺少开始处理日志：%+v", entries)
	}
	if !hasLogEntry(entries, LogLevelInfo, LogDirectionIn, "重复投递已跳过") {
		t.Fatalf("缺少去重跳过日志：%+v", entries)
	}
	if !hasLogEntry(entries, LogLevelInfo, LogDirectionOut, "处理完成（回复 2 字符）") {
		t.Fatalf("缺少处理成功日志（含回复长度）：%+v", entries)
	}
}

// TestQueueFullLoggedAndCounted 覆盖队列满写入点：error 级日志 + queue_full/errors 计数。
func TestQueueFullLoggedAndCounted(t *testing.T) {
	provider := &fakeProvider{}
	release := make(chan struct{})
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, block: release}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider, func(deps *Deps) {
		deps.QueueSize = 1
	})

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m0", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first handle", func() bool { return len(backend.requests()) == 1 })
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatalf("second enqueue should fit: %v", err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "hi")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrQueueFull", err)
	}

	stats, _ := service.Stats("c1")
	if stats.QueueFull != 1 || stats.Errors != 1 {
		t.Fatalf("queue full stats = %+v, want queue_full=1 errors=1", stats)
	}
	if stats.LastError == "" || !strings.Contains(stats.LastError, "队列已满") {
		t.Fatalf("last_error = %q, want 队列满摘要", stats.LastError)
	}
	entries, _ := service.Logs("c1", 50)
	if !hasLogEntry(entries, LogLevelError, LogDirectionIn, "队列已满") {
		t.Fatalf("缺少队列满日志：%+v", entries)
	}
	close(release)
}

// TestBackendErrorLoggedAndCounted 覆盖 processMessage 后端错误写入点。
func TestBackendErrorLoggedAndCounted(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{err: errors.New("agent offline")}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backend error logged", func() bool {
		stats, _ := service.Stats("c1")
		return stats.Errors == 1
	})
	stats, _ := service.Stats("c1")
	if stats.Outbound != 0 || !strings.Contains(stats.LastError, "agent offline") {
		t.Fatalf("stats = %+v", stats)
	}
	entries, _ := service.Logs("c1", 50)
	if !hasLogEntry(entries, LogLevelError, LogDirectionIn, "后端处理失败") {
		t.Fatalf("缺少后端错误日志：%+v", entries)
	}
}

// TestStreamerFailFailureLogged 覆盖 streamer.Fail 失败写入点（额外 error 计数）。
func TestStreamerFailFailureLogged(t *testing.T) {
	provider := &fakeProvider{streamFailErr: errors.New("edit failed")}
	backend := &fakeBackend{err: errors.New("agent offline")}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fail notice logged", func() bool {
		stats, _ := service.Stats("c1")
		return stats.Errors == 2
	})
	entries, _ := service.Logs("c1", 50)
	if !hasLogEntry(entries, LogLevelError, LogDirectionOut, "失败回执发送失败") {
		t.Fatalf("缺少失败回执发送失败日志：%+v", entries)
	}
}

// TestSendFailureLoggedAndCounted 覆盖 provider.Send 失败写入点（final 模式）。
func TestSendFailureLoggedAndCounted(t *testing.T) {
	provider := &fakeProvider{sendErr: errors.New("telegram 500")}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "hi"}}}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.ReplyMode = "final"
	}))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "send failure logged", func() bool {
		stats, _ := service.Stats("c1")
		return stats.Errors == 1
	})
	stats, _ := service.Stats("c1")
	if stats.Outbound != 0 {
		t.Fatalf("投递失败不得计 outbound：%+v", stats)
	}
	entries, _ := service.Logs("c1", 50)
	if !hasLogEntry(entries, LogLevelError, LogDirectionOut, "回复投递失败") {
		t.Fatalf("缺少投递失败日志：%+v", entries)
	}
}

// TestOwnershipGateSkipLogs 覆盖运行归属闸跳过写入点（未运行由 X 持有）。
func TestOwnershipGateSkipLogs(t *testing.T) {
	t.Run("policy owner mismatch", func(t *testing.T) {
		provider := &fakeProvider{polling: true}
		store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
			cfg.Policy.RuntimeOwner = "gateway"
		}))
		service := newTestService(t, store, &fakeBackend{}, provider, func(deps *Deps) {
			deps.HostID = "test-host"
		})
		if err := service.StartConnector(context.Background(), "c1"); err != nil {
			t.Fatal(err)
		}
		if serviceRunning(service, "c1") {
			t.Fatal("策略指定其它宿主的连接器不得启动")
		}
		entries, _ := service.Logs("c1", 20)
		if !hasLogEntry(entries, LogLevelWarn, LogDirectionSystem, "未运行（由 gateway 持有") {
			t.Fatalf("缺少归属闸跳过日志：%+v", entries)
		}
	})

	t.Run("held by another host", func(t *testing.T) {
		provider := &fakeProvider{polling: true}
		store := NewMemoryStore(testConfig("c1", "fake"))
		if _, _, err := store.ClaimConnectorOwner("c1", "other-host", time.Now().UTC(), time.Hour); err != nil {
			t.Fatal(err)
		}
		service := newTestService(t, store, &fakeBackend{}, provider, func(deps *Deps) {
			deps.HostID = "test-host"
		})
		if err := service.StartConnector(context.Background(), "c1"); err != nil {
			t.Fatal(err)
		}
		if serviceRunning(service, "c1") {
			t.Fatal("归属被他宿主持有的连接器不得启动")
		}
		entries, _ := service.Logs("c1", 20)
		if !hasLogEntry(entries, LogLevelWarn, LogDirectionSystem, "未运行（由 other-host 持有）") {
			t.Fatalf("缺少归属闸跳过日志：%+v", entries)
		}
	})
}

// TestStartStopLogs 覆盖启动/停机写入点（Service.Start/Stop 与 StopConnector）。
func TestStartStopLogs(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, &fakeProvider{polling: true})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := service.Logs("c1", 20)
	if !hasLogEntry(entries, LogLevelInfo, LogDirectionSystem, "已启动轮询") {
		t.Fatalf("缺少启动日志：%+v", entries)
	}
	service.Stop(context.Background())
	entries, _ = service.Logs("c1", 20)
	if !hasLogEntry(entries, LogLevelInfo, LogDirectionSystem, "服务停机") {
		t.Fatalf("缺少停机日志：%+v", entries)
	}

	service2 := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, &fakeProvider{polling: true})
	if err := service2.StartConnector(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	service2.StopConnector("c1")
	entries, _ = service2.Logs("c1", 20)
	if !hasLogEntry(entries, LogLevelInfo, LogDirectionSystem, "连接器已停止") {
		t.Fatalf("缺少单连接器停止日志：%+v", entries)
	}
}
