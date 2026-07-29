package cell

import (
	"strings"
	"unicode"
)

// LegacyTimelineParser converts historical "[tag] body" supplement lines into
// typed TimelineEvent values. This is the single compatibility entry for
// string-based timeline styling.
func LegacyTimelineParser(line string) (TimelineEvent, bool) {
	leading, body := splitLeadingWS(line)
	_ = leading
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return TimelineEvent{}, false
	}

	// Bullet + bracket: "• [tool] view path"
	if bullet, tag, rest, ok := splitBulletBracketTag(body); ok {
		ev := eventFromTag(tag, strings.TrimSpace(rest))
		ev.Marker = bullet
		return ev, true
	}

	// Bracket tag: "[tool] view"
	if tag, rest, ok := splitBracketTag(body); ok {
		ev := eventFromTag(tag, strings.TrimSpace(rest))
		return ev, true
	}

	// Bullet status without bracket: "• Edited file.go" / "* note"
	if marker, rest, ok := splitBareBullet(body); ok {
		return TimelineEvent{
			Kind:               TimelineNotice,
			Status:             StatusInfo,
			Title:              rest,
			Marker:             marker,
			SuppressKindPrefix: true,
		}, true
	}

	if strings.HasPrefix(trimmed, "failed:") {
		return TimelineEvent{
			Kind:   TimelineNotice,
			Status: StatusError,
			Title:  trimmed,
		}, true
	}

	return TimelineEvent{}, false
}

func eventFromTag(tag, rest string) TimelineEvent {
	key := strings.ToLower(strings.TrimSpace(tag))
	key = strings.Trim(key, "[]")
	ev := TimelineEvent{Title: rest}
	switch key {
	case "tool":
		ev.Kind = TimelineTool
		ev.Status = StatusRunning
	case "tool done":
		ev.Kind = TimelineTool
		ev.Status = StatusSuccess
	case "tool denied":
		ev.Kind = TimelineTool
		ev.Status = StatusDenied
	case "tools":
		ev.Kind = TimelineTool
	case "approval":
		ev.Kind = TimelineApproval
	case "question":
		ev.Kind = TimelineQuestion
	case "reasoning":
		ev.Kind = TimelineReasoning
	case "thinking":
		ev.Kind = TimelineThinking
	case "planning":
		ev.Kind = TimelinePlanning
	case "progress":
		ev.Kind = TimelineProgress
	case "team", "team summary":
		ev.Kind = TimelineTeam
	case "subagent", "task":
		ev.Kind = TimelineTask
	case "tip":
		ev.Kind = TimelineTip
	case "input":
		ev.Kind = TimelineInput
	default:
		ev.Kind = TimelineUnknown
		// Preserve unknown tag text in title when rest empty.
		if rest == "" {
			ev.Title = tag
		} else {
			ev.Title = strings.TrimSpace(tag + " " + rest)
		}
	}
	return ev
}

func splitLeadingWS(s string) (leading, rest string) {
	i := 0
	for i < len(s) {
		r := rune(s[i])
		if r == ' ' || r == '\t' {
			i++
			continue
		}
		break
	}
	return s[:i], s[i:]
}

func splitBracketTag(body string) (tag, rest string, ok bool) {
	trimmed := strings.TrimLeftFunc(body, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	end := strings.IndexByte(trimmed, ']')
	if end <= 0 {
		return "", "", false
	}
	tag = trimmed[:end+1]
	rest = trimmed[end+1:]
	return tag, rest, true
}

func splitBulletBracketTag(body string) (bullet, tag, rest string, ok bool) {
	trimmedLeft := strings.TrimLeft(body, " \t")
	var marker string
	switch {
	case strings.HasPrefix(trimmedLeft, "• "):
		marker = "• "
	case strings.HasPrefix(trimmedLeft, "* "):
		marker = "* "
	default:
		return "", "", "", false
	}
	// Byte-safe: marker may be multi-byte (• is 3 bytes in UTF-8).
	idx := strings.Index(body, marker)
	if idx < 0 {
		return "", "", "", false
	}
	after := body[idx+len(marker):]
	tag, rest, ok = splitBracketTag(after)
	if !ok {
		return "", "", "", false
	}
	bullet = body[:idx+len(marker)]
	return bullet, tag, rest, true
}

// splitBareBullet extracts "• title" / "* title" lines that have no bracket tag.
// marker includes any leading spaces before the bullet so FormatPlain can match body.
func splitBareBullet(body string) (marker, rest string, ok bool) {
	trimmedLeft := strings.TrimLeft(body, " \t")
	var m string
	switch {
	case strings.HasPrefix(trimmedLeft, "• "):
		m = "• "
	case strings.HasPrefix(trimmedLeft, "* "):
		m = "* "
	default:
		return "", "", false
	}
	idx := strings.Index(body, m)
	if idx < 0 {
		return "", "", false
	}
	rest = strings.TrimSpace(body[idx+len(m):])
	if rest == "" {
		return "", "", false
	}
	return body[:idx+len(m)], rest, true
}
