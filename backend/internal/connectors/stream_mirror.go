// stream_mirror.go 是「会话内容出站」的 stream 形态（订阅平面，docs/bots/13
// §3.3/§7.1 S4）的宿主共享实现：订阅 assistant 增量（assistant_delta）→ 命中
// `deliver=stream` 订阅 → 逐绑定 chat 发占位消息并节流编辑（≥1s，打字机）；
// 终态正文（assistant_message）到达后以全文做最终编辑。
//
// 语义与边界（13 §3.7/§4.5/§7.1）：
//   - L3 与 content 镜像同：IM 来源回合整回合跳过（正文由回复路径投递）；
//   - 不放大：同一目标 chat 在一次回合内只开一条流，编辑节流合并（§3.7#2）；
//   - 降级：provider 不支持流式（未实现 StreamingProvider）或占位启动失败 →
//     退化为一次性投递（终态文本经 per-chat 队列，仍按 stream 订阅审计）；
//   - 终态不单独触发投递：没有增量就没有流（stream 仅 delta 类），避免与
//     content 形态隐式双发；需要「保底正文」时显式另配 content 订阅；
//   - 去重：与回复路径的 delta 订阅同口径——依赖总线在回合内不重复派发
//     （会话内回合由 actor 串行化）；不做逐增量身份去重（多增量共享 turn id）。
package connectors

