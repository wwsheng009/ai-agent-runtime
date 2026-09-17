package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// P0-3b trigger_turn 消费闭环（docs/plan/
// supervision-parent-child-control-optimization-plan-20260917.md §3.3 改动 2）。
//
// 子会话 run 结束时 drain 自己的 mailbox：把 trigger_turn=true 且尚未消费的
// 指令按投递顺序合并成一个 prompt，经 SubmitPromptAsync 提交一次新 turn。
// 消费标记写在 session events（agent.trigger_turn.consumed）里，drain 前先读
// 最近一段事件恢复高水位，因此重复调用零副作用、崩溃重启不会重复触发；提交
// 失败不写标记，下次 run 结束/resume 会重试。
const (
	// EventTriggerTurnConsumed 记录一次 drain 已消费的 trigger_turn 消息；
	// 它同时是幂等账本的持久化标记，父侧 read_agent_events 可见。
	EventTriggerTurnConsumed = "agent.trigger_turn.consumed"
	// EventTriggerTurnDropped 记录被限流/环保护拒绝的 drain，指令留在
	// mailbox（不静默丢弃），由父侧 digest 提示。
	EventTriggerTurnDropped = "agent.trigger_turn.dropped"
	// EventMailboxDelivery 是单发指令投递审计事件（P0-3a 改动 4），写在
	// 发起方会话事件流里，供 read_mailbox_digest / /debug 观察。
	EventMailboxDelivery = "mailbox_delivery"

	// TriggerTurnDrainMinInterval 是同一 child 自动触发 turn 的间隔下限
	// （方案建议 30s，§7.1 风险 2/9）。
	TriggerTurnDrainMinInterval = 30 * time.Second
	// TriggerTurnDrainMaxConsecutive 是同一 child 连续自动触发 turn 的上限；
	// 超限降级为 mailbox 留痕 + dropped 事件（防父子互触发成环）。
	TriggerTurnDrainMaxConsecutive = 3

	triggerTurnMailboxScanLimit        = 200
	triggerTurnConsumedEventScanLimit  = 1000
	triggerTurnPromptPerMessageRuneCap = 4000
)

// MailboxMessageRequestsTriggerTurn reports whether a mailbox message carries
// the trigger_turn=true flag written by a busy followup_task /
// send_input(interrupt=false) delivery.
func MailboxMessageRequestsTriggerTurn(message team.MailMessage) bool {
	if message.Metadata == nil {
		return false
	}
	value, ok := message.Metadata["trigger_turn"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

// eventsBeforeLister is the bounded recent-events reader implemented by
// SQLiteRuntimeStore. The drain uses it to recover the most recent consumed
// markers without replaying a whole session event log.
type eventsBeforeLister interface {
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]runtimeevents.Event, error)
}

// drainTriggerTurnMailbox consumes pending trigger_turn mailbox messages for
// this session at most once per run end. It is best-effort: any failure leaves
// the messages unconsumed so the next run end (or resume) retries.
func (a *SessionActor) drainTriggerTurnMailbox() {
	if a == nil || !a.triggerTurnDrain {
		return
	}
	if !a.triggerTurnDrainInFlight.CompareAndSwap(false, true) {
		return
	}
	defer a.triggerTurnDrainInFlight.Store(false)
	if a.IsStopped() {
		return
	}
	// A successor run already owns the session: leave the messages for its run
	// end instead of racing it with a second turn.
	if state, ok := a.StateSummary(); ok && state.Busy() {
		return
	}
	ctx := context.Background()
	consumedSeq, consumedIDs, err := a.triggerTurnConsumedLedger(ctx)
	if err != nil {
		return
	}
	messages, supported, err := a.listTriggerTurnMailboxMessages(ctx, consumedSeq)
	if err != nil || !supported {
		return
	}
	pending := selectTriggerTurnMessages(messages, consumedIDs, consumedSeq)
	if len(pending) == 0 {
		return
	}
	now := time.Now().UTC()
	a.triggerTurnMu.Lock()
	lastAutoAt := a.triggerTurnLastAutoAt
	consecutive := a.triggerTurnConsecutive
	a.triggerTurnMu.Unlock()
	reason := ""
	switch {
	case consecutive >= TriggerTurnDrainMaxConsecutive:
		reason = "consecutive_limit"
	case !lastAutoAt.IsZero() && now.Sub(lastAutoAt) < TriggerTurnDrainMinInterval:
		reason = "min_interval"
	}
	if reason != "" {
		a.publishTriggerTurnDropped(reason, pending, consecutive, now)
		return
	}
	prompt := buildTriggerTurnPrompt(pending)
	var runMeta *team.RunMeta
	if a.triggerTurnRunMeta != nil {
		if session, loadErr := a.loadSession(ctx); loadErr == nil {
			runMeta = a.triggerTurnRunMeta(ctx, session)
		}
	}
	if err := a.SubmitPromptAsync(ctx, prompt, runMeta, SubmitPromptOption{TriggerTurnAuto: true}); err != nil {
		a.publishTriggerTurnDropped("submit_failed: "+err.Error(), pending, consecutive, now)
		return
	}
	a.triggerTurnMu.Lock()
	a.triggerTurnLastAutoAt = now
	a.triggerTurnConsecutive = consecutive + 1
	a.triggerTurnMu.Unlock()
	a.publishTriggerTurnConsumed(pending, consecutive+1, len(prompt))
}

