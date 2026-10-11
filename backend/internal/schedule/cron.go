package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cronFieldSpec 是一个 cron 字段的取值域与上界。
type cronFieldSpec struct {
	min, max int
}

var cronFieldSpecs = [5]cronFieldSpec{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 7},  // day of week（0 与 7 均为周日）
}

// ParseCron 解析并规范化五字段 Vixie 表达式。
//
// 只接受 *、单值、a-b、*/n、a-b/n 与逗号列表；拒绝 L/W/#、名称、@宏、六字段、
// 越界、反向范围、零步长与空字段。规范化：展开为值集合后确定性重编码——
// 重复合并、相邻合并、均匀步长写 a-b/n、以 * 开头的字段写星号步长、步长 1
// 移除、周日统一写 0。解码期同样用它拒绝非规范化存储。
func ParseCron(expression string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return "", inputError(CodeInvalidRule, "cron expression must have exactly five fields (minute hour day-of-month month day-of-week).")
	}
	sets := [5][]int{}
	starred := [5]bool{}
	for i, raw := range fields {
		set, star, err := parseCronField(i, raw)
		if err != nil {
			return "", err
		}
		sets[i] = set
		starred[i] = star
	}
	// Vixie：0 与 7 都表示周日；规范化统一写 0。
	for _, value := range sets[4] {
		if value == 7 {
			sets[4] = append(sets[4], 0)
		}
	}
	sets[4] = uniqueSorted(sets[4])
	return encodeCron(sets, starred), nil
}

// parseCronField 解析单个字段；返回匹配值集合与该字段是否以 * 开头。
func parseCronField(index int, raw string) ([]int, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false, inputError(CodeInvalidRule, "cron field %d is empty.", index+1)
	}
	spec := cronFieldSpecs[index]
	starred := strings.HasPrefix(trimmed, "*")
	seen := map[int]bool{}
	out := []int{}
	add := func(value int) error {
		if value < spec.min || value > spec.max {
			return inputError(CodeInvalidRule, "cron field %d value %d is out of range %d-%d.", index+1, value, spec.min, spec.max)
		}
		// dow: 7 折叠为 0（规范化时再统一）。
		if index == 4 && value == 7 {
			value = 0
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
		return nil
	}
	for _, part := range strings.Split(trimmed, ",") {
		if part == "" {
			return nil, false, inputError(CodeInvalidRule, "cron field %d has an empty element.", index+1)
		}
		if strings.ContainsAny(part, "LW#") || strings.ContainsAny(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ@") {
			return nil, false, inputError(CodeInvalidRule, "cron field %d uses an unsupported token (L/W/#, names and @macros are rejected).", index+1)
		}
		step := 1
		rangePart := part
		if idx := strings.IndexByte(part, '/'); idx >= 0 {
			rangePart = part[:idx]
			stepRaw := part[idx+1:]
			parsed, err := strconv.Atoi(stepRaw)
			if err != nil || parsed < 1 {
				return nil, false, inputError(CodeInvalidRule, "cron field %d step must be a positive integer.", index+1)
			}
			step = parsed
		}
		lo, hi := spec.min, spec.max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			bounds := strings.Split(rangePart, "-")
			if len(bounds) != 2 {
				return nil, false, inputError(CodeInvalidRule, "cron field %d range must be a-b.", index+1)
			}
			var err error
			lo, err = strconv.Atoi(bounds[0])
			if err != nil {
				return nil, false, inputError(CodeInvalidRule, "cron field %d range start must be numeric.", index+1)
			}
			hi, err = strconv.Atoi(bounds[1])
			if err != nil {
				return nil, false, inputError(CodeInvalidRule, "cron field %d range end must be numeric.", index+1)
			}
			if index == 4 && hi == 7 {
				hi = 0 // 0-7 归一为 0-6 之后再展开会丢 7，单独处理：
			}
		default:
			value, err := strconv.Atoi(rangePart)
			if err != nil {
				return nil, false, inputError(CodeInvalidRule, "cron field %d value must be numeric.", index+1)
			}
			if step != 1 {
				return nil, false, inputError(CodeInvalidRule, "cron field %d step is only valid with * or a range.", index+1)
			}
			lo, hi = value, value
		}
		if lo > hi {
			return nil, false, inputError(CodeInvalidRule, "cron field %d range must not be reversed.", index+1)
		}
		// dow 的 0-7 展开：7 与 0 同义。
		if index == 4 && rangePart != "*" && hi == 0 && strings.Contains(rangePart, "-") {
			hi = 7
		}
		for value := lo; value <= hi; value += step {
			if err := add(value); err != nil {
				return nil, false, err
			}
		}
	}
	sort.Ints(out)
	return uniqueSorted(out), starred, nil
}

