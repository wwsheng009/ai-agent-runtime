package ui

import (
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// p1Record 是 reducer 收到的 (revision, action) 记录。
type p1Record struct {
	revision uint64
	action   UIAction
}

// p1Recorder 记录 reducer 应用序列与 effect 投递序列。
type p1Recorder struct {
	mu      sync.Mutex
	applied []p1Record
	effects []Effect
}

func (r *p1Recorder) apply(rev uint64, a UIAction) []Effect {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, p1Record{revision: rev, action: a})
	return nil
}

func (r *p1Recorder) effect(e Effect) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.effects = append(r.effects, e)
}

func (r *p1Recorder) snapshot() ([]p1Record, []Effect) {
	r.mu.Lock()
	defer r.mu.Unlock()
	applied := make([]p1Record, len(r.applied))
	copy(applied, r.applied)
	effects := make([]Effect, len(r.effects))
	copy(effects, r.effects)
	return applied, effects
}

func (r *p1Recorder) appliedKinds() []string {
	applied, _ := r.snapshot()
	kinds := make([]string, len(applied))
	for i, rec := range applied {
		kinds[i] = actionClassString(rec.action)
	}
	return kinds
}

// newP1Controller 创建带 recorder 的 controller；cap <= 0 用默认容量。
func newP1Controller(t *testing.T, cap int) (*UIController, *p1Recorder) {
	t.Helper()
	rec := &p1Recorder{}
	cfg := UIControllerConfig{}
	if cap > 0 {
		cfg.MailboxSize = cap
	}
	c := NewUIController(cfg, ReducerFunc(rec.apply), rec.effect)
	return c, rec
}

// ---------------------------------------------------------------------------
// 基础：FIFO、revision、stats
// ---------------------------------------------------------------------------

func TestUIController_FIFOOrderAndRevision(t *testing.T) {
	c, rec := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	c.Post(RuntimeEvent{Kind: "A"})
	c.Post(RuntimeEvent{Kind: "B"})
	c.Post(SetActiveBandAction{})
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 3 {
		t.Fatalf("applied = %d, want 3", len(applied))
	}
	for i, want := range []string{"RuntimeEvent(A)", "RuntimeEvent(B)", "Action"} {
		if got := actionClassString(applied[i].action); got != want {
			t.Errorf("applied[%d] = %s, want %s", i, got, want)
		}
		if applied[i].revision != uint64(i) {
			t.Errorf("applied[%d].revision = %d, want %d", i, applied[i].revision, i)
		}
	}
	if rev := c.Revision(); rev != 3 {
		t.Errorf("Revision() = %d, want 3", rev)
	}
	stats := c.Stats()
	if stats.Processed != 3 || stats.Posted != 3 {
		t.Errorf("stats = %+v, want Posted/Processed = 3", stats)
	}
	if stats.Dropped != 0 || stats.Pending != 0 {
		t.Errorf("stats = %+v, want no drops/pending", stats)
	}
}

func TestUIController_LateBoundEffectConsumerReceivesPublishedEffects(t *testing.T) {
	var mu sync.Mutex
	var got []Effect
	c := NewUIController(UIControllerConfig{}, ReducerFunc(func(_ uint64, action UIAction) []Effect {
		if _, ok := action.(DrawRequested); ok {
			return []Effect{FlushEffect{}}
		}
		return nil
	}), nil)
	if !c.SetEffectConsumer(func(effect Effect) {
		mu.Lock()
		got = append(got, effect)
		mu.Unlock()
	}) {
		t.Fatal("late effect consumer was not attached")
	}
	go c.Run()
	defer c.Close()
	if !c.Post(DrawRequested{Key: "late-bind"}) {
		t.Fatal("failed to post draw request")
	}
	c.WaitIdle()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("effect count = %d, want 1", len(got))
	}
	if _, ok := got[0].(FlushEffect); !ok {
		t.Fatalf("effect = %T, want FlushEffect", got[0])
	}
}

func TestUIController_LateBoundEffectConsumerCannotAttachAfterClose(t *testing.T) {
	c := NewUIController(UIControllerConfig{}, nil, nil)
	go c.Run()
	c.Close()
	if c.SetEffectConsumer(func(Effect) {}) {
		t.Fatal("closed controller accepted an effect consumer")
	}
}

// ---------------------------------------------------------------------------
// Coalescing：同 key 合并（dirty 并集 + latest-wins），不同 key 各自保留
// ---------------------------------------------------------------------------

func TestUIController_CoalescableLatestWinsAndDirtyUnion(t *testing.T) {
	c, rec := newP1Controller(t, 0)

	// 先全部投递再启动 Run：保证同 key 合并发生在队列中（确定性）。
	c.Post(DrawRequested{Key: "frame", Reason: "r1", Dirty: renderengine.DirtyContent, Generation: 1})
	c.Post(DrawRequested{Key: "frame", Reason: "r2", Dirty: renderengine.DirtyBand, Generation: 2})
	c.Post(DrawRequested{Key: "status", Reason: "r3", Dirty: renderengine.DirtyStatus, Generation: 3})
	go c.Run()
	defer c.Close()
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 2 {
		t.Fatalf("applied = %d, want 2 (merged)", len(applied))
	}
	first, ok := applied[0].action.(DrawRequested)
	if !ok {
		t.Fatalf("applied[0] = %T, want DrawRequested", applied[0].action)
	}
	if first.Key != "frame" || first.Reason != "r2" || first.Generation != 2 {
		t.Errorf("merged frame = %+v, want Reason r2 / Generation 2", first)
	}
	if first.Dirty != renderengine.DirtyContent|renderengine.DirtyBand {
		t.Errorf("merged dirty = %v, want Content|Band", first.Dirty)
	}
	second, ok := applied[1].action.(DrawRequested)
	if !ok || second.Key != "status" {
		t.Errorf("applied[1] = %+v, want DrawRequested(status)", applied[1].action)
	}
	stats := c.Stats()
	if stats.Posted != 3 || stats.Dropped != 1 {
		t.Errorf("stats = %+v, want Posted 3 / Dropped 1", stats)
	}
}

func TestUIController_CoalescableNoKeyNeverMerged(t *testing.T) {
	c, rec := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	for i := 0; i < 3; i++ {
		if !c.Post(p1NoKeyCoalescable{}) {
			t.Fatalf("post %d rejected", i)
		}
	}
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 3 {
		t.Fatalf("applied = %d, want 3 (no merge without key)", len(applied))
	}
	if stats := c.Stats(); stats.Dropped != 0 {
		t.Errorf("stats.Dropped = %d, want 0", stats.Dropped)
	}
}

