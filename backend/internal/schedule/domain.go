package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewID 生成全局唯一的提醒 id。
func NewID() string { return "schedule-" + uuid.NewString() }

// ScheduleTitle 校验并归一化任务标题：trim 后非空且不超过 MaxTitleLength。
func ScheduleTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", inputError(CodeInvalidPrompt, "title is required and must be non-empty after trimming.")
	}
	if len([]rune(trimmed)) > MaxTitleLength {
		return "", inputError(CodeInvalidPrompt, "title must be at most %d characters.", MaxTitleLength)
	}
	return trimmed, nil
}

// SchedulePrompt 校验并归一化提醒内容：trim 后非空。
func SchedulePrompt(prompt string) (string, error) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "", inputError(CodeInvalidPrompt, "prompt must be non-empty after trimming.")
	}
	return trimmed, nil
}

// CreateAfter 构造一条从正延迟创建的一次性提醒。
func CreateAfter(id, prompt, title string, afterSeconds int64, now time.Time) (Record, error) {
	t, err := ScheduleTitle(title)
	if err != nil {
		return Record{}, err
	}
	p, err := SchedulePrompt(prompt)
	if err != nil {
		return Record{}, err
	}
	if afterSeconds <= 0 {
		return Record{}, inputError(CodeInvalidRule, "after_seconds must be a positive integer.")
	}
	return Record{
		ID:           id,
		Kind:         KindAfter,
		Title:        t,
		Prompt:       p,
		AfterSeconds: afterSeconds,
		ScheduledAt:  now.Add(time.Duration(afterSeconds) * time.Second).UTC(),
	}, nil
}

// CreateAt 构造一条从绝对时点创建的一次性提醒；at 必须是带偏移的 RFC 3339 且严格未来。
func CreateAt(id, prompt, title, at string, now time.Time) (Record, error) {
	t, err := ScheduleTitle(title)
	if err != nil {
		return Record{}, err
	}
	p, err := SchedulePrompt(prompt)
	if err != nil {
		return Record{}, err
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(at))
	if err != nil {
		return Record{}, inputError(CodeInvalidRule, "at must be an RFC 3339 date-time with an explicit offset.")
	}
	if !parsed.After(now) {
		return Record{}, inputError(CodeNotFuture, "at must be strictly in the future.")
	}
	return Record{
		ID:          id,
		Kind:        KindAt,
		Title:       t,
		Prompt:      p,
		ScheduledAt: parsed.UTC(),
	}, nil
}

// CreateEvery 构造一条按固定经过时间重复的提醒；间隔至少 MinEveryIntervalSeconds。
func CreateEvery(id, prompt, title string, everySeconds int64, now time.Time) (Record, error) {
	t, err := ScheduleTitle(title)
	if err != nil {
		return Record{}, err
	}
	p, err := SchedulePrompt(prompt)
	if err != nil {
		return Record{}, err
	}
	if everySeconds < MinEveryIntervalSeconds {
		return Record{}, inputError(CodeFrequencyTooHigh, "every_seconds must be at least %d.", MinEveryIntervalSeconds)
	}
	return Record{
		ID:           id,
		Kind:         KindEvery,
		Title:        t,
		Prompt:       p,
		EverySeconds: everySeconds,
		ScheduledAt:  now.Add(time.Duration(everySeconds) * time.Second).UTC(),
	}, nil
}

// ResolveOccurrence 解析一次到期：返回最近一次发生点与下一个未来目标（无则 nil）。
//
// after/at 为一次性：发生点即已提交目标，无下一个目标。
// every 用算术直接推进到最近一次，不枚举错过的发生时点。
func ResolveOccurrence(rec Record, acceptedAt time.Time) (Occurrence, error) {
	accepted := acceptedAt.UTC()
	target := rec.ScheduledAt.UTC()
	switch rec.Kind {
	case KindAfter, KindAt:
		if accepted.Before(target) {
			return Occurrence{}, logError("%s occurrence cannot precede the committed scheduledAt", rec.Kind)
		}
		return Occurrence{OccurrenceAt: target}, nil
	case KindEvery:
		interval := time.Duration(rec.EverySeconds) * time.Second
		if interval <= 0 {
			return Occurrence{}, logError("every interval must be positive")
		}
		if accepted.Before(target) {
			return Occurrence{}, logError("every occurrence cannot precede the committed scheduledAt")
		}
		steps := accepted.Sub(target) / interval
		occurrence := target.Add(steps * interval)
		next := occurrence.Add(interval)
		return Occurrence{OccurrenceAt: occurrence, NextScheduledAt: &next}, nil
	case KindDaily:
		return resolveDailyOccurrence(rec, accepted)
	case KindWeekly:
		return resolveWeeklyOccurrence(rec, accepted)
	case KindCron:
		return resolveCronOccurrence(rec, accepted)
	default:
		return Occurrence{}, inputError(CodeInvalidRule, "unsupported reminder kind %q", rec.Kind)
	}
}

