package siteaccount

import (
	"strings"
	"testing"
)

func TestFormatBalanceAmount_TwoDecimals(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{5.5, "5.50"},
		{5.50, "5.50"},
		{12.34, "12.34"},
		{9, "9.00"},
		{0, "0.00"},
		{1.234, "1.23"},
		{1.235, "1.24"},
	}
	for _, tc := range cases {
		if got := FormatBalanceAmount(tc.in); got != tc.want {
			t.Fatalf("FormatBalanceAmount(%v)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatBalanceLine_TwoDecimals(t *testing.T) {
	remaining := 5.5
	view := NormalizeAccountView(&AccountSnapshot{
		SiteType:         SiteTypeSub2API,
		Source:           "sub2api",
		Mode:             "subscription",
		QuotaRemaining:   &remaining,
		QuotaDisplayUnit: "USD",
	}, ConfidenceHigh)
	line := FormatBalanceLine(view)
	if !strings.Contains(line, "5.50") {
		t.Fatalf("expected two-decimal balance, got %q", line)
	}
	if strings.Contains(line, "5.5 ") {
		t.Fatalf("unexpected single-decimal formatting, got %q", line)
	}
}