func TestUIController_PromptEditorStatusCoalescesToLatestValue(t *testing.T) {
	c, rec := newP1Controller(t, 1)
	if !c.Post(SetPromptEditorStatusAction{Line: "多行 1/2"}) {
		t.Fatal("failed to post first editor status")
	}
	if !c.Post(SetPromptEditorStatusAction{Line: "多行 2/2"}) {
		t.Fatal("failed to coalesce editor status")
	}
	go c.Run()
	defer c.Close()
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want one coalesced editor status", len(applied))
	}
	status, ok := applied[0].action.(SetPromptEditorStatusAction)
	if !ok || status.Line != "多行 2/2" {
		t.Fatalf("applied action = %#v, want latest editor status", applied[0].action)
	}
	if stats := c.Stats(); stats.Posted != 2 || stats.Dropped != 1 {
		t.Fatalf("stats = %+v, want one merged status", stats)
	}
}

// p1NoKeyCoalescable 是无 key 的 coalescable 测试类型。
type p1NoKeyCoalescable struct{}

func (p1NoKeyCoalescable) isUIAction()         {}
func (p1NoKeyCoalescable) Class() ActionClass  { return ClassCoalescable }
func (p1NoKeyCoalescable) CoalesceKey() string { return "" }

// ---------------------------------------------------------------------------
// Barrier：顺序保证
// ---------------------------------------------------------------------------

func TestUIController_BarrierOrdersAroundIt(t *testing.T) {
	c, rec := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	c.Post(RuntimeEvent{Kind: "before"})
	c.Post(Resize{Width: 80, Height: 24})
	c.Post(RuntimeEvent{Kind: "after"})
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 3 {
		t.Fatalf("applied = %d, want 3", len(applied))
	}
	if _, ok := applied[0].action.(RuntimeEvent); !ok {
		t.Errorf("applied[0] = %T, want RuntimeEvent", applied[0].action)
	}
	if _, ok := applied[1].action.(Resize); !ok {
		t.Errorf("applied[1] = %T, want Resize (barrier)", applied[1].action)
	}
	if _, ok := applied[2].action.(RuntimeEvent); !ok {
		t.Errorf("applied[2] = %T, want RuntimeEvent", applied[2].action)
	}
}

func TestUIController_BarrierSurvivesCoalescableFlood(t *testing.T) {
	c, rec := newP1Controller(t, 8)

	// 先投递全部 flood（同 key 合并进同一槽位，不触发背压），
	// 再投递 barrier，最后启动 Run：顺序确定性。
	const posts = 10000
	for i := 0; i < posts; i++ {
		if !c.Post(DrawRequested{Key: "frame", Reason: "flood", Dirty: renderengine.DirtyContent, Generation: uint64(i)}) {
			t.Fatalf("flood post %d rejected", i)
		}
	}
	if !c.Post(Resize{Width: 120, Height: 40}) {
		t.Fatal("barrier post rejected")
	}
	go c.Run()
	defer c.Close()
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 2 {
		t.Fatalf("applied = %d, want 2 (flood merged to 1 + barrier)", len(applied))
	}
	draw, ok := applied[0].action.(DrawRequested)
	if !ok {
		t.Fatalf("applied[0] = %T, want DrawRequested", applied[0].action)
	}
	if draw.Generation != posts-1 {
		t.Errorf("draw.Generation = %d, want latest %d", draw.Generation, posts-1)
	}
	if _, ok := applied[1].action.(Resize); !ok {
		t.Errorf("applied[1] = %T, want Resize after flood", applied[1].action)
	}
	stats := c.Stats()
	if stats.Posted != posts+1 || stats.Dropped != posts-1 {
		t.Errorf("stats = %+v, want Posted %d / Dropped %d", stats, posts+1, posts-1)
	}
	if stats.Pending != 0 {
		t.Errorf("stats.Pending = %d, want 0", stats.Pending)
	}
}

// ---------------------------------------------------------------------------
// 背压：bounded mailbox 满时 durable 阻塞，Run 启动后排空
// ---------------------------------------------------------------------------

func TestUIController_BoundedMailboxBackpressure(t *testing.T) {
	c, _ := newP1Controller(t, 2)
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "A"}) || !c.Post(RuntimeEvent{Kind: "B"}) {
		t.Fatal("first two posts should be accepted")
	}
	done := make(chan bool, 1)
	go func() {
		done <- c.Post(RuntimeEvent{Kind: "C"})
	}()
	select {
	case <-done:
		t.Fatal("third post should block on full mailbox")
	case <-time.After(100 * time.Millisecond):
	}

	go c.Run()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("post rejected after drain")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked post never drained")
	}
	c.WaitIdle()
}

