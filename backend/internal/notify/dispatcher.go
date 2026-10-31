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
	// EventID 是事件级稳定标识（可空）：通道交付账本用它实现
	// 「同一通道实例 + 同一 chat + 同一事件只投一次」（跨重启/多进程）。
	// 空 = 不参与通道级去重（保持既有语义）。
	EventID string
	// IMPresenceExempt 表示本次事件有 IM target 显式 ignore_presence：在场门
	// 只跳过其它通道，IM 仍投递。没有该标志时在场门按历史语义整体抑制。
	IMPresenceExempt bool
	// SkipCoalesce 让本条事件绕过合并窗口、逐条投递（docs/gateway/08 §5.1）：
	// 宿主在通知正文里带了会话专属内容（如「已完成」附带最后一条助手回复）时
	// 必须置位——Coalesce 的合并摘要只保留标题，会把正文内容丢掉。
	SkipCoalesce bool
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
	// normalChannels 是显式配置的 normal 通道列表。为空时沿用 D6 的
	// 单主通道行为；非空时按列表顺序投递（去重、忽略未注册通道）。
	normalChannels []string
	// normalFallback 是 normal 优先级的回退链（docs/gateway/18 §5.6）：主通道
	// 无收件人（ErrNoRecipient）或投递失败时按链序尝试下一通道，首个成功即止。
	// 显式 normalChannels 非空时以扇出语义为准，本字段不参与。
	normalFallback []string
	clock          func() time.Time
	logf           func(string, ...any)
	prefs          PreferenceStore
	presence       Presence
	// cfgMu 保护可在运行期热替换的策略字段（prefs/presence/quietHours，
	// 如 gateway 的通知策略热加载）。通道面/channels 在装配后固定，不纳入。
	cfgMu sync.RWMutex
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

// SetNormalChannels 指定 normal 优先级的显式通道列表。列表为空时保持
// D6 的单主通道语义；非空时按顺序投递并自动去重。未注册的通道名会被
// 忽略并记录日志，避免配置漂移导致整个通道面静默失效。
func (d *Dispatcher) SetNormalChannels(names ...string) {
	d.normalChannels = d.normalChannels[:0]
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			d.normalChannels = append(d.normalChannels, name)
		}
	}
}

// SetLogger 注入短路/失败日志（可 nil）。
func (d *Dispatcher) SetLogger(logf func(string, ...any)) { d.logf = logf }

// ErrNoRecipient 表示本次投递没有任何通道真正受理（通道无收件人）。normal
// 回退链据此判定是否继续尝试下一通道；errors.Is 可判（docs/gateway/18 §5.6）。
var ErrNoRecipient = errors.New("notify: no recipient")

// ErrAllTargetsFailed 表示回退链上的通道都被尝试过且全部失败。
var ErrAllTargetsFailed = errors.New("notify: all targets failed")

// SkippedError 表示通道**未尝试**投递（预算耗尽等），与"尝试后失败"不同：
// Dispatcher 把回执记为 skipped(reason) 而非失败，避免把"来不及发"算进错误
// 率，也让 skipped(deadline_exceeded) 可被统计（docs/gateway/18 §9）。
type SkippedError struct {
	// Reason 是原因码，取值见 SkipReason* 常量。
	Reason string
}

func (e *SkippedError) Error() string {
	if e == nil || e.Reason == "" {
		return "notify: skipped"
	}
	return "notify: skipped (" + e.Reason + ")"
}

// asSkipped 提取 SkippedError（支持被包装的情况）。
func asSkipped(err error) (*SkippedError, bool) {
	var target *SkippedError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// SetNormalFallback 指定 normal 优先级的回退链（主通道 → 备选 …）。首个成功
// 即止；显式 SetNormalChannels 非空时扇出语义优先，回退链不参与。未注册的
// 通道名告警后跳过；空参数清空回退链（回到 D6 单主通道语义）。
func (d *Dispatcher) SetNormalFallback(names ...string) {
	d.normalFallback = d.normalFallback[:0]
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			d.normalFallback = append(d.normalFallback, name)
		}
	}
}

// SetPreferences 注入偏好存储（nil = 无偏好，行为不变）。热加载安全。
func (d *Dispatcher) SetPreferences(prefs PreferenceStore) {
	d.cfgMu.Lock()
	d.prefs = prefs
	d.cfgMu.Unlock()
}

// SetQuietHours 注入免打扰窗口（空 = 不禁音）。仅作用于 low/normal。
func (d *Dispatcher) SetQuietHours(ranges []QuietRange) {
	d.cfgMu.Lock()
	d.quietHours = append([]QuietRange(nil), ranges...)
	d.cfgMu.Unlock()
}