import (
	"context"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// streamMirrorInterval 是流式订阅编辑的最小间隔（13 §3.7#2：≥1s 节流合并）。
const streamMirrorInterval = time.Second

// streamMirrorQueueSize 是镜像内部命令队列容量；满则丢弃并记日志（不静默吞）。
const streamMirrorQueueSize = 256

// StreamMirrorDeps 聚合宿主侧依赖；Subscribe/Store/Start 任一为 nil 时整体 no-op。
type StreamMirrorDeps struct {
	// Subscribe 订阅会话输出事件（assistant_delta 增量 + assistant_message 终态），
	// 返回退订函数。
	Subscribe func(handler func(runtimeevents.Event)) func()
	// Store 用于反查绑定与逐 bot 的订阅配置。
	Store Store
	// TurnActive 报告该会话是否有 IM 来源回合进行中（L3，13 §4.2，与 content 镜像同）。
	TurnActive func(sessionID string) bool
	// Start 开始一条流式投递（发占位消息），失败/不支持时调用方退化一次性投递。
	// 生产实现 Service.NewSubscriptionStream。
	Start func(ctx context.Context, connectorID, chat string) (StreamingSession, error)
	// Deliver 是退化路径的一次性投递（宿主必须经 per-chat 队列，
	// Service.EnqueueOutbound，13 §3.7#3）。可 nil（永久禁用退化投递）。
	Deliver func(ctx context.Context, connectorID, chat, text string) error
	// Audit 记录一次成功投递（connector/chat/命中订阅 id/去重身份/是否降级；
	// 宿主接 Service.RecordStreamDelivery，13 §7.4）。可 nil。
	Audit func(connectorID, chat, subscriptionID, dedupKey string, degraded bool)
	// Interval 是编辑节流最小间隔；<=0 用默认 1s（测试可缩短）。
	Interval time.Duration
	// Logf 启动失败/编辑失败/队列满日志；可 nil。
	Logf func(format string, args ...any)
}

// streamMirrorEvent 是镜像内部命令（delta=true 增量；false 终态正文）。
type streamMirrorEvent struct {
	delta     bool
	sessionID string
	text      string
	identity  string
}

// streamMirrorTarget 是一条进行中的流式投递（键 = connector + chat）。
type streamMirrorTarget struct {
	connectorID    string
	chat           string
	subscriptionID string
	dedupKey       string
	sessionID      string
	// session 为 nil = 退化路径（provider 不支持流式/占位启动失败，等终态一次性投递）。
	session  StreamingSession
	throttle *deltaThrottle
	text     string
}

// streamMirror 持有单 worker 的活动流表（map 只在 worker 协程内读写，无锁）。
type streamMirror struct {
	ctx    context.Context
	deps   StreamMirrorDeps
	active map[string]*streamMirrorTarget
}

// SubscribeStreamMirror 订阅会话输出并按 stream 形态投递。返回退订函数；
// ctx 取消（宿主 close）时自动退订并尽力收尾在途流。
func SubscribeStreamMirror(ctx context.Context, deps StreamMirrorDeps) func() {
	if deps.Subscribe == nil || deps.Store == nil || deps.Start == nil {
		return func() {}
	}
	mirrorCtx := ctx
	if mirrorCtx == nil {
		mirrorCtx = context.Background()
	}
	mirror := &streamMirror{ctx: mirrorCtx, deps: deps, active: make(map[string]*streamMirrorTarget)}
	queue := make(chan streamMirrorEvent, streamMirrorQueueSize)
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorker := func() { stopOnce.Do(func() { close(stop) }) }

	go func() {
		for {
			select {
			case <-mirrorCtx.Done():
				mirror.finalizeAll()
				return
			case <-stop:
				mirror.finalizeAll()
				return
			case ev := <-queue:
				if ev.delta {
					mirror.handleDelta(ev)
				} else {
					mirror.handleMessage(ev)
				}
			}
		}
	}()

	unsubscribe := deps.Subscribe(func(event runtimeevents.Event) {
		ev, ok := streamMirrorEventFor(event)
		if !ok {
			return
		}
		// L3（13 §4.2）：IM 来源回合的正文已由回复路径投递（含流式）。
		if deps.TurnActive != nil && deps.TurnActive(ev.sessionID) {
			return
		}
		select {
		case queue <- ev:
		default:
			if deps.Logf != nil {
				kind := "delta"
				if !ev.delta {
					kind = "message"
				}
				deps.Logf("connector stream mirror queue full, dropping %s event (session=%s)", kind, ev.sessionID)
			}
		}
	})
	if unsubscribe == nil {
		return func() { stopWorker() }
	}
	var unsubOnce sync.Once
	unsub := func() {
		unsubOnce.Do(func() {
			unsubscribe()
			stopWorker()
		})
	}
	go func() {
		select {
		case <-mirrorCtx.Done():
			unsub()
		case <-stop:
		}
	}()
	return unsub
}

// streamMirrorEventFor 归一化事件（增量 vs 终态，13 §3.3 词表；点分隔别名同 bus 口径）。
func streamMirrorEventFor(event runtimeevents.Event) (streamMirrorEvent, bool) {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return streamMirrorEvent{}, false
	}
	switch event.Type {
	case runtimechat.EventAssistantDelta, "assistant.delta":
		text := deltaStreamPayloadText(event.Payload)
		if text == "" {
			return streamMirrorEvent{}, false
		}
		return streamMirrorEvent{
			delta: true, sessionID: sessionID, text: text,
			identity: attentionIdentity(attentionEventPayloadString(event, "turn_id"), event),
		}, true
	case runtimechat.EventAssistantMessage, "assistant.message":
		text := attentionEventPayloadString(event, "content")
		if text == "" {
			return streamMirrorEvent{}, false
		}
		return streamMirrorEvent{
			sessionID: sessionID, text: text,
			identity: attentionIdentity(attentionEventPayloadString(event, "turn_id"), event),
		}, true
	}
	return streamMirrorEvent{}, false
}

// handleDelta 把增量应用到命中的目标 chat（每事件逐 bot 独立求值，13 §3.5）。
func (m *streamMirror) handleDelta(ev streamMirrorEvent) {
	subEvent, ok := subscriptionEventFor(runtimechat.EventAssistantDelta)
	if !ok {
		return
	}
	for _, cfg := range m.deps.Store.ListConfigs() {
		if !cfg.Enabled {
			continue
		}
		bindings := m.deps.Store.ListBindings(cfg.ID)
		sub, matched := subscriptionStreamMatch(cfg.Policy, subEvent, ev.sessionID, sessionBoundLookup(bindings))
		if !matched {
			continue
		}
		for _, binding := range bindings {
			if strings.TrimSpace(binding.Session.SessionID) != ev.sessionID {
				continue
			}
			chat := strings.TrimSpace(binding.Chat)
			if chat == "" {
				continue
			}
			m.applyDelta(cfg.ID, chat, ev, sub.ID)
		}
	}
}