func TestUIController_TryPostBackpressure(t *testing.T) {
	c, _ := newP1Controller(t, 2)
	defer c.Close()

	if !c.TryPost(RuntimeEvent{Kind: "A"}) {
		t.Fatal("first TryPost should be accepted")
	}
	if !c.TryPost(DrawRequested{Key: "frame", Generation: 1}) {
		t.Fatal("TryPost coalescable should be accepted into free slot")
	}
	// 队列已满（cap 2），同 key 合并仍应成功（不占新槽位）。
	if !c.TryPost(DrawRequested{Key: "frame", Generation: 2}) {
		t.Error("TryPost coalescable should merge into pending slot")
	}
	// 队列已满且无待处理 key：durable 非阻塞投递失败。
	if c.TryPost(RuntimeEvent{Kind: "C"}) {
		t.Error("TryPost should fail on full mailbox")
	}
	go c.Run()
	c.WaitIdle()
	// 合并后 frame 只处理一次：A, frame(gen2)。
	if rev := c.Revision(); rev != 2 {
		t.Errorf("Revision() = %d, want 2", rev)
	}
	c.Close()
	if c.Post(RuntimeEvent{Kind: "D"}) {
		t.Error("Post after Close should be rejected")
	}
	if c.TryPost(RuntimeEvent{Kind: "E"}) {
		t.Error("TryPost after Close should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Reducer follow-up：facade 重入不得等待自身消费 mailbox
// ---------------------------------------------------------------------------

func TestUIController_FollowupBypassesFullExternalMailbox(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	followupPosted := make(chan bool, 1)
	rec := &p1Recorder{}
	var c *UIController
	c = NewUIController(UIControllerConfig{MailboxSize: 1}, ReducerFunc(func(rev uint64, action UIAction) []Effect {
		rec.apply(rev, action)
		if event, ok := action.(RuntimeEvent); ok && event.Kind == "A" {
			close(started)
			<-release
			followupPosted <- c.PostFollowup(SetActiveBandAction{RawLines: []string{"child"}})
		}
		return nil
	}), nil)
	go c.Run()
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "A"}) {
		t.Fatal("failed to post parent action")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parent reducer did not start")
	}
	// The parent is in flight, so this one-slot external mailbox is now full.
	if !c.Post(RuntimeEvent{Kind: "B"}) {
		t.Fatal("failed to fill external mailbox")
	}
	close(release)

	select {
	case ok := <-followupPosted:
		if !ok {
			t.Fatal("reducer follow-up was rejected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up blocked behind the reducer's own full mailbox")
	}
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 3 {
		t.Fatalf("applied = %d, want parent + follow-up + external action", len(applied))
	}
	if got := actionClassString(applied[0].action); got != "RuntimeEvent(A)" {
		t.Errorf("applied[0] = %s, want RuntimeEvent(A)", got)
	}
	if _, ok := applied[1].action.(SetActiveBandAction); !ok {
		t.Errorf("applied[1] = %T, want reducer follow-up", applied[1].action)
	}
	if got := actionClassString(applied[2].action); got != "RuntimeEvent(B)" {
		t.Errorf("applied[2] = %s, want RuntimeEvent(B)", got)
	}
	for i, item := range applied {
		if item.revision != uint64(i) {
			t.Errorf("applied[%d].revision = %d, want %d", i, item.revision, i)
		}
	}
	if stats := c.Stats(); stats.Pending != 0 || stats.Posted != 3 || stats.Processed != 3 {
		t.Errorf("stats = %+v, want fully drained three-action sequence", stats)
	}
}

func TestUIController_PostDeferredPreservesFIFOBeyondMailboxCapacity(t *testing.T) {
	c := NewUIController(UIControllerConfig{MailboxSize: 1}, nil, nil)
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "external"}) {
		t.Fatal("failed to post external action")
	}
	if !c.PostDeferred(SetStatusModelsAction{}) {
		t.Fatal("failed to post deferred facade action")
	}
	if stats := c.Stats(); stats.Pending != 2 {
		t.Fatalf("pending = %d, want deferred action to remain in FIFO queue", stats.Pending)
	}
	if c.TryPost(RuntimeEvent{Kind: "external-after-deferred"}) {
		t.Fatal("deferred facade action must not open a capacity slot for external TryPost")
	}

	var applied []UIAction
	c.reducer = ReducerFunc(func(_ uint64, action UIAction) []Effect {
		applied = append(applied, action)
		return nil
	})
	go c.Run()
	c.WaitIdle()
	if len(applied) != 2 {
		t.Fatalf("applied = %#v, want two actions", applied)
	}
	if event, ok := applied[0].(RuntimeEvent); !ok || event.Kind != "external" {
		t.Fatalf("applied[0] = %#v, want external runtime event", applied[0])
	}
	if _, ok := applied[1].(SetStatusModelsAction); !ok {
		t.Fatalf("applied[1] = %T, want deferred facade action", applied[1])
	}
}

func TestUIController_PostDeferredStatsTrackOverflowAndMerge(t *testing.T) {
	c := NewUIController(UIControllerConfig{MailboxSize: 2}, nil, nil)
	defer c.Close()

	if !c.PostDeferred(DrawRequested{Key: "scheduled.a"}) {
		t.Fatal("first deferred draw rejected")
	}
	if !c.PostDeferred(DrawRequested{Key: "scheduled.a"}) {
		t.Fatal("coalescable deferred draw rejected")
	}
	if !c.PostDeferred(DrawRequested{Key: "scheduled.b"}) {
		t.Fatal("second-key deferred draw rejected")
	}
	if !c.PostDeferred(DrawRequested{Key: "scheduled.c"}) {
		t.Fatal("third-key deferred draw rejected")
	}

	stats := c.Stats()
	if stats.DeferredPosted != 4 || stats.DeferredMerged != 1 {
		t.Fatalf("deferred counters = %+v, want posted=4 merged=1", stats)
	}
	if stats.Pending != 3 || stats.PeakPending != 3 || stats.CapacityOverflow != 1 {
		t.Fatalf("deferred queue stats = %+v, want pending=3 peak=3 overflow=1", stats)
	}
}

func TestUIController_FollowupRejectsOutsideReducer(t *testing.T) {
	c, _ := newP1Controller(t, 1)
	defer c.Close()
	if c.PostFollowup(RuntimeEvent{Kind: "outside"}) {
		t.Fatal("follow-up must be rejected outside an active reducer")
	}
}

func TestUIController_ExternalFollowupCannotOvertakeQueuedMailboxAction(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	followupResult := make(chan bool, 1)
	externalDone := make(chan bool, 1)
	rec := &p1Recorder{}
	var c *UIController
	c = NewUIController(UIControllerConfig{MailboxSize: 1}, ReducerFunc(func(rev uint64, action UIAction) []Effect {
		rec.apply(rev, action)
		if event, ok := action.(RuntimeEvent); ok && event.Kind == "A" {
			close(started)
			<-release
		}
		return nil
	}), nil)
	go c.Run()
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "A"}) {
		t.Fatal("failed to post parent action")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parent reducer did not start")
	}
	if !c.Post(RuntimeEvent{Kind: "B"}) {
		t.Fatal("failed to queue the earlier external action")
	}
	go func() {
		followupResult <- c.PostFollowup(RuntimeEvent{Kind: "must-not-overtake"})
		externalDone <- c.Post(RuntimeEvent{Kind: "C"})
	}()
	select {
	case accepted := <-followupResult:
		if accepted {
			t.Fatal("external producer incorrectly entered the reducer follow-up lane")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external follow-up attempt did not return")
	}
	close(release)
	select {
	case ok := <-externalDone:
		if !ok {
			t.Fatal("external FIFO post was rejected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external FIFO post did not drain")
	}
	c.WaitIdle()

	got := rec.appliedKinds()
	want := []string{"RuntimeEvent(A)", "RuntimeEvent(B)", "RuntimeEvent(C)"}
	if len(got) != len(want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applied = %v, want %v", got, want)
		}
	}
}

