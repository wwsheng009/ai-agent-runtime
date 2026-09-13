package usageanalytics

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// 回归背景（多进程共享分析库）：
//
// 一台机器上会同时跑多个 aicli 进程与 runtime-server，它们共享同一个
// usage_analytics.sqlite 并各自在启动时 migrate。migrate 需要写锁，落败的
// 连接会拿到 "database is locked"；此前 Open 直接把错误抛回调用方、调用方
// 静默吞掉，于是落败进程在整个生命周期内一条用量行都不写 —— 分析库看着
// "几乎为空"（缓存页表现为会话恢复后缓存历史为空），镜像缓存表却完整。
//
// 本文件覆盖：锁错误分类、退避序列、瞬时锁重试后成功（留痕）、并发打开
// 不丢写、写失败不再静默。
// ============================================================================

// warnCapture 捕获 degradeWarn 输出（包级函数变量，测试内替换）。
type warnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnCapture) record(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, fmt.Sprintf(format, args...))
}

func (w *warnCapture) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

func (w *warnCapture) contains(substr string) bool {
	for _, msg := range w.messages() {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// captureDegradeWarn 替换包级 degradeWarn 并返回捕获器（用例结束自动恢复）。
func captureDegradeWarn(t *testing.T) *warnCapture {
	t.Helper()
	captured := &warnCapture{}
	previous := degradeWarn
	degradeWarn = captured.record
	t.Cleanup(func() { degradeWarn = previous })
	return captured
}

// TestIsLockedError 只有瞬时锁冲突才应触发重试。
func TestIsLockedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain locked", err: errors.New("database is locked"), want: true},
		{name: "wrapped locked", err: fmt.Errorf("migrate usage analytics db: %w", errors.New("sqlite3: database is locked")), want: true},
		{name: "table locked", err: errors.New("database table is locked"), want: true},
		{name: "schema locked", err: errors.New("database schema is locked"), want: true},
		{name: "case insensitive", err: errors.New("SQLITE_BUSY: DATABASE IS LOCKED"), want: true},
		{name: "missing table", err: errors.New("no such table: usage_requests"), want: false},
		{name: "disk io", err: errors.New("disk I/O error"), want: false},
		{name: "permission", err: errors.New("attempt to write a readonly database"), want: false},
	}
	for _, tc := range cases {
		if got := isLockedError(tc.err); got != tc.want {
			t.Errorf("%s: isLockedError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestOpenLockRetryWaitBacksOffAndCaps 退避非递减、封顶，且总时长足以覆盖
// 多进程启动时的 migrate 竞争窗口（否则重试形同虚设）。
func TestOpenLockRetryWaitBacksOffAndCaps(t *testing.T) {
	previous := time.Duration(0)
	total := time.Duration(0)
	for retry := 0; retry < openLockRetries; retry++ {
		wait := openLockRetryWait(retry)
		if wait <= 0 {
			t.Fatalf("retry %d: wait = %v, want > 0", retry, wait)
		}
		if wait > openLockRetryMaxWait {
			t.Fatalf("retry %d: wait = %v, want <= %v", retry, wait, openLockRetryMaxWait)
		}
		if retry > 0 && wait < previous {
			t.Fatalf("retry %d: wait = %v 小于上一次 %v（退避必须非递减）", retry, wait, previous)
		}
		previous = wait
		total += wait
	}
	if openLockRetryWait(-1) != openLockRetryBaseWait || openLockRetryWait(0) != openLockRetryBaseWait {
		t.Fatalf("首次退避应为 %v，实际 %v/%v", openLockRetryBaseWait, openLockRetryWait(-1), openLockRetryWait(0))
	}
	if total < 2*time.Second {
		t.Fatalf("总退避 %v 过短，无法覆盖多进程启动竞争窗口", total)
	}
}

// TestOpenRetriesTransientLockAndWarns 另一个进程持有写锁时，Open 必须退避
// 重试到锁释放，并在重试成功后留痕（此前是静默返回错误 → 进程永不采集）。
func TestOpenRetriesTransientLockAndWarns(t *testing.T) {
	warnings := captureDegradeWarn(t)
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")

	// 用独立连接持有写事务，模拟另一个进程正在 migrate。
	guard, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("guard open: %v", err)
	}
	defer func() { _ = guard.Close() }()
	guard.SetMaxOpenConns(1)
	if _, err := guard.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatalf("guard pragma: %v", err)
	}
	tx, err := guard.Begin()
	if err != nil {
		t.Fatalf("guard begin: %v", err)
	}
	if _, err := tx.Exec("CREATE TABLE lock_holder (id INTEGER)"); err != nil {
		t.Fatalf("guard 取得写锁失败（用例前提不成立）: %v", err)
	}

	const (
		holdFor  = 250 * time.Millisecond
		busyWait = 50 * time.Millisecond
	)
	start := time.Now()
	type openResult struct {
		store *Store
		err   error
	}
	done := make(chan openResult, 1)
	go func() {
		store, err := Open(Config{Path: path, BusyTimeout: busyWait})
		done <- openResult{store: store, err: err}
	}()

	time.Sleep(holdFor)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("guard rollback: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Open 应在锁释放后成功，实际失败：%v（warnings: %v）", result.err, warnings.messages())
		}
		defer func() { _ = result.store.Close() }()
		if elapsed := time.Since(start); elapsed < holdFor {
			t.Fatalf("Open 仅耗时 %v（< 持锁 %v），说明写锁未生效、用例失去意义", elapsed, holdFor)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("Open 超时：退避重试未能在锁释放后成功（warnings: %v）", warnings.messages())
	}
	if !warnings.contains("锁重试后打开成功") {
		t.Fatalf("Open 重试成功应留下诊断，warnings = %v", warnings.messages())
	}
}

// TestOpenConcurrentSamePathKeepsAllWriters 回归用例：多个进程（此处为多个
// 连接）同时首次打开同一分析库并各自写一行，任何一方都不应因锁冲突败下阵来
// （此前会静默丢行、丢采集）。
func TestOpenConcurrentSamePathKeepsAllWriters(t *testing.T) {
	captureDegradeWarn(t)
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")

	const writers = 6
	errs := make([]error, writers)
	stores := make([]*Store, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store, err := Open(Config{Path: path})
			if err == nil {
				err = store.execWithLockRetry(
					`INSERT INTO usage_sessions (session_id, status, updated_at_unix_nano) VALUES (?,?,?)`,
					fmt.Sprintf("s-%d", i), SessionStatusCompleted, time.Now().UnixNano(),
				)
			}
			stores[i], errs[i] = store, err
		}(i)
	}
	close(start)
	wg.Wait()

	defer func() {
		for _, store := range stores {
			if store != nil {
				_ = store.Close()
			}
		}
	}()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d 失败（并发打开/写入不应丢采集）: %v", i, err)
		}
	}

	reader, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("reader open: %v", err)
	}
	defer func() { _ = reader.Close() }()
	rows, ok, err := reader.query(`SELECT COUNT(*) FROM usage_sessions`)
	if err != nil || !ok {
		t.Fatalf("count query: ok=%v err=%v", ok, err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			t.Fatalf("scan count: %v", err)
		}
	}
	if count != writers {
		t.Fatalf("写入 %d 行，实际落库 %d 行（并发下丢行）", writers, count)
	}
}

