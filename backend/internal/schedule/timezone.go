package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// wallClock 是本地墙钟读数。
type wallClock struct {
	hour, minute, second, nanos int
}

// ParseTimeZone 解析显式 IANA/UTC 时区名。
func ParseTimeZone(name string) (*time.Location, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, inputError(CodeInvalidTimeZone, "time_zone is required.")
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, inputError(CodeInvalidTimeZone, "unknown time zone %q.", trimmed)
	}
	return loc, nil
}

// parseLocalTime 解析 HH:MM:SS 及可选 1-3 位小数秒，返回墙钟与规范化文本。
func parseLocalTime(raw string) (wallClock, string, error) {
	trimmed := strings.TrimSpace(raw)
	clockPart := trimmed
	frac := ""
	if idx := strings.IndexByte(trimmed, '.'); idx >= 0 {
		clockPart = trimmed[:idx]
		frac = trimmed[idx+1:]
	}
	segments := strings.Split(clockPart, ":")
	if len(segments) != 3 || len(segments[0]) != 2 || len(segments[1]) != 2 || len(segments[2]) != 2 {
		return wallClock{}, "", inputError(CodeInvalidRule, "time must be HH:MM:SS with zero-padded fields.")
	}
	hour, err1 := strconv.Atoi(segments[0])
	minute, err2 := strconv.Atoi(segments[1])
	second, err3 := strconv.Atoi(segments[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return wallClock{}, "", inputError(CodeInvalidRule, "time must be numeric HH:MM:SS.")
	}
	if hour > 23 || minute > 59 || second > 59 {
		return wallClock{}, "", inputError(CodeInvalidRule, "time is out of range (24:00 and leap seconds are not supported).")
	}
	nanos := 0
	if frac != "" {
		if len(frac) < 1 || len(frac) > 3 {
			return wallClock{}, "", inputError(CodeInvalidRule, "fractional seconds must be 1-3 digits.")
		}
		for _, r := range frac {
			if r < '0' || r > '9' {
				return wallClock{}, "", inputError(CodeInvalidRule, "fractional seconds must be digits.")
			}
		}
		padded := frac
		for len(padded) < 3 {
			padded += "0"
		}
		value, _ := strconv.Atoi(padded)
		nanos = value * int(time.Millisecond)
	}
	normalized := fmt.Sprintf("%02d:%02d:%02d.%03d", hour, minute, second, nanos/int(time.Millisecond))
	return wallClock{hour: hour, minute: minute, second: second, nanos: nanos}, normalized, nil
}

// localInstant 返回 loc 中某本地墙钟时间的 UTC 时点。
//
// ok=false 表示该墙钟时间不存在（DST 缺口）。DST 重叠（fall-back）时返回较早的
// 时点（方案 §5.2 的既定语义）。
func localInstant(year int, month time.Month, day int, clock wallClock, loc *time.Location) (time.Time, bool) {
	base := time.Date(year, month, day, clock.hour, clock.minute, clock.second, clock.nanos, time.UTC)
	probes := []time.Time{
		base.Add(-30 * time.Hour), base.Add(-12 * time.Hour), base,
		base.Add(12 * time.Hour), base.Add(30 * time.Hour),
	}
	var candidates []time.Time
	seen := map[int]bool{}
	for _, probe := range probes {
		_, offset := probe.In(loc).Zone()
		if seen[offset] {
			continue
		}
		seen[offset] = true
		candidate := base.Add(-time.Duration(offset) * time.Second)
		if sameWallClock(candidate, loc, year, month, day, clock) {
			candidates = append(candidates, candidate.UTC())
		}
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates[0], true
}

func sameWallClock(t time.Time, loc *time.Location, year int, month time.Month, day int, clock wallClock) bool {
	local := t.In(loc)
	y, mo, d := local.Date()
	h, mi, s := local.Clock()
	return y == year && mo == month && d == day &&
		h == clock.hour && mi == clock.minute && s == clock.second && local.Nanosecond() == clock.nanos
}

// localDateOf 返回 t 在 loc 中的本地日期（以 UTC 午夜表示）。
func localDateOf(t time.Time, loc *time.Location) time.Time {
	y, mo, d := t.In(loc).Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}

// NormalizeWeekdays 校验并归一化 ISO 星期集合（1=周一..7=周日，升序去重）。
func NormalizeWeekdays(weekdays []int) ([]int, error) {
	if len(weekdays) == 0 {
		return nil, inputError(CodeInvalidRule, "weekdays must be a non-empty ISO weekday set (1..7).")
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(weekdays))
	for _, day := range weekdays {
		if day < 1 || day > 7 {
			return nil, inputError(CodeInvalidRule, "weekdays must be integers 1..7 (Monday..Sunday).")
		}
		if seen[day] {
			continue
		}
		seen[day] = true
		out = append(out, day)
	}
	sort.Ints(out)
	return out, nil
}

func isoWeekday(date time.Time) int {
	wd := int(date.Weekday())
	if wd == 0 {
		return 7 // Sunday
	}
	return wd
}

// CreateDaily 构造一条每日本地钟表时间提醒。
func CreateDaily(id, prompt, title string, in DailyInput, now time.Time) (Record, error) {
	t, err := ScheduleTitle(title)
	if err != nil {
		return Record{}, err
	}
	p, err := SchedulePrompt(prompt)
	if err != nil {
		return Record{}, err
	}
	loc, err := ParseTimeZone(in.TimeZone)
	if err != nil {
		return Record{}, err
	}
	clock, normalized, err := parseLocalTime(in.Time)
	if err != nil {
		return Record{}, err
	}
	nowUTC := now.UTC()
	target, ok := nextWallClockTarget(loc, clock, nowUTC, nil, nil)
	if !ok {
		return Record{}, inputError(CodeTimeOutOfRange, "no representable future daily occurrence.")
	}
	return Record{
		ID: id, Kind: KindDaily, Title: t, Prompt: p,
		Time: normalized, TimeZone: strings.TrimSpace(in.TimeZone), ScheduledAt: target,
	}, nil
}

// CreateWeekly 构造一条每周本地钟表时间 + ISO 星期集合提醒。
func CreateWeekly(id, prompt, title string, in WeeklyInput, now time.Time) (Record, error) {
	t, err := ScheduleTitle(title)
	if err != nil {
		return Record{}, err
	}
	p, err := SchedulePrompt(prompt)
	if err != nil {
		return Record{}, err
	}
	loc, err := ParseTimeZone(in.TimeZone)
	if err != nil {
		return Record{}, err
	}
	clock, normalized, err := parseLocalTime(in.Time)
	if err != nil {
		return Record{}, err
	}
	weekdays, err := NormalizeWeekdays(in.Weekdays)
	if err != nil {
		return Record{}, err
	}
	nowUTC := now.UTC()
	target, ok := nextWallClockTarget(loc, clock, nowUTC, nil, weekdays)
	if !ok {
		return Record{}, inputError(CodeTimeOutOfRange, "no representable future weekly occurrence.")
	}
	return Record{
		ID: id, Kind: KindWeekly, Title: t, Prompt: p,
		Time: normalized, TimeZone: strings.TrimSpace(in.TimeZone), Weekdays: weekdays, ScheduledAt: target,
	}, nil
}

// nextWallClockTarget 找 now 之后第一个匹配的本地发生时点。
//
// weekdays 为 nil 表示每日（任意日期）；afterDate 非 nil 时从该日期次日起算。
func nextWallClockTarget(loc *time.Location, clock wallClock, now time.Time, afterDate *time.Time, weekdays []int) (time.Time, bool) {
	date := localDateOf(now, loc)
	if afterDate != nil {
		date = afterDate.AddDate(0, 0, 1)
	}
	selected := weekdaySet(weekdays)
	for i := 0; i < 800; i++ {
		y, mo, d := date.Date()
		if selected(isoWeekday(date)) {
			if candidate, ok := localInstant(y, mo, d, clock, loc); ok && candidate.After(now) {
				return candidate, true
			}
		}
		date = date.AddDate(0, 0, 1)
		if y > 9998 {
			break
		}
	}
	return time.Time{}, false
}

// latestWallClockDue 找 acceptedAt 之前最近一次匹配的实际发生时点（不早于 savedTarget）。
func latestWallClockDue(loc *time.Location, clock wallClock, acceptedAt time.Time, savedTarget time.Time, weekdays []int) time.Time {
	date := localDateOf(acceptedAt, loc).AddDate(0, 0, 1)
	selected := weekdaySet(weekdays)
	for i := 0; i < 400; i++ {
		date = date.AddDate(0, 0, -1)
		if !selected(isoWeekday(date)) {
			continue
		}
		y, mo, d := date.Date()
		candidate, ok := localInstant(y, mo, d, clock, loc)
		if !ok || candidate.After(acceptedAt) {
			continue
		}
		if candidate.Before(savedTarget) {
			return savedTarget
		}
		return candidate
	}
	return savedTarget
}

func weekdaySet(weekdays []int) func(int) bool {
	if weekdays == nil {
		return func(int) bool { return true }
	}
	selected := map[int]bool{}
	for _, day := range weekdays {
		selected[day] = true
	}
	return func(day int) bool { return selected[day] }
}

// resolveDailyOccurrence 解析每日决策：最近一次到期 + 下一个未来目标。
func resolveDailyOccurrence(record Record, acceptedAt time.Time) (Occurrence, error) {
	loc, err := ParseTimeZone(record.TimeZone)
	if err != nil {
		return Occurrence{}, logError("daily record %s has an unreadable time zone", record.ID)
	}
	clock, _, err := parseLocalTime(record.Time)
	if err != nil {
		return Occurrence{}, logError("daily record %s has an unreadable local time", record.ID)
	}
	return resolveWallClockOccurrence(record, acceptedAt, loc, clock, nil)
}

// resolveWeeklyOccurrence 解析每周决策。
func resolveWeeklyOccurrence(record Record, acceptedAt time.Time) (Occurrence, error) {
	loc, err := ParseTimeZone(record.TimeZone)
	if err != nil {
		return Occurrence{}, logError("weekly record %s has an unreadable time zone", record.ID)
	}
	clock, _, err := parseLocalTime(record.Time)
	if err != nil {
		return Occurrence{}, logError("weekly record %s has an unreadable local time", record.ID)
	}
	weekdays, err := NormalizeWeekdays(record.Weekdays)
	if err != nil {
		return Occurrence{}, logError("weekly record %s has an unreadable weekday set", record.ID)
	}
	return resolveWallClockOccurrence(record, acceptedAt, loc, clock, weekdays)
}

func resolveWallClockOccurrence(record Record, acceptedAt time.Time, loc *time.Location, clock wallClock, weekdays []int) (Occurrence, error) {
	accepted := acceptedAt.UTC()
	savedTarget := record.ScheduledAt.UTC()
	if accepted.Before(savedTarget) {
		return Occurrence{}, logError("%s occurrence cannot precede the committed scheduledAt", record.Kind)
	}
	occurrence := latestWallClockDue(loc, clock, accepted, savedTarget, weekdays)
	occurrenceDate := localDateOf(occurrence, loc)
	next, ok := nextWallClockTarget(loc, clock, accepted, &occurrenceDate, weekdays)
	result := Occurrence{OccurrenceAt: occurrence}
	if ok {
		nextCopy := next
		result.NextScheduledAt = &nextCopy
	}
	return result, nil
}