func TestUIController_ContextualFollowupUsesBoundedCapability(t *testing.T) {
	var retained *ReducerContext
	contextPosted := make(chan bool, 1)
	rec := &p1Recorder{}
	c := NewUIController(UIControllerConfig{MailboxSize: 1}, ContextualReducerFunc(func(rev uint64, action UIAction, context *ReducerContext) []Effect {
		rec.apply(rev, action)
		if event, ok := action.(RuntimeEvent); ok && event.Kind == "A" {
			retained = context
			contextPosted <- context.PostFollowup(RuntimeEvent{Kind: "child"})
		}
		return nil
	}), nil)
	go c.Run()
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "A"}) || !c.Post(RuntimeEvent{Kind: "B"}) {
		t.Fatal("failed to post contextual test actions")
	}
	select {
	case ok := <-contextPosted:
		if !ok {
			t.Fatal("contextual reducer follow-up was rejected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contextual reducer did not emit its follow-up")
	}
	c.WaitIdle()
	if retained == nil || retained.PostFollowup(RuntimeEvent{Kind: "late"}) {
		t.Fatal("expired reducer context accepted a delayed follow-up")
	}

	got := rec.appliedKinds()
	want := []string{"RuntimeEvent(A)", "RuntimeEvent(child)", "RuntimeEvent(B)"}
	if len(got) != len(want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applied = %v, want %v", got, want)
		}
	}
}

func TestUIController_PanickedReducerDiscardsItsFollowups(t *testing.T) {
	rec := &p1Recorder{}
	var c *UIController
	c = NewUIController(UIControllerConfig{}, ReducerFunc(func(rev uint64, action UIAction) []Effect {
		rec.apply(rev, action)
		if event, ok := action.(RuntimeEvent); ok && event.Kind == "boom" {
			if !c.PostFollowup(SetActiveBandAction{RawLines: []string{"must not escape panic"}}) {
				panic("follow-up rejected")
			}
			panic("test panic after follow-up")
		}
		return nil
	}), nil)
	go c.Run()
	defer c.Close()

	c.Post(RuntimeEvent{Kind: "boom"})
	c.Post(RuntimeEvent{Kind: "after"})
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 2 || actionClassString(applied[0].action) != "RuntimeEvent(boom)" || actionClassString(applied[1].action) != "RuntimeEvent(after)" {
		t.Fatalf("panicked action leaked a follow-up: %v", rec.appliedKinds())
	}
	if stats := c.Stats(); stats.ReducerPanics != 1 || stats.Processed != 2 || stats.Pending != 0 {
		t.Fatalf("stats = %+v, want one panic and no surviving follow-up", stats)
	}
}

// ---------------------------------------------------------------------------
// Shutdown：Close 后排空剩余队列并退出
// ---------------------------------------------------------------------------

func TestUIController_ShutdownDrainsThenRejects(t *testing.T) {
	c, rec := newP1Controller(t, 0)

	c.Post(RuntimeEvent{Kind: "A"})
	c.Post(RuntimeEvent{Kind: "B"})
	c.Close()
	c.Close() // 幂等

	done := make(chan struct{})
	go func() {
		c.Run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after Close")
	}
	if stats := c.Stats(); stats.Processed != 2 {
		t.Errorf("Processed = %d, want 2 (drained before exit)", stats.Processed)
	}
	if len(rec.appliedKinds()) != 2 {
		t.Errorf("applied = %v, want 2 records", rec.appliedKinds())
	}
	if c.Post(RuntimeEvent{Kind: "C"}) {
		t.Error("Post after Close should be rejected")
	}
	if c.Revision() != 2 {
		t.Errorf("Revision() = %d, want 2", c.Revision())
	}
}

// ---------------------------------------------------------------------------
// 多生产者并发投递：无丢失、无死锁
// ---------------------------------------------------------------------------

func TestUIController_ConcurrentProducersNoLoss(t *testing.T) {
	c, rec := newP1Controller(t, 16)
	go c.Run()
	defer c.Close()

	const producers = 8
	const perProducer = 500
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				if !c.Post(RuntimeEvent{Kind: "evt", Payload: p*perProducer + i}) {
					t.Errorf("producer %d post rejected", p)
					return
				}
			}
		}(p)
	}
	wg.Wait()
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != producers*perProducer {
		t.Fatalf("applied = %d, want %d (no loss)", len(applied), producers*perProducer)
	}
	if stats := c.Stats(); stats.Dropped != 0 || stats.Pending != 0 {
		t.Errorf("stats = %+v, want no drops/pending", stats)
	}
}

func TestUIController_ConcurrentSameKeyMerge(t *testing.T) {
	c, rec := newP1Controller(t, 16)

	// 先并发投递（同 key 全部合并进同一槽位，队列 ≤ 1 不触发背压），
	// 再启动 Run：确定性断言只应用一次。
	const producers = 8
	const perProducer = 500
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				if !c.Post(DrawRequested{Key: "frame", Reason: "p", Generation: uint64(i)}) {
					t.Errorf("producer %d post rejected", p)
					return
				}
			}
		}(p)
	}
	wg.Wait()
	go c.Run()
	defer c.Close()
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1 (all merged)", len(applied))
	}
	draw := applied[0].action.(DrawRequested)
	if draw.Generation != perProducer-1 {
		t.Errorf("Generation = %d, want %d (latest wins)", draw.Generation, perProducer-1)
	}
}

// ---------------------------------------------------------------------------
// Starvation：coalescable 洪泛不得饿死 durable/barrier
// ---------------------------------------------------------------------------

func TestUIController_CoalescableFloodDoesNotStarveDurableOrBarrier(t *testing.T) {
	rec := &p1Recorder{}
	// 慢 reducer：模拟每帧渲染耗时，放大饥饿窗口。
	slow := ReducerFunc(func(rev uint64, a UIAction) []Effect {
		time.Sleep(200 * time.Microsecond)
		return rec.apply(rev, a)
	})
	c := NewUIController(UIControllerConfig{MailboxSize: 16}, slow, rec.effect)
	go c.Run()
	defer c.Close()

	stop := make(chan struct{})
	durableDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // coalescable 洪泛：持续同 key 意图，只占一个槽位
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				if !c.Post(DrawRequested{Key: "frame", Reason: "flood", Dirty: renderengine.DirtyContent, Generation: uint64(i)}) {
					return
				}
			}
		}
	}()
	go func() { // durable 洪泛：200 个内容事件必须全部被处理（不饿死、不丢失）
		defer wg.Done()
		defer close(durableDone)
		for i := 0; i < 200; i++ {
			if !c.Post(RuntimeEvent{Kind: "evt"}) {
				t.Errorf("durable post %d rejected", i)
				return
			}
		}
	}()
	<-durableDone // 洪泛器持续运行期间 durable 全部投递完成
	close(stop)
	wg.Wait()
	if !c.Post(Resize{Width: 120, Height: 40}) {
		t.Fatal("barrier post rejected")
	}
	c.WaitIdle()

	applied, _ := rec.snapshot()
	evtCount := 0
	resizeIdx := -1
	for i, rec := range applied {
		switch rec.action.(type) {
		case RuntimeEvent:
			evtCount++
		case Resize:
			resizeIdx = i
		}
	}
	if evtCount != 200 {
		t.Errorf("applied RuntimeEvent = %d, want 200 (durable must not be starved/lost)", evtCount)
	}
	if resizeIdx < 0 {
		t.Fatal("barrier Resize never applied")
	}
	// barrier 语义：Resize 在其之前入队的所有 action 之后执行（此处即全部 durable 与合并后的 draw）。
	if applied[resizeIdx].action.(Resize).Width != 120 {
		t.Errorf("Resize.Width = %d, want 120", applied[resizeIdx].action.(Resize).Width)
	}
	stats := c.Stats()
	if stats.Dropped == 0 {
		t.Errorf("stats.Dropped = 0, want > 0 (flood merged into pending slot)")
	}
	if stats.Pending != 0 {
		t.Errorf("stats.Pending = %d, want 0", stats.Pending)
	}
}

