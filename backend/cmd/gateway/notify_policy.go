package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
)

// isHighPriorityAttention reports whether an attention kind should bypass
// coalescing and quiet hours (docs/gateway/08-notifications.md §3, §5.6).
// approval/question are the explicitly high-priority states; error is treated
// as high priority too because a failed turn should not be silently muted.
func isHighPriorityAttention(attention string) bool {
	switch attention {
	case "approval", "question", "error":
		return true
	default:
		return false
	}
}

// quietRange is a local-time window [start, end) expressed in minutes since
// midnight. start > end means the window wraps past midnight (e.g. 22:00-08:00).
type quietRange struct {
	start int
	end   int
}

func (r quietRange) contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if r.start == r.end {
		return false
	}
	if r.start < r.end {
		return m >= r.start && m < r.end
	}
	return m >= r.start || m < r.end
}

// parseQuietHours parses a comma-separated list of "HH:MM-HH:MM" windows. An
// empty spec yields no windows (quiet hours disabled).
func parseQuietHours(spec string) ([]quietRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []quietRange
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
		out = append(out, quietRange{start: start, end: end})
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

// inQuietHours reports whether t falls inside any configured quiet window.
func inQuietHours(ranges []quietRange, t time.Time) bool {
	for _, r := range ranges {
		if r.contains(t) {
			return true
		}
	}
	return false
}

// coalesceNotifications merges same-kind low-priority notifications into a
// single summary ("N sessions completed"). A single item is returned unchanged.
func coalesceNotifications(attention string, items []push.Notification) push.Notification {
	if len(items) == 1 {
		return items[0]
	}
	titles := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		t := strings.TrimSpace(it.Title)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		titles = append(titles, t)
	}
	return push.Notification{
		Title: fmt.Sprintf("%d sessions %s", len(items), attention),
		Body:  strings.Join(titles, ", "),
		Tag:   "batch|" + attention,
		URL:   items[0].URL,
	}
}