// TestWriteFailureIsReportedNotSilent 写失败必须留痕：此前 exec 的错误被
// 调用方丢弃，行丢了却没有任何线索。
func TestWriteFailureIsReportedNotSilent(t *testing.T) {
	warnings := captureDegradeWarn(t)
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	// 制造确定性的写失败：表不存在 → INSERT 报 no such table（非锁冲突，不重试）。
	if _, err := store.exec(`DROP TABLE usage_sessions`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	collector := newCollector(store, nil, nil)
	collector.upsertSession("s-1", SessionMeta{Status: SessionStatusCompleted}, time.Time{}, time.Time{})

	if !warnings.contains("usage_sessions") || !warnings.contains("本行已丢失") {
		t.Fatalf("写失败应留下诊断，warnings = %v", warnings.messages())
	}
	// 第二次失败不应再提示（累计 1 次已提示，下一次是第 100 次）。
	before := len(warnings.messages())
	collector.upsertSession("s-2", SessionMeta{}, time.Time{}, time.Time{})
	if after := len(warnings.messages()); after != before {
		t.Fatalf("写失败应按计数节流，实际提示数 %d → %d", before, after)
	}
}

// TestReportWriteFailureThrottling 首次失败提示、之后每 100 次提示一次。
func TestReportWriteFailureThrottling(t *testing.T) {
	warnings := captureDegradeWarn(t)
	collector := &collector{}
	for i := 1; i <= 200; i++ {
		collector.reportWriteFailure("usage_requests", errors.New("database is locked"))
	}
	messages := warnings.messages()
	if len(messages) != 3 {
		t.Fatalf("提示次数 = %d，want 3（第 1、100、200 次）: %v", len(messages), messages)
	}
	if !strings.Contains(messages[0], "累计 1 次") || !strings.Contains(messages[2], "累计 200 次") {
		t.Fatalf("提示内容缺失累计计数: %v", messages)
	}
	if collector.writeFailureCount.Load() != 200 {
		t.Fatalf("失败计数 = %d, want 200", collector.writeFailureCount.Load())
	}
	// nil 错误不计数、不提示。
	collector.reportWriteFailure("usage_requests", nil)
	if collector.writeFailureCount.Load() != 200 {
		t.Fatalf("nil 错误不应计数: %d", collector.writeFailureCount.Load())
	}
}

// TestUpsertRequestWriteFailureIsReported 请求终态落库失败同样要留痕
// （缓存页优先读分析库，静默丢行正是"缓存历史为空"的根因）。
func TestUpsertRequestWriteFailureIsReported(t *testing.T) {
	warnings := captureDegradeWarn(t)
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.exec(`DROP TABLE usage_requests`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	collector.upsertRequest(cacheanalytics.CacheRequestRecord{
		LLMRequestID: "req-1",
		SessionID:    "s-1",
		Status:       cacheanalytics.RequestStatusSuccess,
		FinishedAt:   &finished,
	})
	if !warnings.contains("usage_requests") {
		t.Fatalf("请求行写入失败应留下诊断，warnings = %v", warnings.messages())
	}
}

// TestListSessionsReleasesConnection 回归：查询必须归还唯一连接。
//
// 单连接池（SetMaxOpenConns(1)）下，任何未关闭的 Rows 都会让后续查询/写入
// 永久阻塞；此前 ListSessions 读完 COUNT 行后没有关闭结果集就发起分页查询，
// 4 连接池时只是更晚暴露（并发查询会耗尽连接池）。
func TestListSessionsReleasesConnection(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	for i := 0; i < 3; i++ {
		collector.upsertRequest(cacheanalytics.CacheRequestRecord{
			LLMRequestID: fmt.Sprintf("req-%d", i),
			SessionID:    "s-1",
			Status:       cacheanalytics.RequestStatusSuccess,
			StartedAt:    finished.Add(-time.Second),
			FinishedAt:   &finished,
		})
	}
	// 会话 rollup 需要 usage_sessions 行（ListSessions 以会话为单位聚合）。
	collector.upsertSession("s-1", SessionMeta{Provider: "acme", Model: "model-a"}, finished.Add(-time.Second), finished)

	done := make(chan error, 1)
	go func() {
		list, err := store.ListSessions(Query{})
		if err != nil {
			done <- err
			return
		}
		if list.Total != 1 || len(list.Sessions) != 1 {
			done <- fmt.Errorf("sessions = %d/%d, want 1/1", list.Total, len(list.Sessions))
			return
		}
		if list.Sessions[0].TotalRequests != 3 {
			done <- fmt.Errorf("requests = %d, want 3", list.Sessions[0].TotalRequests)
			return
		}
		// 查询后仍能写入：连接已归还（否则这里会一直等连接）。
		done <- store.execWithLockRetry(
			`INSERT INTO usage_sessions (session_id, status, updated_at_unix_nano) VALUES (?,?,?)`,
			"s-conn-check", SessionStatusCompleted, time.Now().UnixNano())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListSessions 后写入失败: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ListSessions 占住唯一连接：后续写入被永久阻塞")
	}
}

// TestCacheSourceRequestsReleasesConnection 回归：缓存分页查询（COUNT +
// 分页）同样不得占住唯一连接，否则 /web/api/cache/* 的请求列表页在
// 单连接池下会永久挂起。
func TestCacheSourceRequestsReleasesConnection(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	for i := 0; i < 3; i++ {
		collector.upsertRequest(cacheanalytics.CacheRequestRecord{
			LLMRequestID: fmt.Sprintf("cache-req-%d", i),
			SessionID:    "s-1",
			Status:       cacheanalytics.RequestStatusSuccess,
			StartedAt:    finished.Add(-time.Second),
			FinishedAt:   &finished,
		})
	}
	source := NewCacheSource(store, nil, false)

	done := make(chan error, 1)
	go func() {
		page, err := source.Requests("s-1", cacheanalytics.RequestQuery{Limit: 10, Offset: 0})
		if err != nil {
			done <- err
			return
		}
		if page.Total != 3 {
			done <- fmt.Errorf("total = %d, want 3", page.Total)
			return
		}
		// 查询后仍能写入：连接已归还（否则这里会一直等连接）。
		done <- store.execWithLockRetry(
			`INSERT INTO usage_sessions (session_id, status, updated_at_unix_nano) VALUES (?,?,?)`,
			"s-1", SessionStatusCompleted, time.Now().UnixNano())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Requests 后写入失败: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("CacheSource.Requests 占住唯一连接：后续写入被永久阻塞")
	}
}
