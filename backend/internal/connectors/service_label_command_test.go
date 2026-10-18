// 中文标签别名（2026-10-17 真机）：QQ 指令面板 type=command 元素点击后发送
// 元素名称（中文 Label）——命令层必须归一为核心命令，而不是落进 agent 回合。
// 真机证据：点「状态」触发了一次 agent 仓库状态调查、点「帮助」得到 agent
// 自拟的帮助文本（都在普通 turn 里烧掉）。

package connectors

import (
	"context"
	"strings"
	"testing"
)

func TestParseCommandChineseLabelAliases(t *testing.T) {
	cases := []struct {
		text string
		name string
		args string
		ok   bool
	}{
		{"帮助", "help", "", true},
		{"/帮助", "help", "", true},
		{"新会话", "new", "", true},
		{"状态", "status", "", true},
		{"批准", "approve", "", true},
		{"拒绝", "deny", "", true},
		{"邀请", "invite", "", true},
		{"回退", "undo", "", true},
		{"回退 2", "undo", "2", true},
		{"回答 1 好的", "answer", "1 好的", true},
		{"/help", "help", "", true}, // 既有 slash 路径不受影响
		{"帮助我看看这个", "", "", false},
		{"状态如何", "", "", false},
		{"hello", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		cmd, ok := parseCommand("/", tc.text)
		if ok != tc.ok {
			t.Fatalf("parseCommand(%q) ok=%v, want %v", tc.text, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if cmd.name != tc.name || cmd.args != tc.args {
			t.Fatalf("parseCommand(%q) = %+v, want name=%q args=%q", tc.text, cmd, tc.name, tc.args)
		}
	}
	// 面板点击的中文标签同样按核心命令对待（stats 不计普通 turn 计数）。
	if !isCoreCommand("/", "帮助") || !isCoreCommand("/", "状态") || !isCoreCommand("/", "回退 2") {
		t.Fatal("chinese labels must be recognized as core commands")
	}
}

// 端到端：中文标签命令在命令层闭环，不进入 agent 回合（本机 backend 零调用）。
func TestServiceChineseLabelCommandDoesNotEnterAgent(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "帮助")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "help reply", func() bool {
		for _, text := range provider.sentTexts() {
			if strings.Contains(text, "/help") {
				return true
			}
		}
		return false
	})
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("chinese label command must not enter agent turn, backend calls = %d", len(requests))
	}
}
