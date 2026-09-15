package chat

import "testing"

func TestIsAddressableSessionID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		// 规范 ID 与 agent 派生会话必须保持可用：列表/加载依赖它们原样读回。
		{"session_20260915_ab12cd34", true},
		{"lead", true},
		{"note-a", true},
		{"go_tail_tests", true},
		{"a b", true},

		// 路径分隔符：写入保留原串，读取先取最后一段，落库即孤儿。
		{"/root/p26s3b", false},
		{"dir/abc", false},
		{`dir\abc`, false},
		{"abc/", false},
		{`abc\`, false},

		// nil 渲染残留与空白：不应作为会话 ID。
		{"<nil>", false},
		{"<NIL>", false},
		{"  spaced  ", false},
		{"", false},
		{"   ", false},
	}

	for _, tc := range cases {
		if got := IsAddressableSessionID(tc.id); got != tc.want {
			t.Errorf("IsAddressableSessionID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// 不变式：任何被判定为可寻址的 ID，规范化后必须与原串完全一致，
// 否则“可寻址”这一判断本身就是错的（存储层读取时会换成另一个键）。
func TestIsAddressableSessionIDImpliesNormalizationIsIdentity(t *testing.T) {
	for _, id := range []string{
		"session_20260915_ab12cd34",
		"lead",
		"note-a",
		"a b",
		"中文会话",
	} {
		if !IsAddressableSessionID(id) {
			t.Fatalf("IsAddressableSessionID(%q) = false, want true", id)
		}
		if normalized := NormalizeSessionID(id); normalized != id {
			t.Fatalf("NormalizeSessionID(%q) = %q, want identity for addressable ids", id, normalized)
		}
	}
}
