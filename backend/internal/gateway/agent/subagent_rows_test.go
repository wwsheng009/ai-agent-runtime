package agent

import "testing"

func TestReportSubagentsEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"1":     true,
		"true":  true,
		"on":    true,
		"YES":   true,
		"0":     false,
		"false": false,
		"off":   false,
		"no":    false,
	}
	for raw, want := range cases {
		getenv := func(string) string { return raw }
		if got := ReportSubagentsEnabled(getenv); got != want {
			t.Fatalf("ReportSubagentsEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}
