package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fbChannel 是可按次返回指定错误的测试通道（回退链用例专用）。
type fbChannel struct {
	name string
	err  error
	mu   sync.Mutex
	call int
}

func (c *fbChannel) Name() string { return c.name }

func (c *fbChannel) Deliver(_ context.Context, _ Event) error {
	c.mu.Lock()
	c.call++
	c.mu.Unlock()
	return c.err
}

func (c *fbChannel) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call
}

// outcomeOf 按通道名取回执条目。
func outcomeOf(r Receipt, name string) (ChannelOutcome, bool) {
	for _, o := range r.Outcomes {
		if o.Channel == name {
			return o, true
		}
	}
	return ChannelOutcome{}, false
}

func fallbackEvent() Event {
	return EventFor("completion", "fb-1", Notification{Title: "t", Body: "b"})
}

// TestDispatcherFallbackOnNoRecipient 锁定 docs/gateway/18 §5.6 的核心语义：
// 主通道无收件人时按链序改走下一通道，且只在同一 deliver 内完成。
func TestDispatcherFallbackOnNoRecipient(t *testing.T) {
	im := &fbChannel{name: "im", err: ErrNoRecipient}
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")

	r, err := d.Dispatch(context.Background(), fallbackEvent())
	if err != nil {
		t.Fatalf("回退成功后不应报错：%v", err)
	}
	if im.calls() != 1 || push.calls() != 1 {
		t.Fatalf("应先试 im 再回退 webpush：im=%d push=%d", im.calls(), push.calls())
	}
	if o, ok := outcomeOf(r, "im"); !ok || o.OK {
		t.Fatalf("im 的回执应为失败：%+v", r.Outcomes)
	}
	if o, ok := outcomeOf(r, "webpush"); !ok || !o.OK {
		t.Fatalf("webpush 的回执应为成功：%+v", r.Outcomes)
	}
}

// TestDispatcherFallbackStopsAtFirstSuccess：首个成功即止，不再打扰后续通道。
func TestDispatcherFallbackStopsAtFirstSuccess(t *testing.T) {
	im := &fbChannel{name: "im"}
	push := &fbChannel{name: "webpush"}
	ntfy := &fbChannel{name: "ntfy"}
	d := NewDispatcher(NewMemoryLedger(16), im, push, ntfy)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush", "ntfy")

	if _, err := d.Dispatch(context.Background(), fallbackEvent()); err != nil {
		t.Fatal(err)
	}
	if im.calls() != 1 {
		t.Fatalf("im 应被调用一次：%d", im.calls())
	}
	if push.calls() != 0 || ntfy.calls() != 0 {
		t.Fatalf("成功后不得继续扇出：push=%d ntfy=%d", push.calls(), ntfy.calls())
	}
}

// TestDispatcherFallbackAllFailed：全链尝试失败返回 ErrAllTargetsFailed。
func TestDispatcherFallbackAllFailed(t *testing.T) {
	sendErr := errors.New("boom")
	im := &fbChannel{name: "im", err: ErrNoRecipient}
	push := &fbChannel{name: "webpush", err: sendErr}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")

	_, err := d.Dispatch(context.Background(), fallbackEvent())
	if !errors.Is(err, ErrAllTargetsFailed) {
		t.Fatalf("应返回 ErrAllTargetsFailed，实际 %v", err)
	}
	if im.calls() != 1 || push.calls() != 1 {
		t.Fatalf("两个通道都应被尝试：im=%d push=%d", im.calls(), push.calls())
	}
}

// TestDispatcherFallbackDoesNotApplyToHigh：high 仍扇出全部通道，不走回退链。
func TestDispatcherFallbackDoesNotApplyToHigh(t *testing.T) {
	im := &fbChannel{name: "im", err: ErrNoRecipient}
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")

	if _, err := d.Dispatch(context.Background(), EventFor("approval", "fb-high", Notification{Title: "t"})); err == nil {
		t.Fatal("high 扇出中 im 失败应报告错误")
	}
	if push.calls() != 1 {
		t.Fatalf("high 应扇出全部通道：push=%d", push.calls())
	}
}

