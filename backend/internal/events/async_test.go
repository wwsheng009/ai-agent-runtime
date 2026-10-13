package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscribeAsyncDeliversInOrder(t *testing.T) {
	bus := NewBus()
	var mu sync.Mutex
	var got []string
	sub := bus.SubscribeAsync("turn", func(e Event) {
		mu.Lock()
		got = append(got, e.SessionID)
		mu.Unlock()
	}, AsyncOptions{Name: "ordered", QueueSize: 8})
	defer sub.Unsubscribe()

	for i := 0; i < 5; i++ {
		bus.Publish(Event{Type: "turn", SessionID: fmt.Sprintf("s%d", i)})
	}
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// 等待最后一条 handler 完成（WaitDrained 只保证队列空）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sub.Stats().Delivered < 5 {
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 5 {
		t.Fatalf("delivered %d events, want 5", len(got))
	}
	for i, id := range got {
		if id != fmt.Sprintf("s%d", i) {
			t.Fatalf("order = %v (index %d)", got, i)
		}
	}
	if stats := sub.Stats(); stats.Dropped != 0 || stats.Panicked != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestSubscribeAsyncFiltersByType(t *testing.T) {
	bus := NewBus()
	var count atomic.Int64
	sub := bus.SubscribeAsync("wanted", func(Event) { count.Add(1) }, AsyncOptions{})
	defer sub.Unsubscribe()

	bus.Publish(Event{Type: "other"})
	bus.Publish(Event{Type: "wanted"})
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Fatalf("delivered %d, want 1", got)
	}
}

// 慢消费者不得阻塞发布者：这是异步订阅存在的根本理由。
func TestSubscribeAsyncSlowHandlerDoesNotBlockPublish(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	var firstStarted sync.Once
	started := make(chan struct{})
	sub := bus.SubscribeAsync("", func(Event) {
		firstStarted.Do(func() { close(started) })
		<-release
	}, AsyncOptions{Name: "slow", QueueSize: 4, DropPolicy: DropOldest})
	defer sub.Unsubscribe()

	begin := time.Now()
	for i := 0; i < 50; i++ {
		bus.Publish(Event{Type: "t", SessionID: fmt.Sprintf("s%d", i)})
	}
	elapsed := time.Since(begin)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("publish took %v with a blocked consumer; must not block", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("consumer never started")
	}

	close(release)
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stats := sub.Stats(); stats.Delivered+stats.Dropped == 50 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	stats := sub.Stats()
	if stats.Delivered+stats.Dropped != 50 {
		t.Fatalf("delivered+dropped = %d, want 50 (%+v)", stats.Delivered+stats.Dropped, stats)
	}
	if stats.Dropped == 0 {
		t.Fatalf("expected drops with a blocked consumer and queue=4: %+v", stats)
	}
}

