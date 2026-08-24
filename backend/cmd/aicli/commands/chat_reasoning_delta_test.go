package commands

import "testing"

// reasoning chunk 的边界不是文本边界。仅保留旧版对“按行切块但不带换行”
// 的保守兼容；数字、时间戳和标点不能触发补行。
func TestNormalizeReasoningDeltaSeamIsConservative(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		delta    string
		want     string
	}{
		{"line boundary", "Inspecting buildSessionActor architecture", "Designing per-actor coordinator", "\nDesigning per-actor coordinator"},
		{"word continuation", "some architect", "ure here", "ure here"},
		{"existing ends newline", "line one\n", "line two", "line two"},
		{"delta starts space", "architecture", " designing", " designing"},
		{"first chunk", "", "Inspecting", "Inspecting"},
		{"CJK word chunk stays inline", "检查统一渲染器架构", "设计协调器", "设计协调器"},
		{"CJK sentence after 句号", "检查统一渲染器架构。", "设计协调器", "\n设计协调器"},
		{"CJK chunk after 冒号/middle-punct stays inline", "先确认状态", "当前目录", "当前目录"},
		{"CJK closing quote ends sentence", "用户问\u201c执行换行处理\u201d", "是否也存在问题", "\n是否也存在问题"},
		{"UUID boundary", "146eaa", "15-8b", "15-8b"},
		{"timestamp boundary", "2026-08-23T08", ":50-08:59 UTC", ":50-08:59 UTC"},
	}
	for _, tc := range cases {
		if got := normalizeReasoningDeltaSeam(tc.existing, tc.delta); got != tc.want {
			t.Errorf("%s: normalizeReasoningDeltaSeam(%q, %q) = %q, want %q", tc.name, tc.existing, tc.delta, got, tc.want)
		}
	}
}

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
