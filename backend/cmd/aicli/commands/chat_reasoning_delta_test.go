package commands

import "testing"

// 同一块的重复投递不得作为新 delta 追加（避免正文翻倍）。
func TestNormalizeAssistantStreamDeltaDuplicateTail(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		incoming string
		want     string
	}{
		{"duplicate tail", "alpha\nbeta\n", "beta\n", ""},
		{"prefix extension", "alpha\nbeta", "alpha\nbeta\ngamma", "\ngamma"},
		{"plain delta", "alpha", "beta", "beta"},
		{"empty existing", "", "alpha", "alpha"},
	}
	for _, tc := range cases {
		if got := normalizeAssistantStreamDelta(tc.existing, tc.incoming); got != tc.want {
			t.Errorf("%s: normalizeAssistantStreamDelta(%q, %q) = %q, want %q", tc.name, tc.existing, tc.incoming, got, tc.want)
		}
	}
}