// listTriggerTurnMailboxMessages reads the child's durable mailbox rows in
// delivery order. It deliberately uses the session mailbox seq reader
// (MailboxReaderStore.ListMailbox) instead of ListMailboxAgentControlFirst:
// the latter's AgentControl source filters by its own record id, which is a
// different sequence, while the drain's high-water mark is a session mailbox
// seq. A store without ListMailbox falls back to the control reader with
// afterSeq=0 plus per-message id/seq filtering.
func (a *SessionActor) listTriggerTurnMailboxMessages(ctx context.Context, consumedSeq int64) ([]team.MailMessage, bool, error) {
	if a == nil {
		return nil, false, nil
	}
	for _, store := range []interface{}{a.stateStore, a.eventStore} {
		if store == nil {
			continue
		}
		if reader, ok := store.(MailboxReaderStore); ok && reader != nil {
			messages, err := reader.ListMailbox(ctx, a.id, consumedSeq, triggerTurnMailboxScanLimit)
			return messages, true, err
		}
	}
	if reader, ok := a.stateStore.(AgentControlMailboxReaderStore); ok && reader != nil {
		messages, err := reader.ListAgentControlMailbox(ctx, a.id, 0, triggerTurnMailboxScanLimit)
		return messages, true, err
	}
	return nil, false, nil
}

// triggerTurnConsumedLedger returns the mailbox high-water mark and message ids
// already consumed by earlier drains. Both are recovered from the durable
// consumed events, so a restarted actor does not re-trigger an old instruction.
func (a *SessionActor) triggerTurnConsumedLedger(ctx context.Context) (int64, map[string]struct{}, error) {
	ids := map[string]struct{}{}
	if a == nil || a.eventStore == nil {
		return 0, ids, nil
	}
	var events []runtimeevents.Event
	if reader, ok := a.eventStore.(eventsBeforeLister); ok {
		recent, err := reader.ListEventsBefore(ctx, a.id, int64(^uint64(0)>>1), triggerTurnConsumedEventScanLimit)
		if err != nil {
			return 0, nil, err
		}
		events = recent
	} else {
		listed, err := a.eventStore.ListEvents(ctx, a.id, 0, triggerTurnConsumedEventScanLimit)
		if err != nil {
			return 0, nil, err
		}
		events = listed
	}
	highWater := int64(0)
	for _, event := range events {
		if event.Type != EventTriggerTurnConsumed || event.Payload == nil {
			continue
		}
		if seq := payloadInt64Value(event.Payload["mailbox_seq"]); seq > highWater {
			highWater = seq
		}
		for _, id := range payloadStringList(event.Payload["message_ids"]) {
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	return highWater, ids, nil
}

// selectTriggerTurnMessages keeps trigger_turn rows that are not already
// consumed and preserves mailbox delivery order.
func selectTriggerTurnMessages(messages []team.MailMessage, consumed map[string]struct{}, consumedSeq int64) []team.MailMessage {
	pending := make([]team.MailMessage, 0, len(messages))
	for _, message := range messages {
		if !MailboxMessageRequestsTriggerTurn(message) {
			continue
		}
		if id := strings.TrimSpace(message.ID); id != "" {
			if _, ok := consumed[id]; ok {
				continue
			}
		}
		if seq := message.SessionMailboxSeq; seq > 0 && consumedSeq > 0 && seq <= consumedSeq {
			continue
		}
		if strings.TrimSpace(message.Body) == "" {
			continue
		}
		pending = append(pending, message)
	}
	return pending
}

// buildTriggerTurnPrompt merges pending trigger messages into one prompt,
// annotating each source so the child can tell multiple senders apart.
func buildTriggerTurnPrompt(messages []team.MailMessage) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[trigger_turn] %d instruction(s) were delivered while this session was busy. Consume them in delivery order.\n", len(messages))
	for index, message := range messages {
		fmt.Fprintf(&builder, "\n--- instruction %d/%d ---\n", index+1, len(messages))
		from := strings.TrimSpace(message.FromAgent)
		if from == "" {
			from = "unknown"
		}
		fmt.Fprintf(&builder, "from: %s\n", from)
		if id := strings.TrimSpace(message.ID); id != "" {
			fmt.Fprintf(&builder, "message_id: %s\n", id)
		}
		if kind := strings.TrimSpace(message.Kind); kind != "" {
			fmt.Fprintf(&builder, "kind: %s\n", kind)
		}
		body := strings.TrimSpace(message.Body)
		if runes := []rune(body); len(runes) > triggerTurnPromptPerMessageRuneCap {
			body = string(runes[:triggerTurnPromptPerMessageRuneCap]) + "…[truncated]"
		}
		builder.WriteString(body)
		builder.WriteString("\n")
	}
	return builder.String()
}

// publishTriggerTurnConsumed writes the durable idempotency marker and the
// collaboration event the parent can read via read_agent_events.
func (a *SessionActor) publishTriggerTurnConsumed(messages []team.MailMessage, consecutive, promptChars int) {
	ids, highWater := triggerTurnMessageIdentity(messages)
	a.publish(runtimeevents.Event{
		Type:      EventTriggerTurnConsumed,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"message_ids":  ids,
			"mailbox_seq":  highWater,
			"count":        len(messages),
			"prompt_chars": promptChars,
			"consecutive":  consecutive,
			"source":       "trigger_turn_drain",
		},
	})
}

