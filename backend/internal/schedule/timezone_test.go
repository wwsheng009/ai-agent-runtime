package schedule

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestCreateDailyComputesFirstFutureTarget(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rec, err := CreateDaily("schedule-1", "review", "Daily review", DailyInput{Time: "09:00:00", TimeZone: "Asia/Shanghai"}, now)
	if err != nil {
		t.Fatalf("CreateDaily: %v", err)
	}
	if rec.Kind != KindDaily || rec.Time != "09:00:00.000" || rec.TimeZone != "Asia/Shanghai" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	want := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC) // 09:00 CST
	if !rec.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %s, want %s", rec.ScheduledAt, want)
	}
}

func TestDailySkipsDSTGap(t *testing.T) {
	// America/New_York 2026-03-08 02:00 春季跳变：02:30 不存在，应跳过到 03-09。
	now := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	rec, err := CreateDaily("schedule-1", "x", "Gap", DailyInput{Time: "02:30:00", TimeZone: "America/New_York"}, now)
	if err != nil {
		t.Fatalf("CreateDaily: %v", err)
	}
	loc := mustLoad(t, "America/New_York")
	local := rec.ScheduledAt.In(loc)
	if local.Day() != 9 || local.Hour() != 2 || local.Minute() != 30 {
		t.Fatalf("gap not skipped: %s", local.Format(time.RFC3339))
	}
}

func TestDailyOverlapPicksEarlierInstant(t *testing.T) {
	// America/New_York 2026-11-01 秋季重叠：01:30 出现两次，取较早（EDT -04:00 = 05:30Z）。
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rec, err := CreateDaily("schedule-1", "x", "Overlap", DailyInput{Time: "01:30:00", TimeZone: "America/New_York"}, now)
	if err != nil {
		t.Fatalf("CreateDaily: %v", err)
	}
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	if !rec.ScheduledAt.Equal(want) {
		t.Fatalf("overlap = %s, want earlier instant %s", rec.ScheduledAt, want)
	}
}

func TestCreateWeeklyHonoursWeekdaySet(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) // 周日
	rec, err := CreateWeekly("schedule-1", "x", "Weekly", WeeklyInput{
		Time: "09:00:00", TimeZone: "Asia/Shanghai", Weekdays: []int{3, 1, 1},
	}, now)
	if err != nil {
		t.Fatalf("CreateWeekly: %v", err)
	}
	if len(rec.Weekdays) != 2 || rec.Weekdays[0] != 1 || rec.Weekdays[1] != 3 {
		t.Fatalf("weekdays must be normalized unique ascending, got %v", rec.Weekdays)
	}
	loc := mustLoad(t, "Asia/Shanghai")
	local := rec.ScheduledAt.In(loc)
	if local.Hour() != 9 {
		t.Fatalf("local time = %s, want 09:00", local.Format(time.RFC3339))
	}
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if wd := isoWeekday(day); wd != 1 && wd != 3 {
		t.Fatalf("target weekday = %d, want 1 or 3", wd)
	}
	if !rec.ScheduledAt.After(now) || rec.ScheduledAt.Sub(now) > 8*24*time.Hour {
		t.Fatalf("target not in the first week after now: %s", rec.ScheduledAt)
	}
}

func TestResolveDailyAdvancesToNextDay(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rec, err := CreateDaily("schedule-1", "x", "Daily", DailyInput{Time: "09:00:00", TimeZone: "Asia/Shanghai"}, now)
	if err != nil {
		t.Fatalf("CreateDaily: %v", err)
	}
	accepted := rec.ScheduledAt.Add(30 * time.Second)
	occ, err := ResolveOccurrence(rec, accepted)
	if err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	if !occ.OccurrenceAt.Equal(rec.ScheduledAt) {
		t.Fatalf("occurrence = %s, want the committed target", occ.OccurrenceAt)
	}
	if occ.NextScheduledAt == nil || !occ.NextScheduledAt.Equal(rec.ScheduledAt.Add(24*time.Hour)) {
		t.Fatalf("next = %v, want +24h", occ.NextScheduledAt)
	}
}

func TestNormalizeWeekdaysRejectsInvalid(t *testing.T) {
	if _, err := NormalizeWeekdays(nil); err == nil {
		t.Fatal("empty set must be rejected")
	}
	if _, err := NormalizeWeekdays([]int{0}); err == nil {
		t.Fatal("0 must be rejected")
	}
	if _, err := NormalizeWeekdays([]int{8}); err == nil {
		t.Fatal("8 must be rejected")
	}
}

func TestParseLocalTimeRejectsOutOfRange(t *testing.T) {
	if _, _, err := parseLocalTime("24:00:00"); err == nil {
		t.Fatal("24:00 must be rejected")
	}
	if _, _, err := parseLocalTime("09:00"); err == nil {
		t.Fatal("missing seconds must be rejected")
	}
	if _, normalized, err := parseLocalTime("23:00:00.5"); err != nil || normalized != "23:00:00.500" {
		t.Fatalf("fractional normalization = %q, err = %v", normalized, err)
	}
}

func TestParseTimeZoneRejectsUnknown(t *testing.T) {
	if _, err := ParseTimeZone(""); err == nil {
		t.Fatal("empty zone must be rejected")
	}
	if _, err := ParseTimeZone("Mars/Olympus"); err == nil {
		t.Fatal("unknown zone must be rejected")
	}
}
