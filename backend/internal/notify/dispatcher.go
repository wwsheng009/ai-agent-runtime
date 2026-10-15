// dispatcher.go 是通知分发器（docs/bots/schema-architecture 06 §2.4）：在场门
// （Presence，docs/bots/10 §3.2）→ 跨通道去重（Ledger）→ 按优先级择通道
// （high=扇出全部通道、normal=主通道、low=仅收件箱）→ 通道偏好（prefs）→
// 记录回执。免打扰/合并（06 D1/D6）随后续切片接入：静默与合并仍由宿主侧
// 适配层承担（如 gateway pushNotifier 的 quietHours/coalesceWindow）。
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Channel 是通知通道（WebPush/ntfy/IM/SSE/收件箱/终端…，由宿主适配）。
// Deliver 接收完整 Event：除信封外还带 Kind/去重键/寻址提示（如 IM 通道用
// Target 定位会话），通道实现忽略自身不需要的字段。
type Channel interface {
	Name() string
	Deliver(ctx context.Context, ev Event) error
}

// Event 是一次通知投递请求。Priority 由调用方解析（EventFor 按登记表/PriorityOf
// 推导）；零值 = PriorityLow = 不主动投递（与 Priority 枚举语义一致）。
type Event struct {
	Kind     string
	Priority Priority
	DedupKey string
	Audience string
	// Target 是通道专用寻址提示（可选）：IM 通道用它定位会话
	// （SessionRef.SessionID）；WebPush/ntfy 忽略。
	Target string
	// Node 是事件归属宿主节点（可选）：在场门（docs/bots/10 §3.2）的定位键，
	// 与 Session 同时存在时才参与在场判定。
	Node string
	// Session 是事件归属会话（可选）：偏好解析（session:<id>）用；IM 通道的
	// 投递寻址仍用 Target（两者通常相同，但语义分层）。
	Session      string
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
	prefs    PreferenceStore
	presence Presence
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

// SetPreferences 注入偏好存储（nil = 无偏好，行为不变）。
func (d *Dispatcher) SetPreferences(prefs PreferenceStore) { d.prefs = prefs }

// SetClock 注入时钟（测试用；缺省 time.Now）。
func (d *Dispatcher) SetClock(clock func() time.Time) {
	if clock != nil {
		d.clock = clock
	}
}

// Dispatch 执行一次外发：去重短路 → 依优先级择通道投递 → 记回执。
// 重复 DedupKey 直接返回既有回执（不再投递）；空 DedupKey 不去重。
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) (Receipt, error) {
	// 在场门（docs/bots/10 §3.3）：人机同屏时跳过投递，且**不认领去重键**
	// ——查看者离开后同一事件可再次投递（"切走补推一次"）。门禁必须早于
	// 认领，否则去重会吞掉补推。
	if d.presenceActive(ev) {
		if d.logf != nil {
			d.logf("notify dispatcher: presence active for %s/%s (kind=%s), skip delivery",
				strings.TrimSpace(ev.Node), strings.TrimSpace(ev.Session), ev.Kind)
		}
		return Receipt{Key: strings.TrimSpace(ev.DedupKey), At: d.now(), Suppressed: true}, nil
	}
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
		if d.prefs != nil && !d.prefs.Resolve(ev.Session, ch.Name()).Allows(ev.Priority) {
			receipt.Outcomes = append(receipt.Outcomes, ChannelOutcome{Channel: ch.Name(), Skipped: true})
			continue
		}
		err := ch.Deliver(ctx, ev)
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