// TestDispatcherFallbackIgnoredWhenNormalChannelsSet：显式 normalChannels 的
// 扇出语义优先，回退链不参与。
func TestDispatcherFallbackIgnoredWhenNormalChannelsSet(t *testing.T) {
	im := &fbChannel{name: "im", err: ErrNoRecipient}
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")
	d.SetNormalChannels("im", "webpush")

	if _, err := d.Dispatch(context.Background(), fallbackEvent()); err == nil {
		t.Fatal("显式扇出下 im 的失败应被报告")
	}
	if im.calls() != 1 || push.calls() != 1 {
		t.Fatalf("显式列表应两项都投：im=%d push=%d", im.calls(), push.calls())
	}
}

// TestDispatcherFallbackSkipsUnregisteredChannel：未注册通道告警后跳过，不 panic。
func TestDispatcherFallbackSkipsUnregisteredChannel(t *testing.T) {
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), push)
	d.SetPrimary("webpush")
	d.SetNormalFallback("ghost", "webpush")

	var logged []string
	d.SetLogger(func(format string, args ...any) { logged = append(logged, format) })

	if _, err := d.Dispatch(context.Background(), fallbackEvent()); err != nil {
		t.Fatalf("未注册通道应被跳过而非失败：%v", err)
	}
	if push.calls() != 1 {
		t.Fatalf("应回退到 webpush：%d", push.calls())
	}
	if len(logged) == 0 {
		t.Fatal("跳过未注册通道应记录日志")
	}
}

// TestDispatcherFallbackAllSkippedReturnsNoRecipient：全链都被偏好门跳过时
// 返回 ErrNoRecipient（区分"没人收"与"都没发出去"）。
func TestDispatcherFallbackAllSkippedReturnsNoRecipient(t *testing.T) {
	im := &fbChannel{name: "im"}
	push := &fbChannel{name: "webpush"}
	d := NewDispatcher(NewMemoryLedger(16), im, push)
	d.SetPrimary("im")
	d.SetNormalFallback("im", "webpush")
	d.SetPreferences(denyAllPrefs{})

	_, err := d.Dispatch(context.Background(), fallbackEvent())
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("应返回 ErrNoRecipient，实际 %v", err)
	}
	if im.calls() != 0 || push.calls() != 0 {
		t.Fatalf("被跳过的通道不应被调用：im=%d push=%d", im.calls(), push.calls())
	}
}

// denyAllPrefs 拒绝一切事件的偏好源（Muted=true → AllowsEvent 恒 false）。
type denyAllPrefs struct{}

func (denyAllPrefs) Set(Scope, Preference) {}

func (denyAllPrefs) Get(Scope) (Preference, bool) { return Preference{}, false }

func (denyAllPrefs) Resolve(string, string) EffectivePreference {
	return EffectivePreference{Muted: true}
}

// TestDispatcherErrNoRecipientIsComparable 锁定 errors.Is 契约（§5.6 要求
// 实现者能判别该错误，而不是比对字符串）。
func TestDispatcherErrNoRecipientIsComparable(t *testing.T) {
	wrapped := errors.Join(errors.New("x"), ErrNoRecipient)
	if !errors.Is(wrapped, ErrNoRecipient) {
		t.Fatal("ErrNoRecipient 必须可被 errors.Is 判定")
	}
	if !strings.Contains(ErrNoRecipient.Error(), "recipient") {
		t.Fatalf("错误文案应可读：%s", ErrNoRecipient)
	}
	// 回退链的全失败错误必须可被判为 ErrAllTargetsFailed，同时保留底层原因。
	var base = errors.New("boom")
	all := fmt.Errorf("%w: %w", ErrAllTargetsFailed, base)
	if !errors.Is(all, ErrAllTargetsFailed) || !errors.Is(all, base) {
		t.Fatalf("ErrAllTargetsFailed 包装应同时保留两层原因：%v", all)
	}
}
