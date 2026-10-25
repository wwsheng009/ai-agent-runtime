package main

// notify_policy.go 是网关通知策略的持久化与热加载层（docs/gateway/08-
// notifications.md §10）：<data-dir>/notify-policy.json 是唯一写入口，
// 旧 <data-dir>/notify-prefs.json 仅作只读回退（首次 PUT 后不再读取）。
//
// 模型（与控制台「通知设置」页一一对应）：
//
//	{
//	  "version": 1,
//	  "global":   {"muted": false, "min_priority": "normal",
//	               "allow_kinds": ["approval","question","error"],
//	               "quiet_hours": ["22:00-08:00"]},
//	  "sessions": {"s-123": {"muted": true, "min_priority": "high"}},
//	  "channels": {"ntfy": {"muted": true}}
//	}
//
// 语义沿用 notify.Preference 的字段级覆盖：global → session:<id> →
// channel:<name>，更具体层级覆盖更宽层级；allow_kinds 为显式覆盖（空数组
// = 一个 kind 都不允许）。quiet_hours 仅支持 global（指向网关外发与
// App 端过滤的统一免打扰时段），写入到 session/channel 会被拒绝，避免
// 看起来生效但实际被忽略的配置。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

const notifyPolicyVersion = 1

// notifyPolicyFile 是 <data-dir>/notify-policy.json 的磁盘形状。
type notifyPolicyFile struct {
	Version  int                          `json:"version,omitempty"`
	Global   notifyPolicyScope            `json:"global,omitempty"`
	Sessions map[string]notifyPolicyScope `json:"sessions,omitempty"`
	Channels map[string]notifyPolicyScope `json:"channels,omitempty"`
}

// notifyPolicyScope 是单层级的策略旋钮。全部字段可空 = 本层未设置。
type notifyPolicyScope struct {
	Muted       *bool     `json:"muted,omitempty"`
	MinPriority string    `json:"min_priority,omitempty"`
	AllowKinds  *[]string `json:"allow_kinds,omitempty"`
	QuietHours  *[]string `json:"quiet_hours,omitempty"`
}

// notifyPolicyValidationError 区分 400（策略内容非法）与 500（写盘失败）。
type notifyPolicyValidationError struct{ msg string }

func (e *notifyPolicyValidationError) Error() string { return e.msg }

func isNotifyPolicyValidationError(err error) bool {
	var target *notifyPolicyValidationError
	return errors.As(err, &target)
}

// buildNotifyPolicyStore 校验并编译策略为 notify 偏好存储。
// 返回值 quietSet 表示 global.quiet_hours 是否显式给出（显式空数组 = 清空
// --notify-quiet-hours 的窗口，因此需要与"未设置"区分）。
func buildNotifyPolicyStore(file notifyPolicyFile) (*notify.MemoryPreferenceStore, []notify.QuietRange, bool, error) {
	store := notify.NewMemoryPreferenceStore()
	add := func(scope notify.Scope, item notifyPolicyScope, where string) error {
		if item.QuietHours != nil && where != "global" {
			return fmt.Errorf("%s: quiet_hours is only supported on the global scope", where)
		}
		pref := notify.Preference{Muted: item.Muted}
		if mp := strings.TrimSpace(item.MinPriority); mp != "" {
			p, err := parseNotifyPriority(mp)
			if err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			pref.MinPriority = &p
		}
		if item.AllowKinds != nil {
			kinds := make([]string, 0, len(*item.AllowKinds))
			for _, kind := range *item.AllowKinds {
				kind = strings.TrimSpace(kind)
				if kind == "" {
					return fmt.Errorf("%s: allow_kinds contains an empty kind", where)
				}
				kinds = append(kinds, kind)
			}
			pref.AllowKinds = notify.NewKindSet(kinds)
		}
		store.Set(scope, pref)
		return nil
	}

	if err := add(notify.GlobalScope(), file.Global, "global"); err != nil {
		return nil, nil, false, err
	}
	var quiet []notify.QuietRange
	quietSet := false
	if file.Global.QuietHours != nil {
		ranges, err := notify.ParseQuietHours(strings.Join(*file.Global.QuietHours, ","))
		if err != nil {
			return nil, nil, false, fmt.Errorf("global.quiet_hours: %w", err)
		}
		quiet, quietSet = ranges, true
	}
	for id, item := range file.Sessions {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, nil, false, fmt.Errorf("sessions contains an empty session id")
		}
		if err := add(notify.SessionScope(id), item, "session:"+id); err != nil {
			return nil, nil, false, err
		}
	}
	for name, item := range file.Channels {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil, false, fmt.Errorf("channels contains an empty channel name")
		}
		if err := add(notify.ChannelScope(name), item, "channel:"+name); err != nil {
			return nil, nil, false, err
		}
	}
	return store, quiet, quietSet, nil
}