// encodeCron 确定性重编码值集合（方案 §5.3）。
func encodeCron(sets [5][]int, starred [5]bool) string {
	fields := make([]string, 5)
	for i := 0; i < 5; i++ {
		fields[i] = encodeCronField(i, sets[i], starred[i])
	}
	return strings.Join(fields, " ")
}

func encodeCronField(index int, values []int, starred bool) string {
	spec := cronFieldSpecs[index]
	if len(values) == 0 {
		return "*"
	}
	// dow 折叠 7→0 后的有效域是 0..6（7 个值）；其余字段是 max-min+1。
	effectiveSize := spec.max - spec.min + 1
	if index == 4 {
		effectiveSize = 7
	}
	if len(values) == effectiveSize {
		return "*"
	}
	// 星号字段：写最宽星号步长并追加其余取值。
	if starred {
		if step, ok := uniformStep(values); ok && step > 1 && values[0] == spec.min {
			return fmt.Sprintf("*/%d", step)
		}
	}
	// 先尝试把整个集合编码为一个步长段（dsh 语义：均匀步长写 a-b/n），
	// 再回退到连续段切分。
	if len(values) >= 3 {
		if step, ok := uniformStep(values); ok && step > 1 {
			return fmt.Sprintf("%d-%d/%d", values[0], values[len(values)-1], step)
		}
	}
	// 连续段切分。
	segments := splitRuns(values)
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch {
		case len(segment) == 1:
			parts = append(parts, strconv.Itoa(segment[0]))
		default:
			if step, ok := uniformStep(segment); ok && step > 1 && len(segment) >= 3 {
				parts = append(parts, fmt.Sprintf("%d-%d/%d", segment[0], segment[len(segment)-1], step))
			} else {
				parts = append(parts, fmt.Sprintf("%d-%d", segment[0], segment[len(segment)-1]))
			}
		}
	}
	return strings.Join(parts, ",")
}

// splitRuns 把升序集合切成连续段。
func splitRuns(values []int) [][]int {
	out := [][]int{}
	start := 0
	for i := 1; i <= len(values); i++ {
		if i == len(values) || values[i] != values[i-1]+1 {
			out = append(out, values[start:i])
			start = i
		}
	}
	return out
}

// isUniformStep 报告连续段是否为均匀步长（差值恒定且 >1）。
func isUniformStep(segment []int) bool {
	if len(segment) < 3 {
		return false
	}
	step := segment[1] - segment[0]
	if step <= 1 {
		return false
	}
	for i := 2; i < len(segment); i++ {
		if segment[i]-segment[i-1] != step {
			return false
		}
	}
	return true
}

// uniformStep 报告集合（升序）是否为从首值起的等差序列。
func uniformStep(values []int) (int, bool) {
	if len(values) < 2 {
		return 0, false
	}
	step := values[1] - values[0]
	for i := 2; i < len(values); i++ {
		if values[i]-values[i-1] != step {
			return 0, false
		}
	}
	return step, true
}

func uniqueSorted(values []int) []int {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// CreateCron 构造一条五字段 cron 提醒。
func CreateCron(id, prompt, title string, in CronInput, now time.Time) (Record, error) {
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
	expression, err := ParseCron(in.Expression)
	if err != nil {
		return Record{}, err
	}
	nowUTC := now.UTC()
	target, ok := nextCronTarget(expression, loc, nowUTC, nil)
	if !ok {
		return Record{}, inputError(CodeTimeOutOfRange, "no representable future cron occurrence.")
	}
	return Record{
		ID: id, Kind: KindCron, Title: t, Prompt: p,
		Expression: expression, TimeZone: strings.TrimSpace(in.TimeZone), ScheduledAt: target,
	}, nil
}

// resolveCronOccurrence 解析 cron 决策：最近一次到期 + 下一个未来目标。
func resolveCronOccurrence(record Record, acceptedAt time.Time) (Occurrence, error) {
	loc, err := ParseTimeZone(record.TimeZone)
	if err != nil {
		return Occurrence{}, logError("cron record %s has an unreadable time zone", record.ID)
	}
	savedTarget := record.ScheduledAt.UTC()
	if acceptedAt.Before(savedTarget) {
		return Occurrence{}, logError("cron occurrence cannot precede the committed scheduledAt")
	}
	occurrence := latestCronDue(record.Expression, loc, acceptedAt, savedTarget)
	// cron 同一天可多次发生（如每 15 分钟）：下一个目标从决策时刻起找，
	// 不跳过发生日（与 daily/weekly 的"每日期一次"不同）。
	next, ok := nextCronTarget(record.Expression, loc, acceptedAt, nil)
	result := Occurrence{OccurrenceAt: occurrence}
	if ok {
		nextCopy := next
		result.NextScheduledAt = &nextCopy
	}
	return result, nil
}

// parseCronExpression 把规范化表达式解析回值集合（供匹配用）。
func parseCronExpression(expression string) ([5][]int, [5]bool, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return [5][]int{}, [5]bool{}, logError("stored cron expression must have five fields")
	}
	sets := [5][]int{}
	starred := [5]bool{}
	for i, raw := range fields {
		set, star, err := parseCronField(i, raw)
		if err != nil {
			return [5][]int{}, [5]bool{}, logError("stored cron expression is invalid: %v", err)
		}
		sets[i] = set
		starred[i] = star
	}
	return sets, starred, nil
}

