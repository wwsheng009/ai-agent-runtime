// dispatcher.go 是通知分发器（docs/bots/schema-architecture 06 §2.4）：跨通道
// 去重（Ledger）→ 按优先级择通道（high=扇出全部通道、normal=主通道、low=仅收件箱）
// → 记录回执。策略链扩展点（免打扰/合并/在场/偏好，06 D1/D6）随后续切片接入，
// 本切片固定路由规则、不做静默/合并（宿主侧现有行为保持由各自适配层承担）。
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Channel 是通知通道（WebPush/ntfy/IM/SSE/收件箱/终端…，由宿主适配）。
type Channel interface {
	Name() string
	Deliver(ctx context.Context, n Notification) error
}

// Event 是一次通知投递请求。Priority 由调用方解析（EventFor 按登记表/PriorityOf
// 推导）；零值 = PriorityLow = 不主动投递（与 Priority 枚举语义一致）。
type Event struct {
	Kind         string
	Priority     Priority
	DedupKey     string
	Audience     string
	Notification Notification
}

// EventFor 组装请求并按登记表解析优先级（未登记回落 PriorityOf）。
func EventFor(kind, dedupKey string, n Notification) Event {
	p := PriorityOf(kind)
	if spec, ok := SpecOf(kind); ok {
		p = spec.Priority
	}
	return Event{
		Kind:         strings.TrimSpace(kind),
		Priority:     p,
		DedupKey:     strings.TrimSpace(dedupKey),
		Notification: n,
	}
}

// Dispatcher 是通道注册与分发器（单宿主一份；账本可由多消费者共享）。
type Dispatcher struct {
	ledger   Ledger
	channels []Channel
	byName   map[string]Channel
	primary  string
	clock    func() time.Time
	logf     func(string, ...any)
}

// NewDispatcher 注册通道（顺序 = 确定性扇出顺序）；ledger 可为 nil（不去重）。
func NewDispatcher(ledger Ledger, channels ...Channel) *Dispatcher {
	d := &Dispatcher{ledger: ledger, byName: make(map[string]Channel), clock: time.Now}
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		d.channels = append(d.channels, ch)
		d.byName[ch.Name()] = ch
	}
	return d
}

// SetPrimary 指定 normal 优先级的主通道（缺省 = 首个注册通道）。
func (d *Dispatcher) SetPrimary(name string) { d.primary = strings.TrimSpace(name) }

// SetLogger 注入短路/失败日志（可 nil）。
func (d *Dispatcher) SetLogger(logf func(string, ...any)) { d.logf = logf }

// SetClock 注入时钟（测试用；缺省 time.Now）。
func (d *Dispatcher) SetClock(clock func() time.Time) {
	if clock != nil {
		d.clock = clock
	}
}

// Dispatch 执行一次外发：去重短路 → 依优先级择通道投递 → 记回执。
// 重复 DedupKey 直接返回既有回执（不再投递）；空 DedupKey 不去重。
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) (Receipt, error) {
	key := strings.TrimSpace(ev.DedupKey)
	if key != "" && d.ledger != nil && !d.ledger.Claim(key) {
		if d.logf != nil {
			d.logf("notify dispatcher: dedup key %q (kind=%s), skip delivery", key, ev.Kind)
		}
		if r, ok := d.ledger.Receipt(key); ok {
			return r, nil
		}
		return Receipt{Key: key, At: d.now()}, nil
	}
	receipt := Receipt{Key: key, At: d.now()}
	var errs []error
	for _, ch := range d.targetsFor(ev.Priority) {
		err := ch.Deliver(ctx, ev.Notification)
		out := ChannelOutcome{Channel: ch.Name(), OK: err == nil}
		if err != nil {
			out.Err = err.Error()
			errs = append(errs, fmt.Errorf("channel %s: %w", ch.Name(), err))
		}
		receipt.Outcomes = append(receipt.Outcomes, out)
	}
	if key != "" && d.ledger != nil {
		d.ledger.Complete(key, receipt)
	}
	return receipt, errors.Join(errs...)
}

// targetsFor 实现 06 D6 的通道选择：high=全部通道扇出（跨通道去重保证不重复
// 打扰）；normal=主通道单发；low/零值=仅收件箱通道（未注册则不投递，仅记账）。
func (d *Dispatcher) targetsFor(p Priority) []Channel {
	switch p {
	case PriorityHigh:
		return append([]Channel(nil), d.channels...)
	case PriorityNormal:
		if d.primary != "" {
			if ch, ok := d.byName[d.primary]; ok {
				return []Channel{ch}
			}
		}
		if len(d.channels) > 0 {
			return []Channel{d.channels[0]}
		}
		return nil
	default:
		if ch, ok := d.byName["inbox"]; ok {
			return []Channel{ch}
		}
		return nil
	}
}

func (d *Dispatcher) now() time.Time {
	if d.clock != nil {
		return d.clock()
	}
	return time.Now()
}