// applyDelta 累积增量并在节流窗口放行一次编辑；首次命中时开流（占位消息）。
func (m *streamMirror) applyDelta(connectorID, chat string, ev streamMirrorEvent, subscriptionID string) {
	key := connectorID + "\x00" + chat
	target := m.active[key]
	if target != nil && target.sessionID != ev.sessionID {
		// 会话切换（上一回合未收到终态）：先尽力收尾旧流，再开新流。
		m.finalize(m.ctx, key, "")
		target = nil
	}
	if target == nil {
		target = &streamMirrorTarget{
			connectorID: connectorID, chat: chat,
			subscriptionID: subscriptionID, sessionID: ev.sessionID,
			dedupKey: ev.identity, throttle: newDeltaThrottle(m.interval()),
		}
		session, err := m.deps.Start(m.ctx, connectorID, chat)
		switch {
		case err != nil:
			if m.deps.Logf != nil {
				m.deps.Logf("connector %s: stream placeholder start failed (chat=%s, fallback to one-shot content): %v", connectorID, chat, err)
			}
		case session == nil:
			if m.deps.Logf != nil {
				m.deps.Logf("connector %s: stream placeholder unavailable (chat=%s, fallback to one-shot content)", connectorID, chat)
			}
		default:
			target.session = session
		}
		m.active[key] = target
	}
	target.text += ev.text
	if target.session == nil {
		return // 退化：等终态一次性投递。
	}
	if !target.throttle.shouldEmit(target.text) {
		return
	}
	if err := target.session.Update(m.ctx, target.text); err != nil && m.deps.Logf != nil {
		m.deps.Logf("connector %s: stream edit failed (chat=%s): %v", connectorID, chat, err)
	}
}

// handleMessage 用终态正文收尾该会话的全部在途流（多绑定 chat 各自收尾）。
func (m *streamMirror) handleMessage(ev streamMirrorEvent) {
	for key, target := range m.active {
		if target.sessionID == ev.sessionID {
			m.finalize(m.ctx, key, ev.text)
		}
	}
}

// finalize 收尾一条流：finalText 非空 = 终态正文（权威），空 = 用增量累积文本。
// 成功投递才审计（13 §7.4）；失败按 stream 形态计 failed 指标。
func (m *streamMirror) finalize(ctx context.Context, key, finalText string) {
	target := m.active[key]
	if target == nil {
		return
	}
	delete(m.active, key)
	text := strings.TrimSpace(finalText)
	if text == "" {
		text = strings.TrimSpace(target.text)
	}
	if text == "" {
		return
	}
	degraded := target.session == nil
	if target.session != nil {
		if err := target.session.Final(ctx, []OutboundMessage{{Text: text}}); err != nil {
			recordConnectorDelivery(target.connectorID, DeliveryFormStream, deliveryOutcomeFailed)
			if m.deps.Logf != nil {
				m.deps.Logf("connector %s: stream final edit failed (chat=%s): %v", target.connectorID, target.chat, err)
			}
			return
		}
	} else {
		if m.deps.Deliver == nil {
			return
		}
		if err := m.deps.Deliver(ctx, target.connectorID, target.chat, text); err != nil {
			recordConnectorDelivery(target.connectorID, DeliveryFormStream, deliveryOutcomeFailed)
			if m.deps.Logf != nil {
				m.deps.Logf("connector %s: stream degraded delivery failed (chat=%s): %v", target.connectorID, target.chat, err)
			}
			return
		}
	}
	if m.deps.Audit != nil {
		m.deps.Audit(target.connectorID, target.chat, target.subscriptionID, target.dedupKey, degraded)
	}
}

// finalizeAll 尽力收尾全部在途流（退订/宿主 close；发送用独立超时上下文，
// 不随已取消的宿主 ctx 立即失败）。
func (m *streamMirror) finalizeAll() {
	if len(m.active) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for key := range m.active {
		m.finalize(ctx, key, "")
	}
}

func (m *streamMirror) interval() time.Duration {
	if m.deps.Interval > 0 {
		return m.deps.Interval
	}
	return streamMirrorInterval
}
