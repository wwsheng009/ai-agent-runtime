package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 会话切换确认（/resume、/load、/new）在协调器信息流里都只提交一行摘要；
// plain/JSON 投影保留完整 meta 块，脚本可解析的 stdout 不变。resume 的两侧分别由
// chat_resume_confirmation_test.go 与 chat_timeline_command_test.go 覆盖，这里
// 补齐 /load 与 /new。

func TestBuildChatLoadDocumentPlainKeepsSessionMetaBlock(t *testing.T) {
	session := newSessionConfirmationMetaFixture(t)

	doc := buildChatLoadDocument(session, false)
	plain := ui.RenderDocumentPlain(doc)
	// plain 投影第一行必须保持旧标题，不能变成 unified 的
	// 「会话已加载: 标题（compact #N · X轮/Y条消息）」单行形状。
	if !strings.HasPrefix(plain, "会话已加载\n") {
		t.Fatalf("/load plain heading changed:\n%s", plain)
	}
	if doc.LineCount() < 2 {
		t.Fatalf("/load plain projection lost the session meta block: lines=%d\n%s", doc.LineCount(), plain)
	}
	for _, label := range []string{"Session:", "Title:", "Compact Gen:", "History:"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("/load plain projection lost session meta row %q:\n%s", label, plain)
		}
	}
}

func TestBuildChatNewSessionDocumentOneLineConfirmation(t *testing.T) {
	session := newSessionConfirmationMetaFixture(t)

	doc := buildChatNewSessionDocument(session, true)
	plain := ui.RenderDocumentPlain(doc)
	if doc.LineCount() != 1 {
		t.Fatalf("/new one-line confirmation lines=%d want 1:\n%s", doc.LineCount(), plain)
	}
	for _, marker := range []string{"已创建新会话", session.RuntimeSession.ID} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("/new one-line confirmation lost %q:\n%s", marker, plain)
		}
	}
	assertSessionConfirmationStreamClean(t, plain)
}

func TestBuildChatNewSessionDocumentPlainKeepsSessionMetaBlock(t *testing.T) {
	session := newSessionConfirmationMetaFixture(t)

	doc := buildChatNewSessionDocument(session, false)
	plain := ui.RenderDocumentPlain(doc)
	if !strings.HasPrefix(plain, "已创建新会话\n") {
		t.Fatalf("/new plain heading changed:\n%s", plain)
	}
	if !strings.Contains(plain, "Session:") {
		t.Fatalf("/new plain projection lost the session meta block:\n%s", plain)
	}
}
