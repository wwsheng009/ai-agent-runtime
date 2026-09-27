package policy

import "testing"

// §4.12：CommandCode / Claude Code 的兼容别名必须映射到既有 4+1 模式，
// 且别名不会让未知值变成「已识别」（否则拼错的模式会静默降级）。
func TestParseModeAcceptsCompatibilityAliases(t *testing.T) {
	cases := []struct {
		raw  string
		want Mode
	}{
		{"manual", ModeDefault},
		{"  STANDARD ", ModeDefault},
		{"auto-accept", ModeAcceptEdits},
		{"acceptEdits", ModeAcceptEdits},
		{"bypass", ModeBypassPermissions},
		{"dontAsk", ModeDontAsk},
		{"dont-ask", ModeDontAsk},
		// canonical 值不受影响
		{"default", ModeDefault},
		{"accept_edits", ModeAcceptEdits},
		{"plan", ModePlan},
		{"bypass_permissions", ModeBypassPermissions},
		{"dont_ask", ModeDontAsk},
	}
	for _, item := range cases {
		got, ok := ParseMode(item.raw)
		if !ok || got != item.want {
			t.Fatalf("ParseMode(%q) = (%q,%v), want (%q,true)", item.raw, got, ok, item.want)
		}
	}
}

func TestParseModeStillRejectsUnknownValues(t *testing.T) {
	for _, raw := range []string{"", "bogus", "yolo!", "acceptedits2", "bypass2", "autoaccept", "readonly"} {
		if mode, ok := ParseMode(raw); ok {
			t.Fatalf("ParseMode(%q) = (%q,true), want rejected", raw, mode)
		}
	}
}

// normalizeMode 是内部的宽容归一（未知值 → default），必须与 ParseMode 对别名
// 保持一致，否则「解析通过但求值按 default」会在两侧漂移。
func TestNormalizeModeAgreesWithParseModeOnAliases(t *testing.T) {
	for _, raw := range []string{"manual", "auto-accept", "acceptEdits", "bypass", "dontAsk", "bypass_permissions", "plan", "bogus"} {
		parsed, ok := ParseMode(raw)
		if !ok {
			parsed = ModeDefault
		}
		if got := normalizeMode(Mode(raw)); got != parsed {
			t.Fatalf("normalizeMode(%q) = %q, want %q (ParseMode agreement)", raw, got, parsed)
		}
	}
}
