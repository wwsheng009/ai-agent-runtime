// Package keymap 定义 aicli 交互式 TUI 的「按键 → 动作」注册表，并支持用户
// 通过 ~/.aicli/keybindings.json 做部分覆盖。
//
// 设计口径（对齐 CommandCode Interactive Mode 的键位自定义语义）：
//   - action id 使用稳定点分命名（app.*），作为用户配置与 /hotkeys 展示键；
//   - 用户文件只写要改的项，未提及的 action 保持默认；
//   - 绑定值可以是字符串、字符串数组；空数组表示禁用该动作；
//   - 文件缺失、JSON 损坏、未知 action、无法解析的按键都只记录 warning 并
//     回退默认，单个坏条目不拖垮整个文件。
//
// 本包只做「按键 → 动作」解析，不读取 stdin、不持有输入所有权；分发与让位
// 仍由既有输入仲裁（弹层/选择器/pager 优先）决定。
package keymap

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Action 是稳定的动作标识。
type Action string

const (
	// ActionPermissionCycle 在权限模式之间循环切换。
	ActionPermissionCycle Action = "app.permission.cycle"
	// ActionTranscriptPager 打开全屏 transcript 分页器。
	ActionTranscriptPager Action = "app.transcript.pager"
)

// ActionSpec 描述一个可绑定动作的静态元数据。
type ActionSpec struct {
	Action      Action
	Description string
	Defaults    []string
	Remappable  bool
}

// catalog 是唯一的可绑定动作清单：/hotkeys、冲突检测与默认键都从这里读取。
var catalog = []ActionSpec{
	{
		Action:      ActionPermissionCycle,
		Description: "权限模式循环（default → accept_edits → plan → bypass_permissions）",
		Defaults:    []string{"shift+tab", "alt+m"},
		Remappable:  true,
	},
	{
		Action:      ActionTranscriptPager,
		Description: "打开全屏 transcript 分页器（等价 ctrl+t 默认行为）",
		Defaults:    []string{"ctrl+t"},
		Remappable:  true,
	},
}

// Catalog 返回可绑定动作清单的副本。
func Catalog() []ActionSpec {
	out := make([]ActionSpec, len(catalog))
	for i, spec := range catalog {
		out[i] = spec
		out[i].Defaults = append([]string(nil), spec.Defaults...)
	}
	return out
}

// Chord 是一个规范化的按键组合。Key 为小写规范名（字母/数字/符号或命名键）。
type Chord struct {
	Ctrl  bool
	Alt   bool
	Shift bool
	Key   string
}

// IsZero 表示未设置。
func (c Chord) IsZero() bool { return c.Key == "" }

// String 返回规范字符串，修饰键顺序固定为 ctrl+alt+shift+key。
func (c Chord) String() string {
	if c.IsZero() {
		return ""
	}
	parts := make([]string, 0, 4)
	if c.Ctrl {
		parts = append(parts, "ctrl")
	}
	if c.Alt {
		parts = append(parts, "alt")
	}
	if c.Shift {
		parts = append(parts, "shift")
	}
	parts = append(parts, c.Key)
	return strings.Join(parts, "+")
}

var namedKeys = map[string]string{
	"enter":     "enter",
	"return":    "enter",
	"tab":       "tab",
	"escape":    "escape",
	"esc":       "escape",
	"space":     "space",
	"backspace": "backspace",
	"delete":    "delete",
	"del":       "delete",
	"insert":    "insert",
	"ins":       "insert",
	"up":        "up",
	"down":      "down",
	"left":      "left",
	"right":     "right",
	"home":      "home",
	"end":       "end",
	"pageup":    "pageup",
	"pgup":      "pageup",
	"pagedown":  "pagedown",
	"pgdn":      "pagedown",
}

// ParseChord 解析 "ctrl+shift+tab" 这类字符串；大小写不敏感、修饰键顺序无关。
func ParseChord(raw string) (Chord, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Chord{}, fmt.Errorf("空按键")
	}
	parts := strings.Split(trimmed, "+")
	chord := Chord{}
	keyToken := ""
	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			return Chord{}, fmt.Errorf("按键 %q 含空片段", raw)
		}
		switch token {
		case "ctrl", "control":
			chord.Ctrl = true
		case "alt", "option", "opt", "meta":
			chord.Alt = true
		case "shift":
			chord.Shift = true
		default:
			if keyToken != "" {
				return Chord{}, fmt.Errorf("按键 %q 含多个主键", raw)
			}
			keyToken = token
		}
	}
	if keyToken == "" {
		return Chord{}, fmt.Errorf("按键 %q 缺少主键", raw)
	}
	if named, ok := namedKeys[keyToken]; ok {
		chord.Key = named
		return chord, nil
	}
	r, size := utf8.DecodeRuneInString(keyToken)
	if size == 0 || size != len(keyToken) {
		return Chord{}, fmt.Errorf("不支持的按键名 %q", raw)
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsPrint(r) {
		chord.Key = string(r)
		return chord, nil
	}
	return Chord{}, fmt.Errorf("不支持的按键名 %q", raw)
}

// Binding 是某个动作的当前生效绑定。
type Binding struct {
	Action      Action
	Description string
	Chords      []Chord
	Defaults    []Chord
	// Source 为 "user"（用户文件显式覆盖，含禁用）或 "default"。
	Source     string
	Remappable bool
}

// Registry 保存默认绑定与用户覆盖的合并结果。
type Registry struct {
	effective map[Action][]Chord
	defaults  map[Action][]Chord
	sources   map[Action]string
	byChord   map[Chord]Action
	warnings  []string
}

