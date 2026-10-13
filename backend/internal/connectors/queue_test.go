package connectors

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestChatQueueEnqueueAfterCloseReturnsErrQueueClosed 覆盖 Fix-3：closeAll 后
// 入队必须被确定性拒绝（不再创建 worker）。
func TestChatQueueEnqueueAfterCloseReturnsErrQueueClosed(t *testing.T) {
	q := newChatQueue(1, time.Minute)
	q.closeAll()
	if err := q.enqueue("chat-1", func() {}); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("enqueue after closeAll = %v, want ErrQueueClosed", err)
	}
}

// TestChatQueueReopenAllowsEnqueueAgain 覆盖 Fix-3：Service.Start 重启服务时
// reopen 复位关闭标记后，队列可再次使用。
func TestChatQueueReopenAllowsEnqueueAgain(t *testing.T) {
	q := newChatQueue(1, time.Minute)
	q.closeAll()
	q.reopen()

	done := make(chan struct{})
	if err := q.enqueue("chat-1", func() { close(done) }); err != nil {
		t.Fatalf("enqueue after reopen = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not run after reopen")
	}
}

// TestChatQueueEnqueueCloseRace 覆盖 Fix-3：enqueue × closeAll 并发不得出现
// send-on-closed-channel panic（需配合 -race 运行）。
func TestChatQueueEnqueueCloseRace(t *testing.T) {
	q := newChatQueue(8, time.Minute)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				err := q.enqueue("chat-1", func() {})
				if err != nil && !errors.Is(err, ErrQueueFull) && !errors.Is(err, ErrQueueClosed) {
					t.Errorf("unexpected enqueue error: %v", err)
					return
				}
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	q.closeAll()
	close(stop)
	wg.Wait()

	// 关闭后仍应稳定返回 ErrQueueClosed。
	if err := q.enqueue("chat-1", func() {}); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("enqueue after closeAll = %v, want ErrQueueClosed", err)
	}
}

// TestChatQueueDrainsPendingTasksAfterClose 覆盖 Fix-3：closeAll 关闭队列后，
// 已入队任务仍应执行完再退出（不丢已接收的消息）。
func TestChatQueueDrainsPendingTasksAfterClose(t *testing.T) {
	q := newChatQueue(4, time.Minute)
	block := make(chan struct{})
	ran := make(chan struct{}, 2)

	if err := q.enqueue("chat-1", func() {
		<-block
		ran <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.enqueue("chat-1", func() { ran <- struct{}{} }); err != nil {
		t.Fatal(err)
	}

	q.closeAll()
	close(block)
	for i := 0; i < 2; i++ {
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			t.Fatalf("closeAll 后第 %d 个已入队任务被丢弃", i+1)
		}
	}
}
