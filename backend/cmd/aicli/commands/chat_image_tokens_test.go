package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/keymap"
	"github.com/wwsheng009/ai-agent-runtime/internal/clipboardimage"
)

func TestParseChatImageTokenIndexes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []int
	}{
		{name: "空文本", text: "", want: nil},
		{name: "无令牌", text: "看看这张图", want: nil},
		{name: "单个令牌", text: "看看 [Image #1] 这张", want: []int{1}},
		{name: "多个令牌按出现顺序", text: "[Image #3] 先看 [Image #1]", want: []int{3, 1}},
		{name: "重复令牌去重", text: "[Image #2] [Image #2]", want: []int{2}},
		{name: "忽略 0 号", text: "[Image #0] [Image #2]", want: []int{2}},
		{name: "不同大小写不匹配", text: "[image #1]", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseChatImageTokenIndexes(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("parse(%q) = %v, want %v", tc.text, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parse(%q) = %v, want %v", tc.text, got, tc.want)
				}
			}
		})
	}
}

func TestInsertChatImageToken(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		cursor     int
		index      int
		wantText   string
		wantCursor int
	}{
		{name: "空文本", text: "", cursor: 0, index: 1, wantText: "[Image #1] ", wantCursor: 11},
		{name: "行尾补前导空格", text: "看图", cursor: 2, index: 1, wantText: "看图 [Image #1] ", wantCursor: 14},
		{name: "已有空格不重复补", text: "看图 ", cursor: 3, index: 2, wantText: "看图 [Image #2] ", wantCursor: 14},
		{name: "插在中间", text: "abcd", cursor: 2, index: 1, wantText: "ab [Image #1] cd", wantCursor: 14},
		{name: "已存在同序号不重复插入", text: "[Image #1] ", cursor: 11, index: 1, wantText: "[Image #1] ", wantCursor: 11},
		{name: "非法序号不动", text: "abc", cursor: 1, index: 0, wantText: "abc", wantCursor: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotCursor := insertChatImageToken(tc.text, tc.cursor, tc.index)
			if gotText != tc.wantText || gotCursor != tc.wantCursor {
				t.Fatalf("insert(%q,%d,#%d) = %q,%d want %q,%d", tc.text, tc.cursor, tc.index, gotText, gotCursor, tc.wantText, tc.wantCursor)
			}
		})
	}
}

func TestFilterChatImagePathsByDraft(t *testing.T) {
	tokenized := []string{"C:/img/a.png", "C:/img/b.png"}
	external := "C:/acp/c.png"
	buildSession := func(t *testing.T) *ChatSession {
		t.Helper()
		session := &ChatSession{ImagePaths: append(append([]string{}, tokenized...), external)}
		markChatImageTokenPath(session, tokenized[0], 1)
		markChatImageTokenPath(session, tokenized[1], 2)
		return session
	}

	t.Run("令牌都在则都保留", func(t *testing.T) {
		session := buildSession(t)
		got := filterChatImagePathsByDraft(session, "[Image #1] [Image #2] 说明")
		if len(got) != 3 {
			t.Fatalf("应保留全部附件: %+v", got)
		}
	})
	t.Run("删掉 2 号令牌即丢弃该附件", func(t *testing.T) {
		session := buildSession(t)
		got := filterChatImagePathsByDraft(session, "[Image #1] 说明")
		if len(got) != 2 || got[0] != tokenized[0] || got[1] != external {
			t.Fatalf("应保留 1 号附件与非令牌附件: %+v", got)
		}
		if _, ok := session.imageTokenPaths[tokenized[1]]; ok {
			t.Fatal("失效的令牌标记应被清理")
		}
	})
	t.Run("草稿里没有令牌时不做裁剪", func(t *testing.T) {
		session := buildSession(t)
		got := filterChatImagePathsByDraft(session, "只有文字")
		if len(got) != 3 {
			t.Fatalf("无令牌时不应裁剪（保守）: %+v", got)
		}
	})
	t.Run("删掉全部令牌时令牌附件全部丢弃", func(t *testing.T) {
		session := buildSession(t)
		got := filterChatImagePathsByDraft(session, "只留文字 [Image #9]")
		if len(got) != 1 || got[0] != external {
			t.Fatalf("只应保留非令牌附件: %+v", got)
		}
		if session.imageTokenPaths != nil {
			t.Fatalf("标记表应清空: %+v", session.imageTokenPaths)
		}
	})
	t.Run("无附件时返回空", func(t *testing.T) {
		session := &ChatSession{}
		if got := filterChatImagePathsByDraft(session, "[Image #1]"); got != nil {
			t.Fatalf("无附件应返回空: %+v", got)
		}
	})
}