// policyViewFromLegacy 把旧 notify-prefs.json 的三级偏好转换为策略视图，
// 使 GET /api/notify/policy 首次就展示可编辑、可保存的等价配置。
func policyViewFromLegacy(store *notify.MemoryPreferenceStore) notifyPolicyFile {
	file := notifyPolicyFile{Version: notifyPolicyVersion}
	if store == nil {
		return file
	}
	for scope, pref := range store.Snapshot() {
		raw := string(scope)
		converted := policyScopeFromPreference(pref)
		switch {
		case raw == "global":
			file.Global = converted
		case strings.HasPrefix(raw, "session:"):
			if file.Sessions == nil {
				file.Sessions = make(map[string]notifyPolicyScope)
			}
			file.Sessions[strings.TrimPrefix(raw, "session:")] = converted
		case strings.HasPrefix(raw, "channel:"):
			if file.Channels == nil {
				file.Channels = make(map[string]notifyPolicyScope)
			}
			file.Channels[strings.TrimPrefix(raw, "channel:")] = converted
		}
	}
	return file
}

func policyScopeFromPreference(pref notify.Preference) notifyPolicyScope {
	out := notifyPolicyScope{Muted: pref.Muted}
	if pref.MinPriority != nil {
		out.MinPriority = pref.MinPriority.String()
	}
	if pref.AllowKinds != nil {
		kinds := make([]string, 0, len(pref.AllowKinds))
		for kind := range pref.AllowKinds {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		out.AllowKinds = &kinds
	}
	return out
} // notifyPolicySource 是策略文件的加载/保存/热加载源。并发安全：gateway 的
// observe/API/热加载可能从不同 goroutine 到达。任何时刻对外暴露的是最后
// 一次成功解析的策略；非法文件不会清空既有策略（只记录 lastErr 并告警），
// 避免手滑编辑把线上通知全部放行或全部静默。
type notifyPolicySource struct {
	mu         sync.Mutex
	path       string
	legacyPath string
	stamp      string
	loaded     bool
	legacy     bool
	source     string
	view       notifyPolicyFile
	prefs      *notify.MemoryPreferenceStore
	quiet      []notify.QuietRange
	quietSet   bool
	lastErr    error
}

func newNotifyPolicySource(path, legacyPath string) *notifyPolicySource {
	return &notifyPolicySource{path: path, legacyPath: legacyPath}
}

// Path 返回策略文件路径（API 展示用）。
func (s *notifyPolicySource) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// LegacyPath 返回旧偏好文件路径（API 展示用）。
func (s *notifyPolicySource) LegacyPath() string {
	if s == nil {
		return ""
	}
	return s.legacyPath
}

// Load 强制读取一次（启动装配用）。
func (s *notifyPolicySource) Load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(true)
}

// MaybeReload 在文件 mtime/size（或存在性）变化时重读；返回最近一次错误。
func (s *notifyPolicySource) MaybeReload() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(false)
}

