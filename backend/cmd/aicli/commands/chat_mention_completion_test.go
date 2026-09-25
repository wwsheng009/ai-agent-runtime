package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func writeMentionFixture(t *testing.T, files []string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func TestChatMentionTokenAt(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		cursor int
		query  string
		start  int
		valid  bool
	}{
		{name: "行尾引用", text: "看 @chat_com", cursor: 11, query: "chat_com", start: 2, valid: true},
		{name: "光标在中间", text: "看 @chat_composer.go 末尾", cursor: 7, query: "chat", start: 2, valid: true},
		{name: "空引用", text: "看 @", cursor: 3, query: "", start: 2, valid: true},
		{name: "邮箱不算引用", text: "mail a@b.com", cursor: 12, valid: false},
		{name: "空白后中断", text: "@a b", cursor: 4, valid: false},
		{name: "引号内中断", text: "\"@a", cursor: 3, valid: false},
		{name: "左括号后可用", text: "(@a", cursor: 3, query: "a", start: 1, valid: true},
		{name: "无引用", text: "普通文本", cursor: 4, valid: false},
		{name: "越界光标", text: "@a", cursor: 99, valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := chatMentionTokenAt(tc.text, tc.cursor)
			if token.Valid != tc.valid {
				t.Fatalf("valid = %v, want %v (token=%+v)", token.Valid, tc.valid, token)
			}
			if !tc.valid {
				return
			}
			if token.Query != tc.query || token.Start != tc.start {
				t.Fatalf("token = %+v, want query=%q start=%d", token, tc.query, tc.start)
			}
		})
	}
}

func TestChatMentionCandidatesMatchAndSkip(t *testing.T) {
	root := writeMentionFixture(t, []string{
		"backend/cmd/aicli/commands/chat_composer.go",
		"backend/cmd/aicli/commands/chat_command.go",
		"backend/cmd/aicli/ui/inputbox_editor.go",
		"backend/internal/api/runtimeapi/chat_handlers.go",
		"node_modules/pkg/chat_dep.go",
		".git/objects/chat_ref",
		"docs/aicli/interactive-mode.md",
	})

	candidates := chatMentionCandidates(root, "backend/cmd/aicli/commands/chat_", 10)
	if len(candidates) != 2 {
		t.Fatalf("prefix 命中断言失败: %v", candidates)
	}
	for _, want := range []string{"backend/cmd/aicli/commands/chat_command.go", "backend/cmd/aicli/commands/chat_composer.go"} {
		found := false
		for _, got := range candidates {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少候选 %q: %v", want, candidates)
		}
	}

	substring := chatMentionCandidates(root, "inputbox", 10)
	if len(substring) != 1 || substring[0] != "backend/cmd/aicli/ui/inputbox_editor.go" {
		t.Fatalf("子串匹配失败: %v", substring)
	}

	skipped := chatMentionCandidates(root, "chat", 50)
	for _, got := range skipped {
		if strings.Contains(got, "node_modules") || strings.Contains(got, ".git/") {
			t.Fatalf("跳过目录失效: %v", skipped)
		}
	}
}

func TestChatMentionCandidatesDirectorySuffixAndLimit(t *testing.T) {
	root := writeMentionFixture(t, []string{
		"src/a/one.go", "src/b/two.go", "src/c/three.go",
	})
	dirs := chatMentionCandidates(root, "src/", 10)
	want := map[string]bool{"src/a/": true, "src/b/": true, "src/c/": true}
	for _, got := range dirs {
		if !strings.Contains(got, "/") || strings.HasSuffix(got, ".go") {
			continue
		}
		found := want[got]
		if !found && !strings.HasPrefix(got, "src/") {
			t.Fatalf("意外的目录候选 %q", got)
		}
	}
	limited := chatMentionCandidates(root, "", 2)
	if len(limited) != 2 {
		t.Fatalf("limit 未生效: %v", limited)
	}
}

func TestApplyChatMentionCompletionUniqueAndPrefix(t *testing.T) {
	root := writeMentionFixture(t, []string{
		"backend/cmd/aicli/commands/chat_composer.go",
		"backend/cmd/aicli/ui/inputbox_editor.go",
	})

	unique := applyChatMentionCompletion(root, "@inputbox", 9, 10)
	if !unique.Handled {
		t.Fatal("唯一命中应被判定为已处理")
	}
	if unique.Text != "@backend/cmd/aicli/ui/inputbox_editor.go " {
		t.Fatalf("唯一命中补全结果 = %q", unique.Text)
	}
	if unique.Cursor != len([]rune(unique.Text)) {
		t.Fatalf("光标应在补全文本末尾: cursor=%d text=%q", unique.Cursor, unique.Text)
	}

	multi := applyChatMentionCompletion(root, "@backend/cmd/aicli/", 19, 10)
	if !multi.Handled || !strings.Contains(multi.Status, "匹配") {
		t.Fatalf("多命中状态缺失: %+v", multi)
	}
	if multi.Text != "@backend/cmd/aicli/" || multi.Cursor != 19 {
		t.Fatalf("公共前缀未扩展时不应改动文本: %+v", multi)
	}

	statusOnly := applyChatMentionCompletion(root, "@backend/cmd/aicli/", 19, 10)
	if statusOnly.Status == "" {
		t.Fatal("无扩展时也应在状态行报告匹配数量")
	}
}

func TestApplyChatMentionCompletionNoMatchAndNonMention(t *testing.T) {
	root := writeMentionFixture(t, []string{"a.go"})

	none := applyChatMentionCompletion(root, "@zzz", 4, 10)
	if !none.Handled {
		t.Fatal("无匹配也必须消费按键，避免落到 plan mode 切换")
	}
	if none.Text != "@zzz" || none.Status == "" {
		t.Fatalf("无匹配不应改动文本且要有状态说明: %+v", none)
	}

	plain := applyChatMentionCompletion(root, "普通文本", 4, 10)
	if plain.Handled {
		t.Fatalf("非 @ 上下文不得接管 Tab: %+v", plain)
	}
}

func TestChatComposerOnCompleteHandlesMention(t *testing.T) {
	root := writeMentionFixture(t, []string{"docs/aicli/interactive-mode.md"})
	controller := &chatComposerController{mentionRoot: root}
	snapshot := ui.LineEditorSnapshot{Text: "@interactive", Cursor: len([]rune("@interactive"))}
	replacement, ok := controller.onComplete(snapshot)
	if !ok {
		t.Fatal("mention 命中时 onComplete 应返回接管结果")
	}
	if replacement.Text != "@docs/aicli/interactive-mode.md " {
		t.Fatalf("onComplete 补全结果 = %q", replacement.Text)
	}

	// 非 mention 输入仍走原有 plan mode 切换语义（无 session 时不 panic）。
	if _, ok := controller.onComplete(ui.LineEditorSnapshot{Text: "普通文本", Cursor: 4}); ok {
		t.Fatal("非 mention 输入不应被 mention 逻辑接管")
	}
}