// ---------------------------------------------------------------------------
// Reducer panic 恢复
// ---------------------------------------------------------------------------

func TestUIController_ReducerPanicRecovered(t *testing.T) {
	rec := &p1Recorder{}
	reducer := ReducerFunc(func(rev uint64, a UIAction) []Effect {
		if re, ok := a.(RuntimeEvent); ok && re.Kind == "boom" {
			panic("reducer exploded")
		}
		return rec.apply(rev, a)
	})
	c := NewUIController(UIControllerConfig{}, reducer, rec.effect)
	go c.Run()
	defer c.Close()

	c.Post(RuntimeEvent{Kind: "boom"})
	c.Post(RuntimeEvent{Kind: "ok"})
	c.WaitIdle()

	applied, _ := rec.snapshot()
	if len(applied) != 1 || applied[0].action.(RuntimeEvent).Kind != "ok" {
		t.Fatalf("applied = %v, want only the ok event after panic", rec.appliedKinds())
	}
	stats := c.Stats()
	if stats.ReducerPanics != 1 {
		t.Errorf("ReducerPanics = %d, want 1", stats.ReducerPanics)
	}
	if stats.Processed != 2 || stats.Revision != 2 {
		t.Errorf("stats = %+v, want Processed/Revision 2 (panicked action consumed)", stats)
	}
	if stats.Pending != 0 {
		t.Errorf("stats.Pending = %d, want 0 (loop continued)", stats.Pending)
	}
}

// ---------------------------------------------------------------------------
// Effect 投递
// ---------------------------------------------------------------------------

func TestUIController_EffectsDeliveredInOrder(t *testing.T) {
	rec := &p1Recorder{}
	reducer := ReducerFunc(func(rev uint64, a UIAction) []Effect {
		rec.apply(rev, a)
		switch a.(type) {
		case RuntimeEvent:
			return []Effect{FlushEffect{Dirty: renderengine.DirtyContent}}
		case DrawRequested:
			return []Effect{PostActionEffect{Action: RuntimeEvent{Kind: "followup"}}}
		}
		return nil
	})
	c := NewUIController(UIControllerConfig{}, reducer, rec.effect)
	go c.Run()
	defer c.Close()

	c.Post(RuntimeEvent{Kind: "evt"})
	c.Post(DrawRequested{Key: "frame", Generation: 1})
	c.WaitIdle()

	_, effects := rec.snapshot()
	if len(effects) != 2 {
		t.Fatalf("effects = %d, want 2", len(effects))
	}
	for i, effect := range effects {
		if _, ok := effect.(FlushEffect); !ok {
			t.Errorf("effects[%d] = %T, want FlushEffect", i, effect)
		}
	}
	got := rec.appliedKinds()
	want := []string{"RuntimeEvent(evt)", "DrawRequested(frame)", "RuntimeEvent(followup)"}
	if len(got) != len(want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applied = %v, want %v", got, want)
		}
	}
}