func TestSubscribeAsyncDropNewestKeepsOldest(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var seen []string
	sub := bus.SubscribeAsync("", func(e Event) {
		once.Do(func() { close(blocked) })
		<-release
		mu.Lock()
		seen = append(seen, e.SessionID)
		mu.Unlock()
	}, AsyncOptions{QueueSize: 2, DropPolicy: DropNewest})
	defer sub.Unsubscribe()

	bus.Publish(Event{Type: "t", SessionID: "e0"})
	<-blocked
	for _, id := range []string{"e1", "e2", "e3", "e4"} {
		bus.Publish(Event{Type: "t", SessionID: id})
	}
	close(release)
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sub.Stats().Delivered < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	stats := sub.Stats()
	if stats.Delivered != 3 || stats.Dropped != 2 {
		t.Fatalf("stats = %+v, want delivered=3 dropped=2", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range seen {
		if id == "e3" || id == "e4" {
			t.Fatalf("DropNewest should keep the oldest queued events, saw %v", seen)
		}
	}
}

func TestSubscribeAsyncDropBlockWaitsThenDrops(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	sub := bus.SubscribeAsync("", func(Event) {
		once.Do(func() { close(blocked) })
		<-release
	}, AsyncOptions{QueueSize: 1, DropPolicy: DropBlock, BlockTimeout: 40 * time.Millisecond})
	defer sub.Unsubscribe()

	bus.Publish(Event{Type: "t", SessionID: "e0"})
	<-blocked
	bus.Publish(Event{Type: "t", SessionID: "e1"}) // 入队
	begin := time.Now()
	bus.Publish(Event{Type: "t", SessionID: "e2"}) // 满：等待 40ms 后丢弃
	elapsed := time.Since(begin)
	if elapsed < 30*time.Millisecond {
		t.Fatalf("DropBlock returned after %v, want >=~40ms", elapsed)
	}
	if stats := sub.Stats(); stats.Dropped != 1 {
		t.Fatalf("stats = %+v, want dropped=1", sub.Stats())
	}
	close(release)
}

// 处理器 panic 必须被隔离：不得传播到发布者，也不影响其它订阅者。
func TestSubscribeAsyncPanicIsIsolated(t *testing.T) {
	bus := NewBus()
	var syncSeen atomic.Int64
	syncUnsub := bus.SubscribeCancelable("", func(Event) { syncSeen.Add(1) })
	defer syncUnsub()

	var asyncSeen atomic.Int64
	sub := bus.SubscribeAsync("", func(e Event) {
		if e.Type == "boom" {
			panic("handler exploded")
		}
		asyncSeen.Add(1)
	}, AsyncOptions{})
	defer sub.Unsubscribe()

	bus.Publish(Event{Type: "boom"}) // 不得 panic
	bus.Publish(Event{Type: "ok"})
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (sub.Stats().Panicked == 0 || asyncSeen.Load() == 0) {
		time.Sleep(2 * time.Millisecond)
	}
	stats := sub.Stats()
	if stats.Panicked != 1 {
		t.Fatalf("panicked = %d, want 1 (%+v)", stats.Panicked, stats)
	}
	if asyncSeen.Load() != 1 {
		t.Fatalf("handler should keep working after a panic, seen=%d", asyncSeen.Load())
	}
	if syncSeen.Load() != 2 {
		t.Fatalf("sync subscriber should receive both events, seen=%d", syncSeen.Load())
	}
}

func TestSubscribeAsyncUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewBus()
	var count atomic.Int64
	sub := bus.SubscribeAsync("", func(Event) { count.Add(1) }, AsyncOptions{})
	bus.Publish(Event{Type: "t"})
	bus.Publish(Event{Type: "t"})
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && count.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	sub.Unsubscribe()
	if !sub.Stats().Closed {
		t.Fatal("stats should report closed after Unsubscribe")
	}
	bus.Publish(Event{Type: "t"})
	time.Sleep(20 * time.Millisecond)
	if got := count.Load(); got != 2 {
		t.Fatalf("delivered %d after unsubscribe, want 2", got)
	}
}

// 同步订阅语义保持不变：Publish 返回时处理器已执行（连接器/通知中台用异步，
// 现有轻量投影继续用同步）。
func TestSubscribeSyncStillSynchronous(t *testing.T) {
	bus := NewBus()
	called := false
	unsub := bus.SubscribeCancelable("t", func(Event) { called = true })
	defer unsub()
	bus.Publish(Event{Type: "t"})
	if !called {
		t.Fatal("synchronous subscriber must run inside Publish")
	}
}

func TestSubscribeAsyncNilSafety(t *testing.T) {
	var bus *Bus
	if sub := bus.SubscribeAsync("", func(Event) {}, AsyncOptions{}); sub != nil {
		t.Fatal("nil bus should return nil subscription")
	}
	if sub := NewBus().SubscribeAsync("", nil, AsyncOptions{}); sub != nil {
		t.Fatal("nil handler should return nil subscription")
	}
	var sub *AsyncSubscription
	sub.Unsubscribe() // 不得 panic
	if err := sub.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := sub.Stats(); stats.Delivered != 0 {
		t.Fatalf("nil sub stats = %+v", stats)
	}
}
