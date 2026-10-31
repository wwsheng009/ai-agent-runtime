package notify

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// deadlineChannel 模拟"预算耗尽、未启动发送"的通道。
type deadlineChannel struct{ name string }

func (c *deadlineChannel) Name() string { return c.name }

func (c *deadlineChannel) Deliver(context.Context, Event) error {
	return &SkippedError{Reason: SkipReasonDeadlineExceeded}
}

// TestDispatcherSkippedDeadlineIsNotFailure 锁定 §9 指标语义：预算耗尽记
// skipped(deadline_exceeded)，不计入错误率，也不触发回退（同 ctx 下继续尝试
// 只会得到同样结论）。
func TestDispatcherSkippedDeadlineIsNotFailure(t *testing.T) {
	im := &deadlineChannel{name: "im"}
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")

	r, err := d.Dispatch(context.Background(), fallbackEvent())
	if err != nil {
		t.Fatalf("skipped 不应作为错误返回：%v", err)
	}
	if got := r.SkippedByReason(SkipReasonDeadlineExceeded); got != 1 {
		t.Fatalf("应记录 1 次 deadline_exceeded，实际 %d：%+v", got, r.Outcomes)
	}
	if push.calls() != 0 {
		t.Fatal("预算耗尽后不应继续回退到后续通道")
	}
}

// TestDispatcherSkippedPreferenceReason：偏好跳过带 preference 原因码，
// 与 deadline 可区分（两者都是 Skipped，但原因不同）。
func TestDispatcherSkippedPreferenceReason(t *testing.T) {
	im := &fbChannel{name: "im"}
	d := NewDispatcher(NewMemoryLedger(16), im)
	d.SetPrimary("im")
	d.SetPreferences(denyAllPrefs{})
	// 走回退链语义：全链被偏好跳过时 deliverChain 返回 ErrNoRecipient。
	d.SetNormalFallback("im")

	r, err := d.Dispatch(context.Background(), fallbackEvent())
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("全被跳过应返回 ErrNoRecipient，实际 %v", err)
	}
	if got := r.SkippedByReason(SkipReasonPreference); got != 1 {
		t.Fatalf("应记录 1 次 preference 跳过，实际 %d", got)
	}
	if got := r.SkippedByReason(SkipReasonDeadlineExceeded); got != 0 {
		t.Fatalf("不应误记 deadline_exceeded：%d", got)
	}
	if r.Skipped() != 1 {
		t.Fatalf("跳过总数为 1，实际 %d", r.Skipped())
	}
}

// TestSkippedErrorIsMatchable：SkippedError 可被 errors.As 提取（含包装）。
func TestSkippedErrorIsMatchable(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", &SkippedError{Reason: SkipReasonDeadlineExceeded})
	got, ok := asSkipped(wrapped)
	if !ok || got.Reason != SkipReasonDeadlineExceeded {
		t.Fatalf("应能提取被包装的 SkippedError：%v", wrapped)
	}
	if (&SkippedError{}).Error() == "" {
		t.Fatal("空原因也应有可读文案")
	}
}
