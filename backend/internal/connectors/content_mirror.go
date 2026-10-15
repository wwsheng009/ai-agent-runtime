// content_mirror.go 是「会话内容出站」（订阅平面的 content 形态，
// docs/bots/13-event-subscription-routing-design.md §8 S1a）的宿主共享实现：
// 订阅会话终态正文（assistant_message）→ L3 排除 IM 来源回合（§4.2）→
// 逐 bot（policy.content_mirror）× 绑定 chats 扇出 → 宿主经 per-chat 队列
// 投递（§3.7#3，Service.EnqueueOutbound）。
//
// 单一实现点纪律（与 attention.go 同）：事件→目标匹配与去重只在本文件；
// 宿主只提供订阅源、存储与投递函数（aicli 先行，runtime-server 以同款接线
// 对齐＝S1b）。去重键为 L1+L2 合成（13 §3.6）：事件身份 × (connector, chat)，
// 保证同一事件对同一目标至多一条。
package connectors

import (
	"context"
	"strings"
	"sync"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// contentMirrorDedupeCap 是进程内目标级去重窗口（13 §3.6 L1+L2 合成键）。
const contentMirrorDedupeCap = 512

// ContentMirrorDeps 聚合宿主侧依赖；任一必需项为 nil 时整体 no-op。
type ContentMirrorDeps struct {
	// Subscribe 订阅终态正文事件（assistant_message），返回退订函数。
	Subscribe func(handler func(runtimeevents.Event)) func()
	// Store 用于反查绑定与逐 bot 的 policy.content_mirror 开关。
	Store Store
	// Deliver 把正文投递到单个目标 chat；宿主实现必须经 per-chat 队列
	//（Service.EnqueueOutbound），保证与 reply 的串行顺序（13 §3.7#3）。
	Deliver func(ctx context.Context, connectorID, chat, text string) error
	// TurnActive 报告该会话是否有 IM 来源回合进行中（13 §4.2 L3：
	// v1 整回合跳过——其正文已由回复路径投递）。nil = 恒 false（不回退过滤）。
	TurnActive func(sessionID string) bool
	// Audit 记录一次成功入队的投递（connector/chat/命中订阅 id/去重身份；宿主接
	// Service.RecordSubscriptionDelivery → ConnectorLog，13 §7.4）。可 nil。
	Audit func(connectorID, chat, subscriptionID, dedupKey string)
	// Logf 记录投递入队失败；可 nil。
	Logf func(format string, args ...any)
}

// SubscribeContentMirror 订阅 assistant_message 并按订阅平面投递正文副本。
// 返回退订函数；ctx 非 nil 时随其取消自动退订（宿主 close 语义，与
// SubscribeAttention 一致）。
func SubscribeContentMirror(ctx context.Context, deps ContentMirrorDeps) func() {
	if deps.Subscribe == nil || deps.Store == nil || deps.Deliver == nil {
		return func() {}
	}
	deliverCtx := ctx
	if deliverCtx == nil {
		deliverCtx = context.Background()
	}
	dedupe := newAttentionDeduper(contentMirrorDedupeCap)
	unsubscribe := deps.Subscribe(func(event runtimeevents.Event) {
		if event.Type != runtimechat.EventAssistantMessage && event.Type != "assistant.message" {
			return
		}
		sessionID := strings.TrimSpace(event.SessionID)
		if sessionID == "" {
			return
		}
		text := attentionEventPayloadString(event, "content")
		if text == "" {
			// 空正文（reasoning-only 回合）：无副本可投。
			return
		}
		// L3（13 §4.2）：IM 来源回合的正文已由回复路径投递给发起 chat。
		// 事件在 SubmitPrompt 返回前同步发布（events.Bus.Publish 同步派发），
		// Handle 的登记窗口必然覆盖本回调。
		if deps.TurnActive != nil && deps.TurnActive(sessionID) {
			return
		}
		identity := attentionIdentity(attentionEventPayloadString(event, "turn_id"), event)
		subEvent, _ := subscriptionEventFor(event.Type)
		for _, cfg := range deps.Store.ListConfigs() {
			if !cfg.Enabled {
				continue
			}
			// 订阅求解（S2，13 §3.5）：events 已配置走声明式订阅（content 形态），
			// 否则回退 legacy content_mirror；命中后目标=该会话的绑定 chats。
			bindings := deps.Store.ListBindings(cfg.ID)
			sub, matched := subscriptionContentMatch(cfg.Policy, subEvent, sessionID, sessionBoundLookup(bindings))
			if !matched {
				continue
			}
			for _, binding := range bindings {
				if strings.TrimSpace(binding.Session.SessionID) != sessionID {
					continue
				}
				if strings.TrimSpace(binding.Chat) == "" {
					continue
				}
				key := strings.Join([]string{"content", sessionID, cfg.ID, binding.Chat, identity}, "|")
				if !dedupe.first(key) {
					continue
				}
				if err := deps.Deliver(deliverCtx, cfg.ID, binding.Chat, text); err != nil {
					// 入队失败：释放认领——同一事件后续重试仍可投递（罕见路径）。
					dedupe.release(key)
					// 指标（13 §7.4）：失败按目标连接器计数（sent 只在成功路径）。
					recordConnectorDelivery(cfg.ID, DeliveryFormContent, deliveryOutcomeFailed)
					if deps.Logf != nil {
						deps.Logf("connector %s: content mirror enqueue failed (chat=%s): %v", cfg.ID, binding.Chat, err)
					}
					continue
				}
				// S4 审计（13 §7.4）：内容可见性 = 显式订阅 + 可追溯。
				if deps.Audit != nil {
					deps.Audit(cfg.ID, binding.Chat, sub.ID, identity)
				}
			}
		}
	})
	if unsubscribe == nil {
		return func() {}
	}
	var unsubOnce sync.Once
	unsub := func() { unsubOnce.Do(unsubscribe) }
	if ctx == nil {
		return unsub
	}
	go func() {
		<-ctx.Done()
		unsub()
	}()
	return unsub
}
