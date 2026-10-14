package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// notifyPrefsFile 是 <data-dir>/notify-prefs.json 的形状（docs/bots/06
// §1 D6 三级键；文件可选，缺失 = 无偏好 = 行为不变；改动后需重启 gateway）：
//
//	{
//	  "scopes": [
//	    {"scope": "global", "muted": true},
//	    {"scope": "session:s-123", "min_priority": "high"},
//	    {"scope": "channel:ntfy", "muted": true}
//	  ]
//	}
type notifyPrefsFile struct {
	Scopes []notifyPrefEntry `json:"scopes"`
}

type notifyPrefEntry struct {
	Scope       string `json:"scope"`
	Muted       *bool  `json:"muted,omitempty"`
	MinPriority string `json:"min_priority,omitempty"`
}

// loadNotifyPreferences 加载偏好文件；文件不存在返回 (nil, nil)（= 行为不变）。
func loadNotifyPreferences(path string) (*notify.MemoryPreferenceStore, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var file notifyPrefsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	store := notify.NewMemoryPreferenceStore()
	for _, item := range file.Scopes {
		scope, err := parseNotifyScope(strings.TrimSpace(item.Scope))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		pref := notify.Preference{Muted: item.Muted}
		if mp := strings.TrimSpace(item.MinPriority); mp != "" {
			p, err := parseNotifyPriority(mp)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			pref.MinPriority = &p
		}
		store.Set(scope, pref)
	}
	return store, nil
}

func parseNotifyScope(raw string) (notify.Scope, error) {
	switch {
	case raw == "" || raw == "global":
		return notify.GlobalScope(), nil
	case strings.HasPrefix(raw, "session:") && strings.TrimSpace(strings.TrimPrefix(raw, "session:")) != "":
		return notify.SessionScope(strings.TrimPrefix(raw, "session:")), nil
	case strings.HasPrefix(raw, "channel:") && strings.TrimSpace(strings.TrimPrefix(raw, "channel:")) != "":
		return notify.ChannelScope(strings.TrimPrefix(raw, "channel:")), nil
	default:
		return "", fmt.Errorf("invalid scope %q (want global|session:<id>|channel:<name>)", raw)
	}
}

func parseNotifyPriority(raw string) (notify.Priority, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "high":
		return notify.PriorityHigh, nil
	case "normal":
		return notify.PriorityNormal, nil
	case "low":
		return notify.PriorityLow, nil
	default:
		return 0, fmt.Errorf("invalid min_priority %q (want high|normal|low)", raw)
	}
}