// Load 读取用户按键配置文件；文件不存在或内容损坏都回退默认并记录 warning。
func Load(path string) *Registry {
	registry := &Registry{}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		registry.rebuild()
		return registry
	}
	raw, err := os.ReadFile(trimmed)
	switch {
	case err == nil:
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			registry.warn(fmt.Sprintf("按键配置解析失败，已回退默认键位: %v", err))
		} else {
			registry.ApplyUserBindings(parsed)
		}
	case os.IsNotExist(err):
		// 未配置是正常路径。
	default:
		registry.warn(fmt.Sprintf("按键配置读取失败，已回退默认键位: %v", err))
	}
	registry.rebuild()
	return registry
}

// ApplyUserBindings 合并用户绑定；未知 action / 类型错误 / 无法解析的按键
// 只记录 warning，其余条目继续生效。
func (r *Registry) ApplyUserBindings(raw map[string]any) {
	if r == nil || len(raw) == 0 {
		return
	}
	known := make(map[Action]ActionSpec, len(catalog))
	for _, spec := range catalog {
		known[spec.Action] = spec
	}
	r.ensureMaps()
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		id := Action(strings.TrimSpace(key))
		spec, ok := known[id]
		if !ok {
			r.warn(fmt.Sprintf("忽略未知按键动作 %q", key))
			continue
		}
		chords, disabled, err := parseBindingValue(raw[key], key)
		if err != nil {
			r.warn(err.Error())
			continue
		}
		if !spec.Remappable {
			r.warn(fmt.Sprintf("动作 %q 不支持自定义，已忽略", key))
			continue
		}
		if disabled {
			r.effective[id] = nil
			r.sources[id] = "user"
			continue
		}
		r.effective[id] = chords
		r.sources[id] = "user"
	}
}

func parseBindingValue(value any, key string) ([]Chord, bool, error) {
	switch typed := value.(type) {
	case string:
		chord, err := ParseChord(typed)
		if err != nil {
			return nil, false, fmt.Errorf("动作 %q 的按键 %q 无法解析，已忽略: %w", key, typed, err)
		}
		return []Chord{chord}, false, nil
	case []any:
		if len(typed) == 0 {
			return nil, true, nil
		}
		chords := make([]Chord, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("动作 %q 的绑定项必须是字符串，已忽略该项", key)
			}
			chord, err := ParseChord(text)
			if err != nil {
				return nil, false, fmt.Errorf("动作 %q 的按键 %q 无法解析，已忽略该项", key, text)
			}
			chords = append(chords, chord)
		}
		return chords, false, nil
	default:
		return nil, false, fmt.Errorf("动作 %q 的绑定必须是字符串或字符串数组，已忽略", key)
	}
}

func (r *Registry) ensureMaps() {
	if r.effective == nil {
		r.effective = make(map[Action][]Chord)
	}
	if r.defaults == nil {
		r.defaults = make(map[Action][]Chord)
	}
	if r.sources == nil {
		r.sources = make(map[Action]string)
	}
}

func (r *Registry) rebuild() {
	r.ensureMaps()
	for _, spec := range catalog {
		defaults := make([]Chord, 0, len(spec.Defaults))
		for _, raw := range spec.Defaults {
			if chord, err := ParseChord(raw); err == nil {
				defaults = append(defaults, chord)
			}
		}
		r.defaults[spec.Action] = defaults
		if _, overridden := r.effective[spec.Action]; !overridden {
			r.effective[spec.Action] = append([]Chord(nil), defaults...)
			r.sources[spec.Action] = "default"
		}
	}
	r.rebuildChordIndex()
}

func (r *Registry) rebuildChordIndex() {
	r.byChord = make(map[Chord]Action)
	for _, spec := range catalog {
		for _, chord := range r.effective[spec.Action] {
			if existing, taken := r.byChord[chord]; taken {
				if existing != spec.Action {
					r.warn(fmt.Sprintf("按键 %s 同时绑定到 %s 与 %s，保留前者", chord.String(), existing, spec.Action))
				}
				continue
			}
			r.byChord[chord] = spec.Action
		}
	}
}

func (r *Registry) warn(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	for _, existing := range r.warnings {
		if existing == message {
			return
		}
	}
	r.warnings = append(r.warnings, message)
}

// Resolve 按规范 chord 字符串查找动作（如 "shift+tab"）。
func (r *Registry) Resolve(chord string) (Action, bool) {
	if r == nil {
		return "", false
	}
	parsed, err := ParseChord(chord)
	if err != nil {
		return "", false
	}
	return r.ResolveChord(parsed)
}

// ResolveChord 按 Chord 查找动作。
func (r *Registry) ResolveChord(chord Chord) (Action, bool) {
	if r == nil || len(r.byChord) == 0 {
		return "", false
	}
	action, ok := r.byChord[chord]
	return action, ok
}

// Effective 返回按 action id 排序的当前生效绑定。
func (r *Registry) Effective() []Binding {
	if r == nil {
		return nil
	}
	bindings := make([]Binding, 0, len(catalog))
	for _, spec := range catalog {
		source := r.sources[spec.Action]
		if source == "" {
			source = "default"
		}
		bindings = append(bindings, Binding{
			Action:      spec.Action,
			Description: spec.Description,
			Chords:      append([]Chord(nil), r.effective[spec.Action]...),
			Defaults:    append([]Chord(nil), r.defaults[spec.Action]...),
			Source:      source,
			Remappable:  spec.Remappable,
		})
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].Action < bindings[j].Action })
	return bindings
}

// Warnings 返回加载/合并过程中的非致命问题。
func (r *Registry) Warnings() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.warnings...)
}