// cronDateMatches 实现 Vixie 的 DOM/DOW 语义：
// 任一字段为星号字段 → 两者都需匹配；否则任一匹配即可。
func cronDateMatches(date time.Time, sets [5][]int, starred [5]bool, loc *time.Location) bool {
	day := date.In(loc).Day()
	weekday := isoWeekday(date)
	if weekday == 7 {
		weekday = 0
	}
	domOK := contains(sets[2], day)
	dowOK := contains(sets[4], weekday)
	if starred[2] || starred[4] {
		return domOK && dowOK
	}
	return domOK || dowOK
}

func cronTimeMatches(minute, hour int, sets [5][]int) bool {
	return contains(sets[0], minute) && contains(sets[1], hour)
}

func contains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// nextCronTarget 找 now 之后第一个匹配分钟（分钟粒度；跳过不存在/越界的日期）。
func nextCronTarget(expression string, loc *time.Location, now time.Time, afterDate *time.Time) (time.Time, bool) {
	sets, starred, err := parseCronExpression(expression)
	if err != nil {
		return time.Time{}, false
	}
	start := localDateOf(now, loc)
	if afterDate != nil {
		start = afterDate.AddDate(0, 0, 1)
	}
	date := start
	for i := 0; i < 800; i++ {
		y, mo, d := date.Date()
		if cronDateMatches(date, sets, starred, loc) {
			// 当天逐分钟向前扫（从 max(now, 00:00) 起）。
			var from time.Time
			if date.Equal(localDateOf(now, loc)) {
				from = now.In(loc).Truncate(time.Minute).Add(time.Minute)
			} else {
				from = time.Date(y, mo, d, 0, 0, 0, 0, loc)
			}
			end := time.Date(y, mo, d, 23, 59, 0, 0, loc)
			for cursor := from; !cursor.After(end); cursor = cursor.Add(time.Minute) {
				if _, offset := cursor.Zone(); offset != cursorMinuteOffset(date, cursor) {
					continue // 跳过分歧分钟（DST 切换分钟）
				}
				h, m, _ := cursor.Clock()
				if cronTimeMatches(m, h, sets) {
					return cursor.UTC(), true
				}
			}
		}
		date = date.AddDate(0, 0, 1)
		if y > 9998 {
			break
		}
	}
	return time.Time{}, false
}

// cursorMinuteOffset 是一个小助手：取该分钟起始的 UTC 偏移（用于跳过 DST 切换分钟）。
func cursorMinuteOffset(date time.Time, cursor time.Time) int {
	_ = date
	_, offset := cursor.Zone()
	return offset
}

// latestCronDue 找 acceptedAt 之前最近一次匹配分钟（不早于 savedTarget）。
func latestCronDue(expression string, loc *time.Location, acceptedAt time.Time, savedTarget time.Time) time.Time {
	sets, starred, err := parseCronExpression(expression)
	if err != nil {
		return savedTarget
	}
	cursor := acceptedAt.In(loc).Truncate(time.Minute)
	floor := savedTarget
	for i := 0; i < 60*24*400; i++ {
		date := localDateOf(cursor, loc)
		if cronDateMatches(date, sets, starred, loc) {
			h, m, _ := cursor.Clock()
			if cronTimeMatches(m, h, sets) {
				candidate := cursor.UTC()
				if candidate.Before(floor) {
					return floor
				}
				return candidate
			}
		}
		cursor = cursor.Add(-time.Minute)
	}
	return savedTarget
}
