package connectors

import (
	"errors"
	"sync"
	"time"
)

// ErrQueueFull 表示该 IM 会话的处理队列已满（调用方应记录并让平台重试）。
var ErrQueueFull = errors.New("connectors: chat queue is full")

// ErrQueueClosed 表示队列已被 Stop/closeAll 关闭（服务生命周期已结束）。
var ErrQueueClosed = errors.New("connectors: chat queue is closed")

const (
	defaultQueueSize      = 32
	defaultWorkerIdleTime = 2 * time.Minute
)

// chatQueue 为每个 IM 会话维护一个串行、有界的任务队列：
// 同一会话严格有序，跨会话并行；队列满时直接拒绝（不阻塞轮询/发布路径）。
type chatQueue struct {
	mu      sync.Mutex
	size    int
	idle    time.Duration
	closed  bool
	workers map[string]*chatWorker
	now     func() time.Time
}

type chatWorker struct {
	ch     chan func()
	lastAt time.Time
	closed bool
}

func newChatQueue(size int, idle time.Duration) *chatQueue {
	if size <= 0 {
		size = defaultQueueSize
	}
	if idle <= 0 {
		idle = defaultWorkerIdleTime
	}
	return &chatQueue{
		size:    size,
		idle:    idle,
		workers: make(map[string]*chatWorker),
		now:     time.Now,
	}
}

// enqueue 把任务排入指定会话；队列满返回 ErrQueueFull，队列已关闭返回
// ErrQueueClosed。非阻塞发送在 q.mu 临界区内完成，与 closeAll/pruneIdle 的
// "标记 closed + 摘除 worker + 关闭 channel" 互斥，杜绝 send-on-closed-channel。
func (q *chatQueue) enqueue(key string, task func()) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	worker, ok := q.workers[key]
	if !ok || worker.closed {
		worker = &chatWorker{ch: make(chan func(), q.size)}
		q.workers[key] = worker
		go q.run(key, worker)
	}
	worker.lastAt = q.now()

	select {
	case worker.ch <- task:
		return nil
	default:
		return ErrQueueFull
	}
}

func (q *chatQueue) run(key string, worker *chatWorker) {
	for task := range worker.ch {
		task()
	}
	q.mu.Lock()
	if current, ok := q.workers[key]; ok && current == worker {
		delete(q.workers, key)
	}
	q.mu.Unlock()
}

// pruneIdle 回收空闲 worker（由宿主按需调用；P0 保持显式，接入 ticker 在后续阶段）。
func (q *chatQueue) pruneIdle() {
	q.mu.Lock()
	now := q.now()
	var closing []*chatWorker
	for key, worker := range q.workers {
		if now.Sub(worker.lastAt) >= q.idle {
			worker.closed = true
			delete(q.workers, key)
			closing = append(closing, worker)
		}
	}
	q.mu.Unlock()
	for _, worker := range closing {
		close(worker.ch)
	}
}

// closeAll 停止所有 worker（Stop 时调用）：关闭队列，让已入队任务执行完退出。
// 关闭后 enqueue 一律返回 ErrQueueClosed；Start 重启服务时用 reopen 复位。
func (q *chatQueue) closeAll() {
	q.mu.Lock()
	q.closed = true
	var closing []*chatWorker
	for key, worker := range q.workers {
		worker.closed = true
		delete(q.workers, key)
		closing = append(closing, worker)
	}
	q.mu.Unlock()
	for _, worker := range closing {
		close(worker.ch)
	}
}

// reopen 复位关闭标记（Service.Start 重新启动时调用）。
// 旧 worker 已在 closeAll 中从 map 摘除，其 run 收尾按指针相等判断，
// 不会误删重开后的新 worker。
func (q *chatQueue) reopen() {
	q.mu.Lock()
	q.closed = false
	q.mu.Unlock()
}
