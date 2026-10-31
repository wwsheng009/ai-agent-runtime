package main

// notify_delivery_ledger.go — 通道级交付账本（P1，docs/gateway/18 架构治理）。
//
// 目标：同一个「通道实例 + chat + 事件」只投一次。键由 channels.IM 构造：
//
//	gateway target:  <connectors.ChannelKey>|<chat>|<eventID>
//	node target:     node:<node_id>|*|<eventID>
//
// eventID 含网关 bootID + 会话代次（push.go），所以：
//   - 重启后不会与旧条目碰撞（旧条目按 TTL 淘汰）；
//   - 同一 attention 清除后重新进入会得到新代次，不会被误吞。
//
// 落盘格式（<data-dir>/notify-delivery.json，0600）：
//
//	{"entries":{"key":<unix-nano>}}
//
// 写穿（每次 Claim/Release 原子写）：通知频率低，文件小；换来重启后的
// 幂等保证。写失败只降级为内存去重并记日志，不阻塞投递。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// notifyDeliveryTTL 是账本条目保留时间（超过后可重新投递）。
	notifyDeliveryTTL = 24 * time.Hour
	// notifyDeliveryCap 是账本条目上限（超出淘汰最旧）。
	notifyDeliveryCap = 4096
)

type notifyDeliveryLedger struct {
	mu      sync.Mutex
	path    string
	ttl     time.Duration
	max     int
	entries map[string]int64 // key -> unix nano
}

type notifyDeliveryFile struct {
	Entries map[string]int64 `json:"entries"`
}

// newNotifyDeliveryLedger 构造并装载账本（文件缺失 = 空账本）。
func newNotifyDeliveryLedger(path string, ttl time.Duration, max int) *notifyDeliveryLedger {
	if ttl <= 0 {
		ttl = notifyDeliveryTTL
	}
	if max <= 0 {
		max = notifyDeliveryCap
	}
	l := &notifyDeliveryLedger{path: path, ttl: ttl, max: max, entries: make(map[string]int64)}
	l.load()
	return l
}

func (l *notifyDeliveryLedger) load() {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("notify ledger: read %s: %v", l.path, err)
		}
		return
	}
	var file notifyDeliveryFile
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("notify ledger: parse %s: %v", l.path, err)
		return
	}
	now := time.Now()
	for key, ts := range file.Entries {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if now.Sub(time.Unix(0, ts)) <= l.ttl {
			l.entries[key] = ts
		}
	}
	l.pruneLocked(now)
}

// Claim 认领一个投递键；false = 已有未过期条目（应跳过）。
func (l *notifyDeliveryLedger) Claim(key string) bool {
	if l == nil {
		return true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if ts, ok := l.entries[key]; ok && now.Sub(time.Unix(0, ts)) <= l.ttl {
		return false
	}
	l.entries[key] = now.UnixNano()
	l.pruneLocked(now)
	l.persistLocked()
	return true
}

// Release 撤销认领（发送失败时调用，允许后续重试）。
func (l *notifyDeliveryLedger) Release(key string) {
	if l == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[key]; !ok {
		return
	}
	delete(l.entries, key)
	l.persistLocked()
}

// Close 落盘并释放（进程退出时调用）。
func (l *notifyDeliveryLedger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.persistLocked()
}

func (l *notifyDeliveryLedger) pruneLocked(now time.Time) {
	for key, ts := range l.entries {
		if now.Sub(time.Unix(0, ts)) > l.ttl {
			delete(l.entries, key)
		}
	}
	if len(l.entries) <= l.max {
		return
	}
	type kv struct {
		key string
		ts  int64
	}
	list := make([]kv, 0, len(l.entries))
	for key, ts := range l.entries {
		list = append(list, kv{key: key, ts: ts})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ts < list[j].ts })
	for i := 0; i < len(list) && len(l.entries) > l.max; i++ {
		delete(l.entries, list[i].key)
	}
}

func (l *notifyDeliveryLedger) persistLocked() error {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return nil
	}
	raw, err := json.Marshal(notifyDeliveryFile{Entries: l.entries})
	if err != nil {
		return err
	}
	if dir := filepath.Dir(l.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("notify ledger: mkdir %s: %v", dir, err)
			return err
		}
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("notify ledger: write %s: %v", tmp, err)
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		// Windows 不允许 rename 覆盖：先删目标再重试（与 notify-im 同口径）。
		_ = os.Remove(l.path)
		if err2 := os.Rename(tmp, l.path); err2 != nil {
			_ = os.Remove(tmp)
			log.Printf("notify ledger: rename %s: %v", l.path, err2)
			return err2
		}
	}
	return nil
}
