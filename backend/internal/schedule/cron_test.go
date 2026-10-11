package schedule

import (
	"testing"
	"time"
)

func TestParseCronCanonicalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"*/15 9-17 * * 1-5", "*/15 9-17 * * 1-5"},
		{"*/15 09-17 * * 1,1,2-5", "*/15 9-17 * * 1-5"},
		{"0 0 * * 7", "0 0 * * 0"},
		{"0 0 1,2,3,4,5 * *", "0 0 1-5 * *"},
		{"*/2 * * * *", "*/2 * * * *"},
		{"5,10,15,20,25 * * * *", "5-25/5 * * * *"},
		{"* 1,3,5,7,9,11 * * *", "* 1-11/2 * * *"},
		{"0 0 1 1 0", "0 0 1 1 0"},
		{"30 8 1,15 * 1-5", "30 8 1,15 * 1-5"},
	}
	for _, tc := range cases {
		got, err := ParseCron(tc.in)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseCron(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseCronRejectsUnsupported(t *testing.T) {
	bad := []string{
		"* * * *",     // 四字段
		"* * * * * *", // 六字段
		"0 0 * * L",   // L
		"0 0 * * 1#2", // #
		"0 0 * * MON", // 名称
		"@daily",      // 宏
		"60 * * * *",  // minute 越界
		"* 24 * * *",  // hour 越界
		"0 0 0 * *",   // dom 越界
		"0 0 * 13 *",  // month 越界
		"0 0 * * 8",   // dow 越界
		"5-1 * * * *", // 反向范围
		"*/0 * * * *", // 零步长
		"1/2 * * * *", // 单值带步长
		"0 0 1- * *",  // 空边界
		"* * * * *,-", // 空元素
	}
	for _, expression := range bad {
		if _, err := ParseCron(expression); err == nil {
			t.Fatalf("ParseCron(%q) must be rejected", expression)
		}
	}
}

func TestCreateCronStoresCanonicalExpression(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rec, err := CreateCron("schedule-1", "deploy", "Deploy check", CronInput{
		Expression: "*/15 09-17 * * 1,1,2-5", TimeZone: "Asia/Shanghai",
	}, now)
	if err != nil {
		t.Fatalf("CreateCron: %v", err)
	}
	if rec.Expression != "*/15 9-17 * * 1-5" {
		t.Fatalf("expression = %q, want canonical", rec.Expression)
	}
	if rec.Kind != KindCron {
		t.Fatalf("kind = %s", rec.Kind)
	}
	// 目标必须严格未来且落在匹配分钟。
	if !rec.ScheduledAt.After(now) {
		t.Fatalf("target must be future, got %s", rec.ScheduledAt)
	}
	loc := mustLoad(t, "Asia/Shanghai")
	local := rec.ScheduledAt.In(loc)
	if local.Minute()%15 != 0 || local.Hour() < 9 || local.Hour() > 17 {
		t.Fatalf("target does not match the rule: %s", local.Format(time.RFC3339))
	}
}

func TestCronResolveAdvancesToNextMatch(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rec, err := CreateCron("schedule-1", "x", "Cron", CronInput{
		Expression: "*/15 9-17 * * 1-5", TimeZone: "Asia/Shanghai",
	}, now)
	if err != nil {
		t.Fatalf("CreateCron: %v", err)
	}
	accepted := rec.ScheduledAt.Add(90 * time.Second)
	occ, err := ResolveOccurrence(rec, accepted)
	if err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	if !occ.OccurrenceAt.Equal(rec.ScheduledAt) {
		t.Fatalf("occurrence = %s, want committed target", occ.OccurrenceAt)
	}
	if occ.NextScheduledAt == nil {
		t.Fatal("cron must always have a next target")
	}
	next := occ.NextScheduledAt.In(mustLoad(t, "Asia/Shanghai"))
	if next.Sub(rec.ScheduledAt.In(mustLoad(t, "Asia/Shanghai"))) != 15*time.Minute {
		t.Fatalf("next = %s, want +15m", next.Format(time.RFC3339))
	}
}

func TestCronDOMDOWVixieSemantics(t *testing.T) {
	loc := mustLoad(t, "UTC")
	sets, starred, err := parseCronExpression("0 0 1 * 1")
	if err != nil {
		t.Fatalf("parseCronExpression: %v", err)
	}
	// dom=1, dow=月一；两者均非星号 → 任一匹配即可。
	firstOfMonth := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // 周日
	monday := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	other := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) // 周二
	if !cronDateMatches(firstOfMonth, sets, starred, loc) {
		t.Fatal("dom match alone must pass (neither field starred)")
	}
	if !cronDateMatches(monday, sets, starred, loc) {
		t.Fatal("dow match alone must pass (neither field starred)")
	}
	if cronDateMatches(other, sets, starred, loc) {
		t.Fatal("no match must fail")
	}

	// dom=* → 两者都需匹配（dow 受限）。
	sets2, starred2, _ := parseCronExpression("0 0 * * 1")
	if !cronDateMatches(monday, sets2, starred2, loc) {
		t.Fatal("monday must match when dom is starred")
	}
	if cronDateMatches(firstOfMonth, sets2, starred2, loc) {
		t.Fatal("sunday must not match when dom is starred and dow=1")
	}
}

func TestValidateRecordRejectsNonCanonicalCron(t *testing.T) {
	rec := Record{ID: "s", Kind: KindCron, Title: "t", Prompt: "p",
		Expression: "*/15 09-17 * * 1-5", TimeZone: "Asia/Shanghai",
		ScheduledAt: time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)}
	if err := ValidateRecord(rec); err == nil {
		t.Fatal("non-canonical stored expression must be rejected")
	}
}