// Effective 返回当前生效的偏好存储、global quiet_hours 与"是否显式设置"
// 标志；调用前会做一次增量热加载检查。prefs 可为 nil（无任何策略）。
func (s *notifyPolicySource) Effective() (*notify.MemoryPreferenceStore, []notify.QuietRange, bool, error) {
	if s == nil {
		return nil, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.refreshLocked(false)
	return s.prefs, append([]notify.QuietRange(nil), s.quiet...), s.quietSet, err
}

// CurrentView 返回当前策略视图（GET API 用，含热加载检查）。
func (s *notifyPolicySource) CurrentView() (notifyPolicyFile, error) {
	if s == nil {
		return notifyPolicyFile{Version: notifyPolicyVersion}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.refreshLocked(false)
	return s.view, err
}

// SourceLabel 返回策略来源：file（策略文件）| legacy（旧偏好回退）| default。
func (s *notifyPolicySource) SourceLabel() string {
	if s == nil {
		return "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.source != "" {
		return s.source
	}
	if s.legacy {
		return "legacy"
	}
	if s.view.Version == 0 && len(s.view.Sessions) == 0 && len(s.view.Channels) == 0 {
		return "default"
	}
	return "file"
}

// Apply 校验并原子写入策略文件，成功后热替换内存状态（PUT API 用）。
// 内容非法返回 notifyPolicyValidationError（HTTP 400）；写盘失败返回普通错误。
func (s *notifyPolicySource) Apply(view notifyPolicyFile) error {
	if s == nil {
		return errors.New("notify policy source is not configured")
	}
	view.Version = notifyPolicyVersion
	if _, _, _, err := buildNotifyPolicyStore(view); err != nil {
		return &notifyPolicyValidationError{msg: err.Error()}
	}
	if err := writeNotifyPolicyFile(s.path, view); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(true)
}

// refreshLocked 读取/重读策略。force=false 时仅当文件戳变化才重读。
func (s *notifyPolicySource) refreshLocked(force bool) error {
	stamp, exists := s.stampLocked()
	if !force && s.loaded && stamp == s.stamp {
		return s.lastErr
	}
	s.stamp = stamp
	s.loaded = true
	if !exists {
		legacyStore, err := loadNotifyPreferences(s.legacyPath)
		if err != nil {
			s.lastErr = fmt.Errorf("legacy notify-prefs.json: %w", err)
			return s.lastErr
		}
		if legacyStore == nil {
			s.view = notifyPolicyFile{Version: notifyPolicyVersion}
			s.prefs = nil
			s.quiet = nil
			s.quietSet = false
			s.legacy = false
			s.source = "default"
			s.lastErr = nil
			return nil
		}
		view := policyViewFromLegacy(legacyStore)
		prefs, quiet, quietSet, err := buildNotifyPolicyStore(view)
		if err != nil {
			s.lastErr = err
			return err
		}
		s.view, s.prefs, s.quiet, s.quietSet = view, prefs, quiet, quietSet
		s.legacy, s.source, s.lastErr = true, "legacy", nil
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.lastErr = err
		return err
	}
	var view notifyPolicyFile
	if err := json.Unmarshal(raw, &view); err != nil {
		s.lastErr = fmt.Errorf("parse %s: %w", s.path, err)
		return s.lastErr
	}
	prefs, quiet, quietSet, err := buildNotifyPolicyStore(view)
	if err != nil {
		s.lastErr = err
		return err
	}
	if view.Version == 0 {
		view.Version = notifyPolicyVersion
	}
	s.view, s.prefs, s.quiet, s.quietSet = view, prefs, quiet, quietSet
	s.legacy, s.source, s.lastErr = false, "file", nil
	return nil
}

// stampLocked 计算策略文件戳；策略文件缺失时回退到旧偏好文件戳。
func (s *notifyPolicySource) stampLocked() (string, bool) {
	if info, err := os.Stat(s.path); err == nil {
		return fmt.Sprintf("file:%d:%d", info.ModTime().UnixNano(), info.Size()), true
	} else if !os.IsNotExist(err) {
		return "error:" + err.Error(), false
	}
	if info, err := os.Stat(s.legacyPath); err == nil {
		return fmt.Sprintf("legacy:%d:%d", info.ModTime().UnixNano(), info.Size()), false
	}
	return "missing", false
}

func writeNotifyPolicyFile(path string, view notifyPolicyFile) error {
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows 不允许 rename 覆盖已存在文件：先删目标再重试。
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}