func TestComposerClipboardActionInsertsImageToken(t *testing.T) {
	fixture := writeClipboardFixturePNG(t)
	stubClipboardImageRead(t, clipboardimage.Result{Path: fixture, Width: 2, Height: 2}, nil)

	session := &ChatSession{}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	composer := &chatComposerController{
		session:    session,
		prompt:     formatSessionUserPrompt(session),
		completion: newChatSlashCompletionController(session),
	}
	action := composer.hooks().OnActionKey
	if action == nil {
		t.Fatal("composer 应注册动作键钩子")
	}
	result := action(ui.LineEditorSnapshot{Text: "看这张", Cursor: len([]rune("看这张"))}, string(keymap.ActionClipboardImage))
	if !result.Claimed {
		t.Fatal("剪贴板动作应被认领")
	}
	if result.Replacement == nil {
		t.Fatal("附件成功后应在光标处插入令牌")
	}
	if !strings.Contains(result.Replacement.Text, "[Image #1]") {
		t.Fatalf("令牌未插入: %q", result.Replacement.Text)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("附件数量错误: %+v", session.ImagePaths)
	}
	if index := session.imageTokenPaths[session.ImagePaths[0]]; index != 1 {
		t.Fatalf("令牌标记应为 1，得到 %d", index)
	}
}

func TestComposerSubmitDropsAttachmentsWithDeletedTokens(t *testing.T) {
	session := &ChatSession{}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.ImagePaths = []string{"C:/img/a.png", "C:/img/b.png"}
	markChatImageTokenPath(session, session.ImagePaths[0], 1)
	markChatImageTokenPath(session, session.ImagePaths[1], 2)
	external := "C:/acp/c.png"
	session.ImagePaths = append(session.ImagePaths, external)
	composer := &chatComposerController{
		session:    session,
		prompt:     formatSessionUserPrompt(session),
		completion: newChatSlashCompletionController(session),
	}
	submit := composer.hooks().OnSubmit
	if submit == nil {
		t.Fatal("composer 应注册提交钩子")
	}
	if _, ok := submit(ui.LineEditorSnapshot{Text: "[Image #1] 保留第一张", Cursor: len([]rune("[Image #1] 保留第一张"))}); ok {
		t.Fatal("普通文本提交不应被命令接管")
	}
	if len(session.ImagePaths) != 2 || session.ImagePaths[0] != "C:/img/a.png" || session.ImagePaths[1] != external {
		t.Fatalf("提交时应丢弃被删令牌的附件、保留其它来源附件: %+v", session.ImagePaths)
	}
}

// 键入的 /attach <path> 在编辑器之外执行：新增附件后应把令牌写回下一次草稿，
// 这样用户可以直接删令牌弃图，而不是只能靠 /attach remove N。
func TestAppendChatImageTokenAfterCommandAddsAttachment(t *testing.T) {
	fixture := writeClipboardFixturePNG(t)
	session := &ChatSession{SessionDir: t.TempDir()}
	session.Interaction = newTestChatInteractionCoordinator(t, session)

	before := len(session.ImagePaths)
	if result := executeStructuredAttachmentCommand(session, "/attach "+fixture); len(result.Blocks) == 0 {
		t.Fatalf("/attach 应返回文本结果: %+v", result)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("/attach 未写入附件: %+v", session.ImagePaths)
	}
	if !appendChatImageTokenForNewAttachments(session, before) {
		t.Fatal("新增附件后应把令牌写回草稿")
	}
	draft := session.Interaction.PromptInputSnapshot().Text
	if !strings.Contains(draft, "[Image #1]") {
		t.Fatalf("草稿里应出现令牌: %q", draft)
	}
	if index := session.imageTokenPaths[session.ImagePaths[0]]; index != 1 {
		t.Fatalf("令牌标记应为 1，得到 %d", index)
	}
	// 没有新增附件时不应重复写入。
	if appendChatImageTokenForNewAttachments(session, len(session.ImagePaths)) {
		t.Fatal("无新增附件不应写回令牌")
	}
	if again := session.Interaction.PromptInputSnapshot().Text; again != draft {
		t.Fatalf("草稿不应被改写: %q → %q", draft, again)
	}
}
