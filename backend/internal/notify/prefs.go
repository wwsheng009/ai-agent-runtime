// prefs.go 是通知偏好三级存储（docs/bots/06-target-architecture.md §1 D6）：
// global / session:<id> / channel:<name>，更具体的层级**字段级**覆盖更宽泛的层级
// （指针字段 = 本层未设置）。当前提供两个旋钮：静音（Muted）与优先级门槛
// （MinPriority）。默认（无任何配置）= 不静音、门槛 low，不改变既有路由行为。
package notify

import (
	"strings"
	"sync"
)

// Scope 是偏好层级键（global | session:<id> | channel:<name>）。
type Scope string

// GlobalScope 返回全局层级键（总闸）。
func GlobalScope() Scope { return "global" }

// SessionScope 返回会话层级键。
func SessionScope(sessionID string) Scope { return Scope("session:" + strings.TrimSpace(sessionID)) }

// ChannelScope 返回通道层级键。
func ChannelScope(channel string) Scope { return Scope("channel:" + strings.TrimSpace(channel)) }

// KindSet 是 kind 白名单集合。nil = 未设置（不限）；非 nil（含空集）=
// 显式覆盖：只有集合内 kind 允许外发。别名（completion/completed）在
// 构建与查询时归一化，避免词表双写导致策略静默失效。
type KindSet map[string]struct{}

// normalizeKind 归一化登记表中的同义词（kinds.go 同时登记了 gateway 旧词
// completed 与内核词 completion）。
func normalizeKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "completion":
		return "completed"
	default:
		return strings.TrimSpace(kind)
	}
}

// NewKindSet 从 kind 列表构建集合；空列表返回非 nil 空集（= 全禁）。
func NewKindSet(kinds []string) KindSet {
	set := make(KindSet, len(kinds))
	for _, kind := range kinds {
		if kind = normalizeKind(kind); kind != "" {
			set[kind] = struct{}{}
		}
	}
	return set
}

// Contains 报告 kind 是否在集合内（nil 集合 = 全允许）。
func (k KindSet) Contains(kind string) bool {
	if k == nil {
		return true
	}
	_, ok := k[normalizeKind(kind)]
	return ok
}

// Preference 是单层级的偏好；指针字段 = 本层未设置（向更宽层级回落）。
type Preference struct {
	// Muted 为 true 时该层级下的投递全部静音（global 级 = 总闸；channel 级
	// 可用显式 false 解除全局静音）。
	Muted *bool
	// MinPriority 是投递下限：优先级低于它的通知在该层级被丢弃（不外发）。
	MinPriority *Priority
	// AllowKinds 是 kind 白名单（nil = 本层未设置，沿用更宽层级；非 nil 的
	// 空集 = 本层显式禁止全部 kind）。与 Muted/MinPriority 一样按字段级覆盖。
	AllowKinds KindSet
}

// EffectivePreference 是合并后的生效偏好（默认值已补齐）。
type EffectivePreference struct {
	Muted       bool
	MinPriority Priority
	// AllowKinds 为 nil 表示不限 kind。
	AllowKinds KindSet
}

// Allows 报告在当前偏好下该优先级的通知是否应被投递（静音或低于门槛 → false）。
func (e EffectivePreference) Allows(p Priority) bool {
	return !e.Muted && p >= e.MinPriority
}

// AllowsKind 报告在当前偏好下该 kind 是否允许外发。
func (e EffectivePreference) AllowsKind(kind string) bool {
	return e.AllowKinds.Contains(kind)
}

// AllowsEvent 同时校验优先级门槛与 kind 白名单（Dispatcher 通道级门控）。
func (e EffectivePreference) AllowsEvent(p Priority, kind string) bool {
	return e.Allows(p) && e.AllowsKind(kind)
}

// PreferenceStore 提供三级偏好的读写与合并解析。
type PreferenceStore interface {
	Set(scope Scope, pref Preference)
	Get(scope Scope) (Preference, bool)
	// Resolve 合并 global → session:<id> → channel:<name>（空 id/name 跳过对应层）。
	Resolve(sessionID, channel string) EffectivePreference
}

// MemoryPreferenceStore 是线程安全的内存实现；持久化由宿主负责（gateway 见
// <data-dir>/notify-prefs.json 装载）。
type MemoryPreferenceStore struct {
	mu    sync.RWMutex
	items map[Scope]Preference
}

// NewMemoryPreferenceStore 创建空偏好存储（全默认 = 不改变行为）。
func NewMemoryPreferenceStore() *MemoryPreferenceStore {
	return &MemoryPreferenceStore{items: make(map[Scope]Preference)}
}

// Set 写入某层级偏好（覆盖式）。
func (s *MemoryPreferenceStore) Set(scope Scope, pref Preference) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[Scope]Preference)
	}
	s.items[scope] = pref
}

// Get 读取某层级偏好。
func (s *MemoryPreferenceStore) Get(scope Scope) (Preference, bool) {
	if s == nil {
		return Preference{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pref, ok := s.items[scope]
	return pref, ok
}

// Resolve 见 PreferenceStore 契约。
func (s *MemoryPreferenceStore) Resolve(sessionID, channel string) EffectivePreference {
	eff := EffectivePreference{Muted: false, MinPriority: PriorityLow}
	apply := func(scope Scope) {
		pref, ok := s.Get(scope)
		if !ok {
			return
		}
		if pref.Muted != nil {
			eff.Muted = *pref.Muted
		}
		if pref.MinPriority != nil {
			eff.MinPriority = *pref.MinPriority
		}
		if pref.AllowKinds != nil {
			eff.AllowKinds = pref.AllowKinds
		}
	}
	apply(GlobalScope())
	if strings.TrimSpace(sessionID) != "" {
		apply(SessionScope(sessionID))
	}
	if strings.TrimSpace(channel) != "" {
		apply(ChannelScope(channel))
	}
	return eff
}

// Replace 原子替换全部层级偏好（热加载/持久化回写用；nil 或空 = 清空为默认）。
func (s *MemoryPreferenceStore) Replace(items map[Scope]Preference) {
	if s == nil {
		return
	}
	next := make(map[Scope]Preference, len(items))
	for scope, pref := range items {
		next[scope] = pref
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = next
}

// Snapshot 返回全部层级偏好的副本（旧 notify-prefs.json 迁移/展示用）。
func (s *MemoryPreferenceStore) Snapshot() map[Scope]Preference {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[Scope]Preference, len(s.items))
	for scope, pref := range s.items {
		out[scope] = pref
	}
	return out
}