func TestUIController_PostActionEffectPrecedesNextExternalMailboxAction(t *testing.T) {
	rec := &p1Recorder{}
	c := NewUIController(UIControllerConfig{MailboxSize: 1}, ReducerFunc(func(rev uint64, action UIAction) []Effect {
		rec.apply(rev, action)
		if event, ok := action.(RuntimeEvent); ok && event.Kind == "A" {
			return []Effect{PostActionEffect{Action: RuntimeEvent{Kind: "child"}}}
		}
		return nil
	}), nil)
	go c.Run()
	defer c.Close()

	if !c.Post(RuntimeEvent{Kind: "A"}) || !c.Post(RuntimeEvent{Kind: "B"}) {
		t.Fatal("failed to post test actions")
	}
	c.WaitIdle()
	got := rec.appliedKinds()
	want := []string{"RuntimeEvent(A)", "RuntimeEvent(child)", "RuntimeEvent(B)"}
	if len(got) != len(want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applied = %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 1 actor-owned transition state
// ---------------------------------------------------------------------------

func TestUIController_TracksBarrierTransitionState(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	c.Post(Resize{Width: 120, Height: 42, Generation: 9})
	c.Post(LeaseAcquired{LeaseID: 41})
	c.Post(LeaseReleased{LeaseID: 40}) // stale release must not clear lease 41
	c.Post(EffectResult{Token: 73, MayHavePartiallyWritten: true})
	c.Post(DrawRequested{Key: "frame", Reason: "test", Dirty: renderengine.DirtyBand, Generation: 12})
	c.Post(LeaseReleased{LeaseID: 41})
	c.WaitIdle()

	state := c.State()
	if state.Revision != 6 {
		t.Fatalf("state revision = %d, want 6", state.Revision)
	}
	if state.Geometry != (GeometryState{Width: 120, Height: 42, Generation: 9}) {
		t.Fatalf("geometry = %+v", state.Geometry)
	}
	if state.Lease.Active || state.Lease.ID != 0 {
		t.Fatalf("lease after matching release = %+v", state.Lease)
	}
	if state.Effects.Count != 1 || state.Effects.Last.Token != 73 || !state.Effects.Last.MayHavePartiallyWritten {
		t.Fatalf("effects = %+v", state.Effects)
	}
	if state.LastDraw.Key != "frame" || state.LastDraw.Generation != 12 || state.LastDraw.Dirty != renderengine.DirtyBand {
		t.Fatalf("last draw = %+v", state.LastDraw)
	}
}

func TestUIController_PanickedActionDoesNotMutateTransitionState(t *testing.T) {
	reducer := ReducerFunc(func(_ uint64, action UIAction) []Effect {
		if _, ok := action.(LeaseAcquired); ok {
			panic("test panic")
		}
		return nil
	})
	c := NewUIController(UIControllerConfig{}, reducer, nil)
	go c.Run()
	defer c.Close()

	c.Post(LeaseAcquired{LeaseID: 44})
	c.Post(EffectResult{Token: 45})
	c.WaitIdle()

	state := c.State()
	if state.Lease.Active || state.Lease.ID != 0 {
		t.Fatalf("panicked action mutated lease state: %+v", state.Lease)
	}
	if state.Effects.Count != 1 || state.Effects.Last.Token != 45 {
		t.Fatalf("subsequent action was not tracked: %+v", state.Effects)
	}
	if state.Revision != 2 {
		t.Fatalf("state revision = %d, want absolute controller revision 2", state.Revision)
	}
}

func TestUIController_AppStateSnapshotTracksActorDomainsAndDetaches(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	finalizedAt := time.Unix(1_700_000_000, 0).UTC()
	transcript := &scene.Snapshot{
		Revision: 7,
		Cells: []*scene.TranscriptCell{{
			ID:          41,
			Sequence:    3,
			Kind:        scene.KindAssistant,
			Source:      "semantic transcript",
			Revision:    5,
			Phase:       scene.CellCommitted,
			FinalizedAt: &finalizedAt,
		}},
	}
	actions := []UIAction{
		ReplaceTranscriptAction{Snapshot: transcript},
		SetActiveCellAction{Active: ActiveCellState{
			CellID:   42,
			Revision: 9,
			Kind:     scene.KindAssistant,
			Phase:    ActiveCellMutable,
			Source:   "mutable semantic source",
			Stable:   SourceRange{Start: 0, End: 8},
		}},
		Resize{Width: 120, Height: 42, Generation: 13},
		LeaseAcquired{LeaseID: 88},
		SetStatusModelsAction{Status: style.StatusLineModel{
			State:     style.RunReady,
			StateText: "Ready",
			Segments:  []style.StatusSegment{{Text: "status"}},
		}},
		ShowPromptAction{Line: "> "},
		InputEvent{Text: "draft", Cursor: 3, PasteActive: true},
		SetPromptStateAction{Line: "> ", Input: "draft", Rows: 2, CursorRow: 1, CursorCol: 2},
		SetActiveBandAction{RawLines: []string{"legacy projection only"}},
		ShowPopupAction{Lines: []string{"choice"}, Owner: "selection", Prompt: "Select: "},
	}
	for _, action := range actions {
		if !c.Post(action) {
			t.Fatalf("Post(%T) rejected", action)
		}
	}
	c.WaitIdle()

	state := c.State()
	if state.Revision != uint64(len(actions)) || state.LayoutGeneration != 13 {
		t.Fatalf("revision/layout = %d/%d, want %d/13", state.Revision, state.LayoutGeneration, len(actions))
	}
	if state.Geometry != (GeometryState{Width: 120, Height: 42, Generation: 13}) || !state.Lease.Active || state.Lease.ID != 88 {
		t.Fatalf("geometry/lease = %+v/%+v", state.Geometry, state.Lease)
	}
	if state.Transcript.Revision != 7 || len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != "semantic transcript" {
		t.Fatalf("transcript = %+v", state.Transcript)
	}
	if state.Active.CellID != 42 || state.Active.Kind != scene.KindAssistant || state.Active.Phase != ActiveCellMutable || state.Active.Source != "mutable semantic source" {
		t.Fatalf("active = %+v", state.Active)
	}
	if state.Bottom.PromptInput != "draft" || state.Bottom.PromptCursor != 3 || !state.Bottom.PasteActive || !state.Bottom.PromptVisible {
		t.Fatalf("prompt bottom state = %+v", state.Bottom)
	}
	if state.Bottom.Focus != BottomFocusPopup || state.Bottom.ComposerLine != "Select: " || len(state.Bottom.PopupLines) != 1 {
		t.Fatalf("popup bottom state = %+v", state.Bottom)
	}
	if len(state.Bottom.ActiveBandLines) != 1 || state.Bottom.ActiveBandLines[0] != "legacy projection only" {
		t.Fatalf("legacy active projection = %q", state.Bottom.ActiveBandLines)
	}

	// State() must be an immutable snapshot, not an alias of controller state.
	state.Transcript.Cells[0].Source = "mutated outside actor"
	*state.Transcript.Cells[0].FinalizedAt = time.Time{}
	state.Bottom.PopupLines[0] = "mutated outside actor"
	state.Bottom.ActiveBandLines[0] = "mutated outside actor"
	again := c.State()
	if again.Transcript.Cells[0].Source != "semantic transcript" || !again.Transcript.Cells[0].FinalizedAt.Equal(finalizedAt) || again.Bottom.PopupLines[0] != "choice" || again.Bottom.ActiveBandLines[0] != "legacy projection only" {
		t.Fatalf("State returned actor-owned mutable data: %+v", again)
	}
}

func TestUIController_TranscriptActionDerivesMutableActiveCell(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	c.Post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4,
		Cells: []*scene.TranscriptCell{
			{ID: 11, Revision: 2, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted},
			{ID: 12, Revision: 3, Kind: scene.KindAssistant, Source: "live source", Phase: scene.CellMutable},
		},
	}})
	c.WaitIdle()

	state := c.State()
	if state.Active.CellID != 12 || state.Active.Revision != 3 || state.Active.Kind != scene.KindAssistant || state.Active.Phase != ActiveCellMutable || state.Active.Source != "live source" {
		t.Fatalf("active state = %+v, want mutable semantic cell", state.Active)
	}
	if !state.Active.Stable.Valid() || state.Active.Stable != (SourceRange{}) || state.Active.Enqueued != (SourceRange{}) || state.Active.Acked != (SourceRange{}) {
		t.Fatalf("derived active state guessed stream-effect progress: %+v", state.Active)
	}

	c.Post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 5,
		Cells:    []*scene.TranscriptCell{{ID: 12, Revision: 4, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted}},
	}})
	c.WaitIdle()
	if state := c.State(); state.Active != (ActiveCellState{}) {
		t.Fatalf("finalized transcript retained stale active source: %+v", state.Active)
	}
}

