// Package notify 是通知治理内核（策略层）：注意力优先级分类、免打扰时段与
// 低优先合并。目标架构见 docs/bots/06-target-architecture.md §2.2：
// 本包先落地"策略内核"（优先级/静默/合并），通道注册与调度（Channel/Dispatcher）
// 在后续阶段接入，宿主侧的高优先立即投递 / 低优先合并 / 免打扰行为保持逐字不变。
//
// 依赖说明：Notification 目前是 internal/gateway/push 的信封别名，避免在
// gateway 里做无意义的类型转换；等 Channel 抽象落地（P1）时再把信封提升到
// 本包，由各通道适配（Web Push / ntfy / IM / SSE / 终端）。
package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
)

// Notification 是通道无关的通知信封（当前为 push 信封的别名）。
type Notification = push.Notification

// Priority 是通知优先级（与 05/06 文档的 high/normal/low 一致）。
type Priority int

const (
	// PriorityLow 不会被主动投递（仅收件箱/日志）。
	PriorityLow Priority = iota
	// PriorityNormal 低优先：可合并、可静音。
	PriorityNormal
	// PriorityHigh 高优先：立即投递并穿透免打扰。
	PriorityHigh
)

func (p Priority) String() string {
	switch p {
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	default:
		return "low"
	}
}

// PriorityOf 把注意力 kind 映射为优先级。
//
//	approval / question / error        → high（穿透免打扰、立即）
//	completed / waiting                → normal（合并窗口内合并、免打扰期静音）
//	其它非空 kind                       → normal（保守：未知状态按普通处理，不静默丢弃）
//	空                                  → low（不作为通知触发）
func PriorityOf(kind string) Priority {
	switch strings.TrimSpace(kind) {
	case "approval", "question", "error":
		return PriorityHigh
	case "":
		return PriorityLow
	default:
		return PriorityNormal
	}
}

// IsHighPriority 报告该注意力是否应绕过合并与免打扰。
func IsHighPriority(kind string) bool {
	return PriorityOf(kind) == PriorityHigh
}

// QuietRange 是本地时间的免打扰窗口 [Start, End)，单位为分钟（自午夜起）。
// Start > End 表示跨午夜（如 22:00-08:00）。
type QuietRange struct {
	Start int
	End   int
}

// Contains 报告 t 是否落在窗口内（半开区间；零宽窗口恒为 false）。
func (r QuietRange) Contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if r.Start == r.End {
		return false
	}
	if r.Start < r.End {
		return m >= r.Start && m < r.End
	}
	return m >= r.Start || m < r.End
}

// ParseQuietHours 解析以逗号分隔的 "HH:MM-HH:MM" 窗口列表；空串表示不禁音。
func ParseQuietHours(spec string) ([]QuietRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []QuietRange
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bounds := strings.SplitN(part, "-", 2)
		if len(bounds) != 2 {
			return nil, fmt.Errorf("invalid quiet-hours window %q (want HH:MM-HH:MM)", part)
		}
		start, err := parseClock(bounds[0])
		if err != nil {
			return nil, fmt.Errorf("invalid quiet-hours window %q: %w", part, err)
		}
		end, err := parseClock(bounds[1])
		if err != nil {
			return nil, fmt.Errorf("invalid quiet-hours window %q: %w", part, err)
		}
		out = append(out, QuietRange{Start: start, End: end})
	}
	return out, nil
}

func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return h*60 + m, nil
}

// InQuietHours 报告 t 是否落在任一免打扰窗口内。
func InQuietHours(ranges []QuietRange, t time.Time) bool {
	for _, r := range ranges {
		if r.Contains(t) {
			return true
		}
	}
	return false
}

// Coalesce 把同类低优先通知合并为一条摘要（"N sessions completed"）；
// 单项原样返回。投递提示（Urgency/TTL）沿用首条，保证通道侧行为不变。
func Coalesce(attention string, items []Notification) Notification {
	if len(items) == 1 {
		return items[0]
	}
	titles := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		if _, ok := seen[title]; ok {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
	}
	return Notification{
		Title:   fmt.Sprintf("%d sessions %s", len(items), attention),
		Body:    strings.Join(titles, ", "),
		Tag:     "batch|" + attention,
		URL:     items[0].URL,
		Urgency: items[0].Urgency,
		TTL:     items[0].TTL,
	}
}
