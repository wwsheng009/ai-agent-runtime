package agent

import "testing"

func TestReportSubagentsEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"1":     true,
		"true":  true,
		"on":    true,
		"YES":   true,
	}
	for raw, want := range cases {
		getenv := func(string) string { return raw }
		if got := ReportSubagentsEnabled(getenv); got != want {
			t.Fatalf("ReportSubagentsEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}
