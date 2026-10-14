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

// Preference 是单层级的偏好；指针字段 = 本层未设置（向更宽层级回落）。
type Preference struct {
	// Muted 为 true 时该层级下的投递全部静音（global 级 = 总闸；channel 级
	// 可用显式 false 解除全局静音）。
	Muted *bool
	// MinPriority 是投递下限：优先级低于它的通知在该层级被丢弃（不外发）。
	MinPriority *Priority
}

// EffectivePreference 是合并后的生效偏好（默认值已补齐）。
type EffectivePreference struct {
	Muted       bool
	MinPriority Priority
}

// Allows 报告在当前偏好下该优先级的通知是否应被投递（静音或低于门槛 → false）。
func (e EffectivePreference) Allows(p Priority) bool {
	return !e.Muted && p >= e.MinPriority
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