// publishTriggerTurnDropped records a rate-limited / loop-guarded drain. The
// messages stay in the mailbox (nothing is silently dropped) and the event is
// throttled so repeated run ends cannot flood the parent event stream.
func (a *SessionActor) publishTriggerTurnDropped(reason string, messages []team.MailMessage, consecutive int, now time.Time) {
	ids, highWater := triggerTurnMessageIdentity(messages)
	key := reason + "|" + strings.Join(ids, ",")
	a.triggerTurnMu.Lock()
	if key == a.triggerTurnLastDroppedKey && !a.triggerTurnLastDroppedAt.IsZero() &&
		now.Sub(a.triggerTurnLastDroppedAt) < TriggerTurnDrainMinInterval {
		a.triggerTurnMu.Unlock()
		return
	}
	a.triggerTurnLastDroppedKey = key
	a.triggerTurnLastDroppedAt = now
	a.triggerTurnMu.Unlock()
	a.publish(runtimeevents.Event{
		Type:      EventTriggerTurnDropped,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"reason":      reason,
			"message_ids": ids,
			"mailbox_seq": highWater,
			"count":       len(messages),
			"consecutive": consecutive,
			"source":      "trigger_turn_drain",
		},
	})
}

func triggerTurnMessageIdentity(messages []team.MailMessage) ([]string, int64) {
	ids := make([]string, 0, len(messages))
	highWater := int64(0)
	for _, message := range messages {
		if id := strings.TrimSpace(message.ID); id != "" {
			ids = append(ids, id)
		}
		if seq := message.SessionMailboxSeq; seq > highWater {
			highWater = seq
		} else if seq := message.Seq; seq > highWater {
			highWater = seq
		}
	}
	return ids, highWater
}

// noteRunTriggerOrigin resets the auto-trigger loop guard for runs that were not
// started by the drain. The drain's own runs carry autoTrigger=true and keep
// the consecutive counter so the ≤3 limit counts real consecutive auto turns.
func (a *SessionActor) noteRunTriggerOrigin(autoTrigger bool) {
	if a == nil || !a.triggerTurnDrain || autoTrigger {
		return
	}
	a.triggerTurnMu.Lock()
	a.triggerTurnConsecutive = 0
	a.triggerTurnLastAutoAt = time.Time{}
	a.triggerTurnMu.Unlock()
}

func payloadInt64Value(value interface{}) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	default:
		return 0
	}
}

func payloadStringList(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, strings.TrimSpace(item))
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, strings.TrimSpace(fmt.Sprint(item)))
		}
		return out
	default:
		return nil
	}
}
