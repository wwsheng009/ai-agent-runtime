package connectors

import (
	"errors"
	"sync"
	"time"
)

// ErrQueueFull 表示该 IM 会话的处理队列已满（调用方应记录并让平台重试）。
var ErrQueueFull = errors.New("connectors: chat queue is full")

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

// enqueue 把任务排入指定会话；队列满返回 ErrQueueFull。
func (q *chatQueue) enqueue(key string, task func()) error {
	q.mu.Lock()
	worker, ok := q.workers[key]
	if !ok || worker.closed {
		worker = &chatWorker{ch: make(chan func(), q.size)}
		q.workers[key] = worker
		go q.run(key, worker)
	}
	worker.lastAt = q.now()
	q.mu.Unlock()

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
func (q *chatQueue) closeAll() {
	q.mu.Lock()
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
