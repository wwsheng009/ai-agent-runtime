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
	// Suppressed 表示该事件因在场（人机同屏）被跳过、未投递任何通道；此时
	// DedupKey **未被认领**，查看者离开后同一事件可再次投递（补推语义，
	// docs/bots/10 §3.3）。
	Suppressed bool
	// Muted 表示该事件因静默时段（免打扰窗口，仅 low/normal）被丢弃。与
	// Suppressed 的语义差异：静默是**永久性**丢弃——宿主的去重快照已记，
	// 静默期结束后同一事件**不**补推（现网 flush 行为，docs/bots/12 §3 D3）；
	// DedupKey 同样未被认领（静默是时刻性判定，不是投递记账）。
	Muted bool
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

// Release 撤销某键的认领与回执。生命周期镜像宿主快照：attention 换值/清除时
// 宿主据此释放旧键，使"清空再挂回"等**代次事件**可重新投递——纯 (node,
// session, attention) 键不含代次，持久认领会吞掉 re-arm（现有单测
// TestPushNotifierObserve 抓住的回归，docs/bots/12 §3 D2）。未认领键忽略。
func (l *MemoryLedger) Release(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[key]; !ok {
		return
	}
	delete(l.items, key)
	for i, k := range l.order {
		if k == key {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
}
