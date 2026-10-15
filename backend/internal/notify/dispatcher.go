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
	"sync"
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
	// quietHours/coalesceWindow 是免打扰与合并策略（docs/bots/12 §3 D3）：
	// 静默窗口内 low/normal 事件被永久丢弃（回执 Muted，不认领键——宿主
	// 去重快照已记，静默期后不补推）；合并窗口把 normal 事件按 attention
	// 攒批延后投递。high 类两者皆不适用（绕过合并与免打扰）。
	quietHours     []QuietRange
	coalesceWindow time.Duration
	coalesceNow    func() time.Time
	muCoalesce     sync.Mutex
	coalesceBatch  map[string][]Event
	coalesceTimer  *time.Timer
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

// SetQuietHours 注入免打扰窗口（空 = 不禁音）。仅作用于 low/normal。
func (d *Dispatcher) SetQuietHours(ranges []QuietRange) { d.quietHours = ranges }

// SetCoalesceWindow 注入合并窗口（0 = 不合并）。仅作用于 normal。
func (d *Dispatcher) SetCoalesceWindow(window time.Duration) {
	d.coalesceWindow = window
}

// SetCoalesceClock 注入合并/静默判定时钟（测试用；缺省与 d.clock 同源）。
func (d *Dispatcher) SetCoalesceClock(now func() time.Time) { d.coalesceNow = now }

// Muted 报告当前时刻该优先级是否落入静默窗口（high 恒 false——穿透）。
func (d *Dispatcher) Muted(p Priority) bool {
	if p >= PriorityHigh || len(d.quietHours) == 0 {
		return false
	}
	return InQuietHours(d.quietHours, d.coalesceClockNow())
}

func (d *Dispatcher) coalesceClockNow() time.Time {
	if d.coalesceNow != nil {
		return d.coalesceNow()
	}
	return d.now()
}

// coalesce 把 normal 事件攒入按 attention 分组的批（docs/bots/12 §3 D3），
// 首事件起定时器；flushCoalesced 在窗口到期时统一投递。攒批期间不认领
// 去重键（flush 时由账本短路兜底重复）。
func (d *Dispatcher) coalesce(ev Event) Receipt {
	d.muCoalesce.Lock()
	if d.coalesceBatch == nil {
		d.coalesceBatch = make(map[string][]Event)
	}
	d.coalesceBatch[ev.Kind] = append(d.coalesceBatch[ev.Kind], ev)
	if d.coalesceTimer == nil {
		d.coalesceTimer = time.AfterFunc(d.coalesceWindow, d.flushCoalesced)
	}
	d.muCoalesce.Unlock()
	return Receipt{Key: strings.TrimSpace(ev.DedupKey), At: d.now()}
}

// flushCoalesced 投出攒批的合并通知（受静默判定约束）。仅供 Dispatcher
// 内部定时器调用；宿主不再自持批处理。
func (d *Dispatcher) flushCoalesced() {
	d.muCoalesce.Lock()
	batch := d.coalesceBatch
	d.coalesceBatch = nil
	d.coalesceTimer = nil
	quiet := d.Muted(PriorityNormal)
	d.muCoalesce.Unlock()
	if len(batch) == 0 || quiet {
		return
	}
	out := make([]Event, 0, len(batch))
	for attention, events := range batch {
		notes := make([]Notification, 0, len(events))
		for _, ev := range events {
			notes = append(notes, ev.Notification)
		}
		note := Coalesce(attention, notes)
		first := events[0]
		first.Notification = note
		out = append(out, first)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ev := range out {
		if _, err := d.deliver(ctx, ev); err != nil {
			if d.logf != nil {
				d.logf("notify dispatcher: coalesced flush failed (kind=%s): %v", ev.Kind, err)
			}
		}
	}
}

// deliver 是账本认领 → 偏好门 → 择通道投递 → 记回执的内部投递段；不带
// 在场/静默/合并门（那三门只存在于对外 Dispatch 主路径——flush 产物与
// 攒批产物不再二次判定，避免递归攒批与重复静默丢弃）。
func (d *Dispatcher) deliver(ctx context.Context, ev Event) (Receipt, error) {
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

// SetLedger 注入跨路径去重账本（构造后注入用；nil = 不去重）。
// 仅应在通道装配阶段调用（与 NewDispatcher 的 ledger 参数等价）。
func (d *Dispatcher) SetLedger(l Ledger) { d.ledger = l }

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
	// 静默门（docs/bots/12 §3 D3）：免打扰窗口内的 low/normal 事件被永久
	// 丢弃——回执 Muted、不认领键（静默是时刻性判定；宿主去重快照在调用
	// 前已记，静默期后不补推）。high 类穿透。
	if ev.Priority < PriorityHigh && d.Muted(ev.Priority) {
		if d.logf != nil {
			d.logf("notify dispatcher: quiet hours active (kind=%s priority=%s), drop", ev.Kind, ev.Priority)
		}
		return Receipt{Key: strings.TrimSpace(ev.DedupKey), At: d.now(), Muted: true}, nil
	}
	// 合并门（docs/bots/12 §3 D3）：normal 事件在窗口内按 attention 攒批，
	// 定时器统一 flush。攒批期间键暂不认领（flush 时若已被其他路径投出，
	// 由账本短路兜底）。high 不攒批；low 无需攒批（收件箱即时）。
	if ev.Priority == PriorityNormal && d.coalesceWindow > 0 {
		return d.coalesce(ev), nil
	}
	return d.deliver(ctx, ev)
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