func TestUIController_AppStatePopupStackFollowsOwnerAndHandleActions(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	first := PopupHandle{owner: "modal:selection", instance: 1}
	second := PopupHandle{owner: "modal:selection", instance: 2}
	actions := []UIAction{
		ShowPopupAction{Lines: []string{"agent panel"}, Owner: "agent_panel", PreserveCursor: true},
		ShowPopupAction{Lines: []string{"slash"}, Owner: "slash_completion", PreserveCursor: true},
		ClearPopupAction{Owner: "slash_completion", PreserveCursor: true},
		ShowPopupAction{Lines: []string{"first"}, Owner: first.owner, Prompt: "first> ", Input: true, Handle: &first},
		ShowPopupAction{Lines: []string{"second"}, Owner: second.owner, Prompt: "second> ", Input: true, Handle: &second},
		UpdatePopupAction{Handle: first, Lines: []string{"first updated"}, Prompt: "first> ", PreserveCursor: true},
		ClearPopupAction{Handle: &first, PreserveCursor: true},
		ClearPopupAction{Handle: &second, PreserveCursor: true},
	}
	for _, action := range actions {
		if !c.Post(action) {
			t.Fatalf("Post(%T) rejected", action)
		}
	}
	c.WaitIdle()

	state := c.AppState()
	if state.Bottom.PopupOwner != "agent_panel" || len(state.Bottom.PopupLines) != 1 || state.Bottom.PopupLines[0] != "agent panel" {
		t.Fatalf("popup restoration = %+v, want agent panel", state.Bottom)
	}
	if state.Bottom.PopupInstance != 0 || len(state.Bottom.PopupStack) != 0 || state.Bottom.Focus != BottomFocusPopup {
		t.Fatalf("popup stack after tokenized cleanup = %+v", state.Bottom)
	}

	// A snapshot caller must not be able to mutate a suspended layer retained
	// by the actor. This catches shallow copies hidden below the active popup.
	c.Post(ShowPopupAction{Lines: []string{"modal"}, Owner: "modal:priority:test", Prompt: "go> ", Input: true, Handle: &first})
	c.WaitIdle()
	state = c.AppState()
	if len(state.Bottom.PopupStack) != 1 || state.Bottom.PopupStack[0].Lines[0] != "agent panel" {
		t.Fatalf("expected agent panel suspended below modal: %+v", state.Bottom)
	}
	state.Bottom.PopupStack[0].Lines[0] = "outside mutation"
	again := c.AppState()
	if again.Bottom.PopupStack[0].Lines[0] != "agent panel" {
		t.Fatalf("AppState leaked popup-stack memory: %+v", again.Bottom.PopupStack)
	}
}

func TestUIController_AppStateTracksRemainingBottomPaneFacadeActions(t *testing.T) {
	c, _ := newP1Controller(t, 0)
	go c.Run()
	defer c.Close()

	dynamic := &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"}
	actions := []UIAction{
		SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: "Ready"}},
		SetDynamicStatusModelAction{Dynamic: dynamic},
		ShowPromptAction{Line: "> "},
		SetPromptNoticeAction{Line: "queued"},
		SetPromptEditorStatusAction{Line: "editing"},
		TrackPromptInputAction{Line: "> ", Input: "draft", Rows: 2, CursorRow: 1, CursorCol: 5},
		SetPromptRowsAction{Rows: 3},
	}
	for _, action := range actions {
		if !c.Post(action) {
			t.Fatalf("Post(%T) rejected", action)
		}
	}
	c.WaitIdle()

	state := c.AppState()
	if state.Bottom.StatusModel == nil || state.Bottom.StatusModel.StateText != "Ready" || state.Bottom.DynamicStatusModel == nil || state.Bottom.DynamicStatusModel.StateText != "Working" {
		t.Fatalf("status actions did not update the one BottomPane status state: %+v", state.Bottom)
	}
	if state.Bottom.PromptInput != "draft" || state.Bottom.PromptReservedRows != 3 || state.Bottom.PromptCursorRow != 1 || state.Bottom.PromptCursorCol != 5 || !state.Bottom.PromptVisible {
		t.Fatalf("prompt state transition = %+v", state.Bottom)
	}
	if state.Bottom.PromptNoticeLine != "queued" || state.Bottom.PromptEditorStatusLine != "editing" || state.Bottom.Focus != BottomFocusPrompt {
		t.Fatalf("prompt context/focus = %+v", state.Bottom)
	}

	// Composer cleanup must clear prompt-owned overlay state while retaining an
	// existing popup owner. In particular it must not discard the popup stack
	// data that Phase 2 has moved out of FixedBottomSurface mutex state.
	for _, action := range []UIAction{
		ShowPopupAction{Lines: []string{"agent panel"}, Owner: "agent_panel", PreserveCursor: true},
		SetComposerPreviewAction{Line: "compose> "},
		ClearComposerPreviewAction{},
	} {
		if !c.Post(action) {
			t.Fatalf("Post(%T) rejected", action)
		}
	}
	c.WaitIdle()

	state = c.AppState()
	if state.Bottom.PopupOwner != "agent_panel" || len(state.Bottom.PopupLines) != 1 || state.Bottom.PopupLines[0] != "agent panel" || state.Bottom.ComposerLine != "" {
		t.Fatalf("composer cleanup damaged popup state: %+v", state.Bottom)
	}
	if state.Bottom.PromptVisible || state.Bottom.PromptReservedRows != 0 || state.Bottom.PromptNoticeLine != "" || state.Bottom.PromptEditorStatusLine != "" || state.Bottom.Focus != BottomFocusPopup {
		t.Fatalf("composer cleanup left stale prompt state: %+v", state.Bottom)
	}

	// Snapshot detach includes the two partial status actions introduced by this
	// slice; action payload pointers must not leak through AppState().
	*state.Bottom.DynamicStatusModel = style.StatusLineModel{StateText: "outside mutation"}
	if again := c.AppState(); again.Bottom.DynamicStatusModel == nil || again.Bottom.DynamicStatusModel.StateText != "Working" {
		t.Fatalf("dynamic status leaked actor memory: %+v", again.Bottom.DynamicStatusModel)
	}
}

// ---------------------------------------------------------------------------
// 分类表
// ---------------------------------------------------------------------------

