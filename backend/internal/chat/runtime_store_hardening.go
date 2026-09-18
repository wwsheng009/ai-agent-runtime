package chat

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// runtime store 并发不变量（P0 加固）：
//
//  1. 连接池只有一条连接（SetMaxOpenConns(1)），任何一次池获取都必须能在有限
//     时间内完成；持有池连接期间，禁止在同一个 goroutine 上再次向连接池发起
//     获取——重入会自我死锁（连接永远等不到归还）。
//  2. 锁顺序固定为 mailboxWriteMu -> mu -> 池连接；禁止反向获取。
//  3. 长时间持有池连接的路径必须登记到 poolReentryGuard，便于重入检测与排查。
//
// poolReentryGuard 只在确实有 goroutine 持有专用连接时才解析 goroutine id，
// 正常路径（active == 0）只有一次原子读，开销可忽略。
type poolReentryGuard struct {
	active     atomic.Int64
	detections atomic.Int64

	mu     sync.Mutex
	scopes map[uint64]string
}

func (g *poolReentryGuard) enter(scope string) {
	if g == nil {
		return
	}
	gid := currentGoroutineID()
	g.mu.Lock()
	if g.scopes == nil {
		g.scopes = make(map[uint64]string)
	}
	g.scopes[gid] = scope
	g.mu.Unlock()
	g.active.Add(1)
}

func (g *poolReentryGuard) exit() {
	if g == nil {
		return
	}
	gid := currentGoroutineID()
	g.mu.Lock()
	delete(g.scopes, gid)
	g.mu.Unlock()
	g.active.Add(-1)
}

// check reports an error when the calling goroutine already holds a dedicated
// connection. Callers must not proceed to acquire from the shared pool then.
func (g *poolReentryGuard) check(op string) error {
	if g == nil || g.active.Load() == 0 {
		return nil
	}
	gid := currentGoroutineID()
	g.mu.Lock()
	scope, held := g.scopes[gid]
	g.mu.Unlock()
	if !held {
		return nil
	}
	g.detections.Add(1)
	return fmt.Errorf("runtime store pool re-entry refused: %s while goroutine already holds a dedicated connection (scope=%s)", op, scope)
}

func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	if n <= len(prefix) {
		return 0
	}
	var id uint64
	for _, c := range buf[len(prefix):n] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

func (s *SQLiteRuntimeStore) enterDedicatedConn(scope string) {
	if s == nil {
		return
	}
	s.reentry.enter(scope)
}

func (s *SQLiteRuntimeStore) exitDedicatedConn() {
	if s == nil {
		return
	}
	s.reentry.exit()
}

// checkPoolReentry refuses nested pool acquisition on a goroutine that already
// holds a dedicated connection. It is cheap on the normal path (one atomic
// load) and logs plus counts every detection.
func (s *SQLiteRuntimeStore) checkPoolReentry(op string) error {
	if s == nil {
		return nil
	}
	if err := s.reentry.check(op); err != nil {
		logpkg.Warnf("%v", err)
		return err
	}
	return nil
}

// ReentryDetections reports how many same-goroutine pool re-entries were refused.
func (s *SQLiteRuntimeStore) ReentryDetections() int64 {
	if s == nil {
		return 0
	}
	return s.reentry.detections.Load()
}

// operationContext bounds a store operation when the caller did not supply a
// deadline. This prevents a saturated (or wedged) single-connection pool from
// blocking callers forever.
func (s *SQLiteRuntimeStore) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.opTimeout <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.opTimeout)
}

// queryUsesSharedPool reports whether a mailbox query would acquire from the
// store's shared single-connection pool. *sql.Conn / *sql.Tx do not.
func queryUsesSharedPool(query runtimeMailboxQuery) bool {
	if query == nil {
		return true
	}
	_, isDB := query.(*sql.DB)
	return isDB
}

type notifyDropStats struct {
	events       atomic.Int64
	mailbox      atomic.Int64
	agentControl atomic.Int64
}

// NotifyDropStats reports in-process watcher notification drops (full channel)
// since store start. Silent drops are otherwise invisible to callers.
type NotifyDropStats struct {
	Events       int64
	Mailbox      int64
	AgentControl int64
}

func (s *SQLiteRuntimeStore) NotifyDropStats() NotifyDropStats {
	if s == nil {
		return NotifyDropStats{}
	}
	return NotifyDropStats{
		Events:       s.notifyDrops.events.Load(),
		Mailbox:      s.notifyDrops.mailbox.Load(),
		AgentControl: s.notifyDrops.agentControl.Load(),
	}
}
