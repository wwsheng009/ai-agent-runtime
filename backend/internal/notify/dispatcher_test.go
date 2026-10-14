package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type notifyTestChannel struct {
	name  string
	err   error
	calls []Notification
}

func (c *notifyTestChannel) Name() string { return c.name }

func (c *notifyTestChannel) Deliver(_ context.Context, ev Event) error {
	c.calls = append(c.calls, ev.Notification)
	return c.err
}

func notifyTestNote(title string) Notification { return Notification{Title: title, Body: "b"} }

// TestDispatcherHighFansOutAndDedupesAcrossChannels 锁定 06 §2.4 的核心验收：
// high 优先级扇出全部通道；同一 DedupKey 跨通道只投一次（重发短路）。
func TestDispatcherHighFansOutAndDedupesAcrossChannels(t *testing.T) {
	c1 := &notifyTestChannel{name: "im"}
	c2 := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), c1, c2)

	r1, err := d.Dispatch(context.Background(), EventFor("approval", "key-1", notifyTestNote("n1")))
	if err != nil {
		t.Fatal(err)
	}
	if len(c1.calls) != 1 || len(c2.calls) != 1 {
		t.Fatalf("high 应扇出全部通道：im=%d webpush=%d", len(c1.calls), len(c2.calls))
	}
	if len(r1.Outcomes) != 2 {
		t.Fatalf("回执 outcomes = %+v", r1.Outcomes)
	}
	// 同 DedupKey 再次投递：跨通道短路，不再打扰。
	if _, err := d.Dispatch(context.Background(), EventFor("approval", "key-1", notifyTestNote("n1-again"))); err != nil {
		t.Fatal(err)
	}
	if len(c1.calls) != 1 || len(c2.calls) != 1 {
		t.Fatal("重复 DedupKey 不得再次投递")
	}
}

// TestDispatcherNormalGoesToPrimaryOnly：normal 只走主通道（06 D6）。
func TestDispatcherNormalGoesToPrimaryOnly(t *testing.T) {
	c1 := &notifyTestChannel{name: "im"}
	c2 := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), c1, c2)
	d.SetPrimary("webpush")

	if _, err := d.Dispatch(context.Background(), EventFor("completion", "k2", notifyTestNote("done"))); err != nil {
		t.Fatal(err)
	}
	if len(c1.calls) != 0 || len(c2.calls) != 1 {
		t.Fatalf("normal 应只走主通道：im=%d webpush=%d", len(c1.calls), len(c2.calls))
	}
}

// TestDispatcherLowGoesToInboxOnlyWhenRegistered：low 仅收件箱（未注册则不投递、仅记账）。
func TestDispatcherLowGoesToInboxOnlyWhenRegistered(t *testing.T) {
	web := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), web)
	if _, err := d.Dispatch(context.Background(), Event{Priority: PriorityLow, DedupKey: "k3", Notification: notifyTestNote("low")}); err != nil {
		t.Fatal(err)
	}
	if len(web.calls) != 0 {
		t.Fatal("low 不得命中 webpush")
	}

	inbox := &notifyTestChannel{name: "inbox"}
	d2 := NewDispatcher(NewMemoryLedger(16), inbox)
	if _, err := d2.Dispatch(context.Background(), Event{Priority: PriorityLow, DedupKey: "k4", Notification: notifyTestNote("low")}); err != nil {
		t.Fatal(err)
	}
	if len(inbox.calls) != 1 {
		t.Fatal("low 应落到收件箱通道")
	}
}

// TestDispatcherAggregatesErrorsAndRecordsReceipt：失败聚合返回；认领后不自动重试；
// 回执如实记录每通道结果。
func TestDispatcherAggregatesErrorsAndRecordsReceipt(t *testing.T) {
	bad := &notifyTestChannel{name: "bad", err: errors.New("boom")}
	ok := &notifyTestChannel{name: "ok"}
	d := NewDispatcher(NewMemoryLedger(16), bad, ok)

	r, err := d.Dispatch(context.Background(), EventFor("error", "k5", notifyTestNote("e")))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(r.Outcomes) != 2 || r.Outcomes[0].OK || !r.Outcomes[1].OK {
		t.Fatalf("回执 outcomes = %+v", r.Outcomes)
	}
	// 失败也不由分发器重试：重复键短路上一次结果。
	if _, err := d.Dispatch(context.Background(), EventFor("error", "k5", notifyTestNote("e2"))); err != nil {
		t.Fatal(err)
	}
	if len(bad.calls) != 1 || len(ok.calls) != 1 {
		t.Fatal("已认领键不得被分发器重试")
	}
	if rr, okk := d.ledger.(*MemoryLedger).Receipt("k5"); !okk || rr.Outcomes[0].Err != "boom" {
		t.Fatalf("回执未保留失败详情：%+v ok=%v", rr, okk)
	}
}

// TestEventForUsesRegistryPriority：登记表驱动优先级；未知 kind 不登记。
func TestEventForUsesRegistryPriority(t *testing.T) {
	if EventFor("approval", "k", notifyTestNote("x")).Priority != PriorityHigh {
		t.Fatal("approval 应为 high")
	}
	if EventFor("completion", "k", notifyTestNote("x")).Priority != PriorityNormal {
		t.Fatal("completion 应为 normal")
	}
	if EventFor("", "k", notifyTestNote("x")).Priority != PriorityLow {
		t.Fatal("空 kind 应为 low")
	}
	if _, ok := SpecOf("unknown-kind"); ok {
		t.Fatal("未登记 kind 不得返回登记项")
	}
	if len(KnownKinds()) == 0 {
		t.Fatal("登记表不得为空")
	}
}
