// 已发消息账本：记录机器人成功发送的消息 id 与时间，供撤回使用。
//
// 边界：
//   - 进程内、按 chat 保留最近 N 条（重启丢失；官方撤回窗口仅 2 分钟，
//     重启后即便记住 id 也大概率已过期）；
//   - 只记录普通消息/富媒体消息（msg_type 0/2/7）的响应 id；
//     流式消息的 stream_msg_id 是另一套 ID，不进入本账本；
//   - 发送响应缺 id（异常/老版本）时跳过记录，不报错。
package qqbot

import (
	"strings"
	"sync"
	"time"
)

// sentLedgerCapacity 是每个 chat 保留的最近消息条数（撤回只关心最近几条）。
const sentLedgerCapacity = 8

// recallWindow 是官方撤回时限（发送后 2 分钟内可撤回）。
const recallWindow = 2 * time.Minute

// sentMessageEntry 是一条已发消息。
type sentMessageEntry struct {
	MessageID string
	SentAt    time.Time
}

// sentLedger 是按 chat 的最近已发消息环形账本。
type sentLedger struct {
	mu      sync.Mutex
	entries map[string][]sentMessageEntry
}

// record 记录一条已发消息（同 id 去重；超出容量丢最旧）。
func (l *sentLedger) record(chat, messageID string, at time.Time) {
	chat = strings.TrimSpace(chat)
	messageID = strings.TrimSpace(messageID)
	if l == nil || chat == "" || messageID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string][]sentMessageEntry)
	}
	list := l.entries[chat]
	for _, item := range list {
		if item.MessageID == messageID {
			return
		}
	}
	list = append(list, sentMessageEntry{MessageID: messageID, SentAt: at})
	if len(list) > sentLedgerCapacity {
		list = list[len(list)-sentLedgerCapacity:]
	}
	l.entries[chat] = list
}

// lastRecallable 返回该 chat 最近一条仍在撤回窗口内的消息。
func (l *sentLedger) lastRecallable(chat string, now time.Time) (sentMessageEntry, bool) {
	if l == nil {
		return sentMessageEntry{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.entries[strings.TrimSpace(chat)]
	for i := len(list) - 1; i >= 0; i-- {
		entry := list[i]
		if now.Sub(entry.SentAt) <= recallWindow {
			return entry, true
		}
	}
	return sentMessageEntry{}, false
}

// remove 删除一条已记录消息（撤回成功后调用）。
func (l *sentLedger) remove(chat, messageID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		return
	}
	key := strings.TrimSpace(chat)
	list, exists := l.entries[key]
	if !exists {
		return
	}
	out := list[:0]
	for _, item := range list {
		if item.MessageID != messageID {
			out = append(out, item)
		}
	}
	l.entries[key] = out
}
