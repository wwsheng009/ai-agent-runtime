package notify

import (
	"context"
	"testing"
	"time"
)

// TestDispatcherSkipCoalesceDeliversImmediately 锁定 docs/gateway/08 §5.7 的
// SkipCoalesce 语义：宿主在通知正文里塞了会话专属内容（「已完成」附带最后一条
// 助手回复）时，事件必须绕过合并窗口逐条投递——Coalesce 的合并摘要只保留标题，
// 会把正文内容丢掉，用户看到的就是"功能时灵时不灵"。
func TestDispatcherSkipCoalesceDeliversImmediately(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(nil, ch)
	d.SetPrimary("webpush") // 无回退链：确定性地走 targetFor 投递
	d.SetCoalesceWindow(40 * time.Millisecond)

	// 带内容的 normal 事件：立即投递，不进合并批。
	plain := EventFor("completed", "k1", notifyTestNote("s1"))
	plain.SkipCoalesce = true
	plain.Notification.Body = "s1: completed\n最后一条助手回复"
	if _, err := d.Dispatch(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("SkipCoalesce 事件应立刻投递，calls=%d", len(ch.calls))
	}
	if got := ch.calls[0].Body; got != "s1: completed\n最后一条助手回复" {
		t.Fatalf("正文内容被改写：%q", got)
	}
	// 立刻可见：窗口到期后不应再补一条。
	time.Sleep(60 * time.Millisecond)
	if len(ch.calls) != 1 {
		t.Fatalf("窗口到期后不应重复投递，calls=%d", len(ch.calls))
	}

	// 对照：不带 SkipCoalesce 的同 kind 事件仍走合并（窗口内攒批）。
	legacy := EventFor("completed", "k2", notifyTestNote("s2"))
	if _, err := d.Dispatch(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("合并窗口内的 normal 事件不应立即投递，calls=%d", len(ch.calls))
	}
	time.Sleep(80 * time.Millisecond)
	if len(ch.calls) != 2 {
		t.Fatalf("窗口到期应 flush 一条，calls=%d", len(ch.calls))
	}
}