func TestUIActionClassification(t *testing.T) {
	cases := []struct {
		name string
		act  UIAction
		cls  ActionClass
		key  string
	}{
		{"DrawRequested", DrawRequested{Key: "frame"}, ClassCoalescable, "frame"},
		{"Timer", Timer{Key: "cursor"}, ClassCoalescable, "cursor"},
		{"Resize", Resize{}, ClassBarrier, ""},
		{"LeaseAcquired", LeaseAcquired{}, ClassBarrier, ""},
		{"LeaseReleased", LeaseReleased{}, ClassBarrier, ""},
		{"EffectResult", EffectResult{}, ClassBarrier, ""},
		{"BeginHistoryCommit", BeginHistoryCommit{}, ClassBarrier, ""},
		{"HistoryCommitAcknowledged", HistoryCommitAcknowledged{}, ClassBarrier, ""},
		{"HistoryCommitsAcknowledged", HistoryCommitsAcknowledged{}, ClassBarrier, ""},
		{"HistoryCommitFailed", HistoryCommitFailed{}, ClassBarrier, ""},
		{"HistoryProjectionRecovered", HistoryProjectionRecovered{}, ClassBarrier, ""},
		{"HistoryProjectionInvalidated", HistoryProjectionInvalidated{}, ClassBarrier, ""},
		{"HistoryScrollbackReconciled", HistoryScrollbackReconciled{}, ClassBarrier, ""},
		{"RuntimeEvent", RuntimeEvent{}, ClassDurable, ""},
		{"InputEvent", InputEvent{}, ClassDurable, ""},
		{"SetActiveBandAction", SetActiveBandAction{}, ClassDurable, ""},
		{"ClearActiveBandAction", ClearActiveBandAction{}, ClassDurable, ""},
		{"SetStatusModelsAction", SetStatusModelsAction{}, ClassDurable, ""},
		{"SetStatusModelAction", SetStatusModelAction{}, ClassDurable, ""},
		{"SetDynamicStatusModelAction", SetDynamicStatusModelAction{}, ClassDurable, ""},
		{"ShowPromptAction", ShowPromptAction{}, ClassDurable, ""},
		{"PromptSubmittedAction", PromptSubmittedAction{}, ClassDurable, ""},
		{"SetPromptStateAction", SetPromptStateAction{}, ClassDurable, ""},
		{"TrackPromptInputAction", TrackPromptInputAction{}, ClassDurable, ""},
		{"ResetPromptAction", ResetPromptAction{}, ClassDurable, ""},
		{"SetPromptRowsAction", SetPromptRowsAction{}, ClassDurable, ""},
		{"SetPromptNoticeAction", SetPromptNoticeAction{}, ClassDurable, ""},
		{"SetPromptEditorStatusAction", SetPromptEditorStatusAction{}, ClassCoalescable, "prompt-editor-status"},
		{"SetComposerPreviewAction", SetComposerPreviewAction{}, ClassDurable, ""},
		{"ClearComposerPreviewAction", ClearComposerPreviewAction{}, ClassDurable, ""},
		{"ShowPopupAction", ShowPopupAction{}, ClassDurable, ""},
		{"ClearPopupAction", ClearPopupAction{}, ClassDurable, ""},
		{"UpdatePopupAction", UpdatePopupAction{}, ClassDurable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.act.Class(); got != tc.cls {
				t.Errorf("Class() = %v, want %v", got, tc.cls)
			}
			if got := tc.act.CoalesceKey(); got != tc.key {
				t.Errorf("CoalesceKey() = %q, want %q", got, tc.key)
			}
		})
	}
}

func TestInputEventSequenceMergeKeepsNewestDraftAndRenderIntent(t *testing.T) {
	if got := (InputEvent{Sequence: 1}).Class(); got != ClassCoalescable {
		t.Fatalf("sequenced InputEvent class = %v, want coalescable", got)
	}
	if got := (InputEvent{Sequence: 1}).CoalesceKey(); got != "prompt-input" {
		t.Fatalf("sequenced InputEvent key = %q, want prompt-input", got)
	}
	if got := (InputEvent{}).Class(); got != ClassDurable {
		t.Fatalf("legacy InputEvent class = %v, want durable", got)
	}
	merged := mergeActions(
		InputEvent{Text: "old", Cursor: 3, Render: true, Sequence: 1},
		InputEvent{Text: "new", Cursor: 3, Sequence: 2},
	)
	got, ok := merged.(InputEvent)
	if !ok || got.Text != "new" || got.Sequence != 2 || !got.Render {
		t.Fatalf("merged InputEvent = %#v, want newest text with preserved render", merged)
	}
}

func TestHistoryCommitWakeNeededForPromptSubmittedFence(t *testing.T) {
	// PromptSubmittedAction 是用户提交输入后的无条件重绘栅栏：即使 history
	// ledger 无 pending（例如 geometry 尚未发布导致 planEligibleHistoryCommits
	// 返回空），也必须 wake presenter，让用户消息块立即出帧，而不是等 LLM
	// 首个 chunk 触发 flush。
	state := UIControllerState{}
	if !historyCommitWakeNeeded(PromptSubmittedAction{}, state) {
		t.Fatal("PromptSubmittedAction must wake the presenter unconditionally")
	}
	state.HistoryEffects.Frozen = true
	if !historyCommitWakeNeeded(PromptSubmittedAction{}, state) {
		t.Fatal("PromptSubmittedAction must wake even while history effects are frozen")
	}
	// 对照组：同样的空状态（无 pending、无 reconciliation）下，普通 action
	// 不应 wake——证明 PromptSubmittedAction 走的是独立的无条件分支。
	state.HistoryEffects.Frozen = false
	if historyCommitWakeNeeded(SetActiveCellAction{}, state) {
		t.Fatal("SetActiveCellAction must not wake an idle ledger with no pending commits")
	}
}

func TestHistoryCommitWakeNeededForStandaloneScrollbackReconciliation(t *testing.T) {
	state := UIControllerState{}
	state.HistoryEffects.ProjectionUnknown = false
	state.HistoryEffects.ReconciliationRequired = true

	if !historyCommitWakeNeeded(HistoryProjectionRecovered{}, state) {
		t.Fatal("HistoryProjectionRecovered did not wake a known viewport with outstanding reconciliation")
	}
	if !historyCommitWakeNeeded(FinalizeActiveCellAction{}, state) {
		t.Fatal("finalize did not wake a known viewport with outstanding reconciliation")
	}

	state.Lease.Active = true
	if historyCommitWakeNeeded(HistoryProjectionRecovered{}, state) {
		t.Fatal("lease-active state must not wake an executor that cannot run")
	}
	state.Lease.Active = false
	state.HistoryEffects.Frozen = true
	if historyCommitWakeNeeded(HistoryProjectionRecovered{}, state) {
		t.Fatal("frozen history must not wake an executor that cannot run")
	}

	state.HistoryEffects.Frozen = false
	state.HistoryEffects.ReconciliationRequired = false
	if historyCommitWakeNeeded(HistoryProjectionRecovered{}, state) {
		t.Fatal("known reconciled state should not wake on recovery publication")
	}
}
