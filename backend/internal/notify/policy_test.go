package notify

import (
	"testing"
	"time"
)

func TestPriorityOfMapping(t *testing.T) {
	cases := []struct {
		kind string
		want Priority
	}{
		{"approval", PriorityHigh},
		{"question", PriorityHigh},
		{"error", PriorityHigh},
		{"completed", PriorityNormal},
		{"waiting", PriorityNormal},
		{"something_new", PriorityNormal},
		{"", PriorityLow},
		{"  ", PriorityLow},
	}
	for _, tc := range cases {
		if got := PriorityOf(tc.kind); got != tc.want {
			t.Fatalf("PriorityOf(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
	if !IsHighPriority("approval") || IsHighPriority("completed") {
		t.Fatal("IsHighPriority mapping drifted")
	}
	if PriorityHigh.String() != "high" || PriorityNormal.String() != "normal" || PriorityLow.String() != "low" {
		t.Fatal("priority string mapping drifted")
	}
}

func TestParseQuietHours(t *testing.T) {
	ranges, err := ParseQuietHours("22:00-08:00, 12:30-13:00")
	if err != nil {
		t.Fatalf("ParseQuietHours: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("want 2 windows, got %d", len(ranges))
	}
	if ranges[0].Start != 22*60 || ranges[0].End != 8*60 {
		t.Fatalf("first window = %+v", ranges[0])
	}
	if ranges[1].Start != 12*60+30 || ranges[1].End != 13*60 {
		t.Fatalf("second window = %+v", ranges[1])
	}

	if got, err := ParseQuietHours(""); err != nil || got != nil {
		t.Fatalf("empty spec = %v, %v", got, err)
	}
	for _, bad := range []string{"25:00-08:00", "nope", "10:00", "10:70-11:00"} {
		if _, err := ParseQuietHours(bad); err == nil {
			t.Fatalf("ParseQuietHours(%q) = nil error, want error", bad)
		}
	}
}

func TestQuietRangeContains(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 1, 1, h, m, 0, 0, time.Local)
	}
	overnight := QuietRange{Start: 22 * 60, End: 8 * 60}
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{
		{at(23, 0), true},
		{at(2, 30), true},
		{at(7, 59), true},
		{at(8, 0), false},
		{at(12, 0), false},
		{at(21, 59), false},
	} {
		if got := overnight.Contains(tc.t); got != tc.want {
			t.Fatalf("overnight.Contains(%s) = %v, want %v", tc.t, got, tc.want)
		}
	}
	daytime := QuietRange{Start: 12 * 60, End: 13 * 60}
	if !daytime.Contains(at(12, 30)) || daytime.Contains(at(13, 0)) || daytime.Contains(at(11, 59)) {
		t.Fatal("daytime window bounds are not half-open")
	}
	if (QuietRange{Start: 0, End: 0}).Contains(at(0, 0)) {
		t.Fatal("zero-width window must not contain any time")
	}
	if !InQuietHours([]QuietRange{overnight}, at(23, 30)) {
		t.Fatal("InQuietHours should match the overnight window")
	}
	if InQuietHours(nil, at(23, 30)) {
		t.Fatal("no windows means never quiet")
	}
}

func TestCoalesce(t *testing.T) {
	single := Coalesce("completed", []Notification{{Title: "A"}})
	if single.Title != "A" {
		t.Fatalf("single item should pass through, got %+v", single)
	}

	merged := Coalesce("completed", []Notification{
		{Title: "A", Urgency: "normal", TTL: time.Hour},
		{Title: "B", Urgency: "normal", TTL: time.Hour},
		{Title: "A", Urgency: "normal", TTL: time.Hour},
	})
	if merged.Title != "3 sessions completed" {
		t.Fatalf("merged title = %q", merged.Title)
	}
	if merged.Body != "A, B" {
		t.Fatalf("merged body = %q (want deduped titles)", merged.Body)
	}
	if merged.Tag != "batch|completed" {
		t.Fatalf("merged tag = %q", merged.Tag)
	}
	if merged.Urgency != "normal" || merged.TTL != time.Hour {
		t.Fatalf("merged delivery hints = %q/%v, want normal/1h", merged.Urgency, merged.TTL)
	}
}