// preferenceStore 返回当前偏好存储（热加载安全读口）。
func (d *Dispatcher) preferenceStore() PreferenceStore {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return d.prefs
}

// quietRanges 返回当前免打扰窗口副本（热加载安全读口）。
func (d *Dispatcher) quietRanges() []QuietRange {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return d.quietHours
}

// SetCoalesceWindow 注入合并窗口（0 = 不合并）。仅作用于 normal。
func (d *Dispatcher) SetCoalesceWindow(window time.Duration) {
	d.coalesceWindow = window
}

// SetCoalesceClock 注入合并/静默判定时钟（测试用；缺省与 d.clock 同源）。
func (d *Dispatcher) SetCoalesceClock(now func() time.Time) { d.coalesceNow = now }

// Muted 报告当前时刻该优先级是否落入静默窗口（high 恒 false——穿透）。
func (d *Dispatcher) Muted(p Priority) bool {
	ranges := d.quietRanges()
	if p >= PriorityHigh || len(ranges) == 0 {
		return false
	}
	return InQuietHours(ranges, d.coalesceClockNow())
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
	prefs := d.preferenceStore()

	// 回退链（docs/gateway/18 §5.6）：必须在同一个 deliver 内完成，不能递归
	// Dispatch——递归会二次认领已 Claim 的账本键，破坏去重语义。
	if chain := d.fallbackChain(ev.Priority); len(chain) > 0 {
		err := d.deliverChain(ctx, ev, chain, prefs, &receipt)
		if key != "" && d.ledger != nil {
			d.ledger.Complete(key, receipt)
		}
		return receipt, err
	}

	var errs []error
	for _, ch := range d.targetsFor(ev.Priority) {
		if prefs != nil && !prefs.Resolve(ev.Session, ch.Name()).AllowsEvent(ev.Priority, ev.Kind) {
			receipt.Outcomes = append(receipt.Outcomes, ChannelOutcome{Channel: ch.Name(), Skipped: true, Reason: SkipReasonPreference})
			continue
		}
		err := ch.Deliver(ctx, ev)
		out := ChannelOutcome{Channel: ch.Name(), OK: err == nil}
		if skipped, isSkip := asSkipped(err); isSkip {
			// 未尝试投递（预算耗尽）：记 skipped(reason)，不计入错误率。
			out.OK = false
			out.Skipped = true
			out.Reason = skipped.Reason
			if d.logf != nil {
				d.logf("notify dispatcher: channel %s skipped (%s) kind=%s", ch.Name(), skipped.Reason, ev.Kind)
			}
		} else if err != nil {
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

// fallbackChain 解析 normal 的回退链（nil = 不走回退语义）。显式
// normalChannels 非空时扇出优先，回退链不参与；未注册通道告警后跳过。
func (d *Dispatcher) fallbackChain(p Priority) []Channel {
	if p != PriorityNormal || len(d.normalFallback) == 0 {
		return nil
	}
	if len(d.normalChannels) > 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.normalFallback))
	out := make([]Channel, 0, len(d.normalFallback))
	for _, name := range d.normalFallback {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		ch, ok := d.byName[name]
		if !ok {
			if d.logf != nil {
				d.logf("notify dispatcher: fallback channel %q is not registered, ignore", name)
			}
			continue
		}
		out = append(out, ch)
	}
	return out
}

// deliverChain 按回退链逐通道尝试：首个成功即止；被偏好门跳过的通道不计入
// 尝试次数。全部尝试失败返回 ErrAllTargetsFailed，全部被跳过返回
// ErrNoRecipient——调用方据此区分"没人收"与"都没发出去"。
func (d *Dispatcher) deliverChain(ctx context.Context, ev Event, chain []Channel, prefs PreferenceStore, receipt *Receipt) error {
	var errs []error
	attempted := 0
	for _, ch := range chain {
		name := ch.Name()
		if prefs != nil && !prefs.Resolve(ev.Session, name).AllowsEvent(ev.Priority, ev.Kind) {
			receipt.Outcomes = append(receipt.Outcomes, ChannelOutcome{Channel: name, Skipped: true, Reason: SkipReasonPreference})
			continue
		}
		attempted++
		err := ch.Deliver(ctx, ev)
		out := ChannelOutcome{Channel: name, OK: err == nil}
		if err == nil {
			receipt.Outcomes = append(receipt.Outcomes, out)
			if d.logf != nil && len(errs) > 0 {
				d.logf("notify dispatcher: channel %s accepted after %d failure(s) (kind=%s)", name, len(errs), ev.Kind)
			}
			return nil
		}
		if skipped, isSkip := asSkipped(err); isSkip {
			// 预算耗尽：本轮未启动发送。记为 skipped 而非失败，并停止回退——
			// 后续通道共享同一 ctx，继续尝试只会得到同样的结论。
			out.OK = false
			out.Skipped = true
			out.Reason = skipped.Reason
			receipt.Outcomes = append(receipt.Outcomes, out)
			if d.logf != nil {
				d.logf("notify dispatcher: channel %s skipped (%s) kind=%s, stop fallback", name, skipped.Reason, ev.Kind)
			}
			return nil
		}
		out.Err = err.Error()
		receipt.Outcomes = append(receipt.Outcomes, out)
		errs = append(errs, fmt.Errorf("channel %s: %w", name, err))
		if d.logf != nil && errors.Is(err, ErrNoRecipient) {
			d.logf("notify dispatcher: channel %s has no recipient (kind=%s), try next", name, ev.Kind)
		}
	}
	if attempted == 0 {
		return ErrNoRecipient
	}
	return fmt.Errorf("%w: %w", ErrAllTargetsFailed, errors.Join(errs...))
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
		// IM target 可以显式 ignore_presence：此时只投 IM（其它通道仍受在场门
		// 约束），避免运维告警被“正在看控制台”静音。
		if ev.IMPresenceExempt {
			if ch, ok := d.byName["im"]; ok && ch != nil {
				return d.deliverIMOnly(ctx, ev, ch)
			}
		}
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
	// 合并门：SkipCoalesce 的 normal 事件逐条投递（宿主在正文里塞了会话专属
	// 内容，合并摘要只保留标题会丢内容）。
	if ev.Priority == PriorityNormal && d.coalesceWindow > 0 && !ev.SkipCoalesce {
		return d.coalesce(ev), nil
	}
	return d.deliver(ctx, ev)
}

// deliverIMOnly 是在场门下的 IM 专用投递：保留账本/偏好语义，但不走
// 其它通道与 normal 回退链（在场时 push 仍应静音）。
func (d *Dispatcher) deliverIMOnly(ctx context.Context, ev Event, ch Channel) (Receipt, error) {
	key := strings.TrimSpace(ev.DedupKey)
	if key != "" && d.ledger != nil && !d.ledger.Claim(key) {
		if d.logf != nil {
			d.logf("notify dispatcher: dedup key %q (kind=%s), skip IM delivery", key, ev.Kind)
		}
		if r, ok := d.ledger.Receipt(key); ok {
			return r, nil
		}
		return Receipt{Key: key, At: d.now()}, nil
	}
	receipt := Receipt{Key: key, At: d.now()}
	prefs := d.preferenceStore()
	if prefs != nil && !prefs.Resolve(ev.Session, ch.Name()).AllowsEvent(ev.Priority, ev.Kind) {
		receipt.Outcomes = append(receipt.Outcomes, ChannelOutcome{
			Channel: ch.Name(), Skipped: true, Reason: SkipReasonPreference,
		})
		if key != "" && d.ledger != nil {
			d.ledger.Complete(key, receipt)
		}
		return receipt, nil
	}
	err := ch.Deliver(ctx, ev)
	out := ChannelOutcome{Channel: ch.Name(), OK: err == nil}
	if skipped, isSkip := asSkipped(err); isSkip {
		out.OK = false
		out.Skipped = true
		out.Reason = skipped.Reason
	} else if err != nil {
		out.Err = err.Error()
	}
	receipt.Outcomes = append(receipt.Outcomes, out)
	if key != "" && d.ledger != nil {
		d.ledger.Complete(key, receipt)
	}
	return receipt, err
}

// targetsFor 实现通道选择：high=全部通道扇出；normal=显式列表，未配置时
// 回退 D6 单主通道；low/零值=仅收件箱通道（未注册则不投递，仅记账）。
func (d *Dispatcher) targetsFor(p Priority) []Channel {
	switch p {
	case PriorityHigh:
		return append([]Channel(nil), d.channels...)
	case PriorityNormal:
		if channels := d.configuredNormalTargets(); len(channels) > 0 {
			return channels
		}
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

// configuredNormalTargets 解析 normalChannels。空配置返回 nil，让调用方
// 回退到 D6 主通道；未注册通道逐个告警后跳过。
func (d *Dispatcher) configuredNormalTargets() []Channel {
	if len(d.normalChannels) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.normalChannels))
	out := make([]Channel, 0, len(d.normalChannels))
	for _, name := range d.normalChannels {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		ch, ok := d.byName[name]
		if !ok {
			if d.logf != nil {
				d.logf("notify dispatcher: normal channel %q is not registered, ignore", name)
			}
			continue
		}
		out = append(out, ch)
	}
	return out
}

func (d *Dispatcher) now() time.Time {
	if d.clock != nil {
		return d.clock()
	}
	return time.Now()
}
