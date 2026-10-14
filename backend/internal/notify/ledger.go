// ledger.go 是跨通道投递账本（docs/bots/schema-architecture 06 §2.4 的 Delivery
// Ledger）：同一 DedupKey 跨通道只投一次，投递后记录回执。本切片给出内存实现
// （有界 FIFO 淘汰、进程内幂等）；持久化/跨进程共享随宿主切流切片接入（gateway
// 现有 push dedup 快照届时迁移到本账本）。
package notify

import (
	"strings"
	"sync"
	"time"
)

// ChannelOutcome 是单通道投递结果（回执条目）。
type ChannelOutcome struct {
	Channel string
	OK      bool
	Err     string
	// Skipped 表示该通道被偏好（静音/优先级门槛）跳过，未尝试投递。
	Skipped bool
}

// Receipt 是一次投递的回执（按 DedupKey 记账）。
type Receipt struct {
	Key      string
	At       time.Time
	Outcomes []ChannelOutcome
}

// Ledger 是投递账本契约。
//
// Claim 是跨通道去重的原子判定点：语义 = "Seen + 登记" 的合并（check-then-act
// 存在竞态，故选"认领"原语），首次返回 true、重复返回 false；空键表示不去重。
// Complete 记录回执；Receipt 供观测/测试回读。注意：认领后即使投递失败也不由
// 本层自动重试（"不再重复打扰"）；重试语义由各通道自身负责。
type Ledger interface {
	Claim(key string) bool
	Complete(key string, receipt Receipt)
	Receipt(key string) (Receipt, bool)
}

// MemoryLedger 是有界内存账本（容量满时淘汰最旧键，仿事件级去重窗口）。
type MemoryLedger struct {
	mu    sync.Mutex
	cap   int
	order []string
	items map[string]Receipt
}

// NewMemoryLedger 创建内存账本；capacity<=0 时取默认 1024。
func NewMemoryLedger(capacity int) *MemoryLedger {
	if capacity <= 0 {
		capacity = 1024
	}
	return &MemoryLedger{cap: capacity, items: make(map[string]Receipt, capacity)}
}

// Claim 见 Ledger 契约（空键恒 true = 不去重）。
func (l *MemoryLedger) Claim(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[key]; ok {
		return false
	}
	l.items[key] = Receipt{Key: key}
	l.order = append(l.order, key)
	for len(l.order) > l.cap {
		delete(l.items, l.order[0])
		l.order = l.order[1:]
	}
	return true
}

// Complete 见 Ledger 契约：只对**已认领**的键记回执（未认领/已淘汰键忽略，
// 避免账本被未认领写入撑大）。
func (l *MemoryLedger) Complete(key string, receipt Receipt) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[key]; !ok {
		return
	}
	receipt.Key = key
	l.items[key] = receipt
}

// Receipt 见 Ledger 契约。
func (l *MemoryLedger) Receipt(key string) (Receipt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.items[strings.TrimSpace(key)]
	return r, ok
}