// BuildView 把记录与墙钟组合为模型可见视图。
func BuildView(rec Record, now time.Time) View {
	state := StateScheduled
	if !now.UTC().Before(rec.ScheduledAt.UTC()) {
		state = StateOverdue
	}
	return View{Record: rec, State: state, DeliveryMode: DeliveryModeHost}
}

// RenderReminderFraming 渲染一次性提醒的固定投递文本。
func RenderReminderFraming(rec Record) string {
	return strings.Join([]string{
		"[SCHEDULE REMINDER]",
		"This is a scheduled message from the user",
		"schedule_id_json: " + jsonString(rec.ID),
		"occurrence_at: " + rec.ScheduledAt.UTC().Format(time.RFC3339Nano),
		"reminder_prompt_json: " + jsonString(rec.Prompt),
	}, "\n")
}

// ValidateRecord 校验一条持久记录（解码期使用）；非法即返回 LogError。
func ValidateRecord(rec Record) error {
	if strings.TrimSpace(rec.ID) == "" {
		return logError("record id is required")
	}
	title, err := ScheduleTitle(rec.Title)
	if err != nil || title != rec.Title {
		return logError("record %s has an invalid stored title", rec.ID)
	}
	if _, err := SchedulePrompt(rec.Prompt); err != nil {
		return logError("record %s has an invalid stored prompt", rec.ID)
	}
	if rec.ScheduledAt.IsZero() {
		return logError("record %s has no scheduledAt", rec.ID)
	}
	utc := rec.ScheduledAt.UTC()
	if utc.Year() < 1 || utc.Year() > 9999 {
		return logError("record %s scheduledAt is outside the four-digit-year range", rec.ID)
	}
	switch rec.Kind {
	case KindAfter:
		if rec.AfterSeconds <= 0 {
			return logError("record %s after_seconds must be positive", rec.ID)
		}
	case KindAt:
		// 无附加字段。
	case KindEvery:
		if rec.EverySeconds < MinEveryIntervalSeconds {
			return logError("record %s every_seconds must be at least %d", rec.ID, MinEveryIntervalSeconds)
		}
	case KindDaily:
		if strings.TrimSpace(rec.Time) == "" || strings.TrimSpace(rec.TimeZone) == "" {
			return logError("record %s daily rule needs time and timeZone", rec.ID)
		}
	case KindWeekly:
		if strings.TrimSpace(rec.Time) == "" || strings.TrimSpace(rec.TimeZone) == "" {
			return logError("record %s weekly rule needs time and timeZone", rec.ID)
		}
		if _, err := NormalizeWeekdays(rec.Weekdays); err != nil {
			return logError("record %s has an invalid weekday set", rec.ID)
		}
	case KindCron:
		if strings.TrimSpace(rec.Expression) == "" || strings.TrimSpace(rec.TimeZone) == "" {
			return logError("record %s cron rule needs expression and timeZone", rec.ID)
		}
		if canonical, err := ParseCron(rec.Expression); err != nil || canonical != rec.Expression {
			return logError("record %s cron expression is not canonical", rec.ID)
		}
	default:
		return logError("record %s has unknown kind %q", rec.ID, rec.Kind)
	}
	return nil
}

func jsonString(value string) string {
	// 仅需转义引号与反斜杠；内容为纯文本提示。
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + replacer.Replace(value) + `"`
}

func (r Record) String() string {
	return fmt.Sprintf("schedule{%s %s %s}", r.ID, r.Kind, r.ScheduledAt.UTC().Format(time.RFC3339))
}
