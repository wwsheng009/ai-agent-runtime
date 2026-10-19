package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// A→B→A 的开关信号：只有授权式装载真正切到不同 runtime session 时才开启
// 新的交付代次。同会话重载、大小写等价的 ID、空身份都不开——空身份还必须
// 保留上一次已知身份，否则下一次真实装载会被误判成「首次发布」。
func TestNoteArmedTranscriptSessionOpensEpochOnlyOnSwitch(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-a"}}
	c := &chatInteractionCoordinator{session: session}

	if c.noteArmedTranscriptSession() {
		t.Fatal("first armed transcript post must not open a delivery epoch")
	}
	if c.noteArmedTranscriptSession() {
		t.Fatal("same-session armed reload must not open a delivery epoch")
	}
	setChatRuntimeSessionPointer(session, &runtimechat.Session{ID: "SESSION-A"})
	if c.noteArmedTranscriptSession() {
		t.Fatal("case-equivalent runtime session id must not open a delivery epoch")
	}

	// 空身份不携带会话信息：既不开启代次，也不能清掉已知身份。
	setChatRuntimeSessionPointer(session, &runtimechat.Session{})
	if c.noteArmedTranscriptSession() {
		t.Fatal("empty runtime session id must not open a delivery epoch")
	}
	setChatRuntimeSessionPointer(session, &runtimechat.Session{ID: "session-a"})
	if c.noteArmedTranscriptSession() {
		t.Fatal("empty-id post must not clear the tracked session identity")
	}

	setChatRuntimeSessionPointer(session, &runtimechat.Session{ID: "session-b"})
	if !c.noteArmedTranscriptSession() {
		t.Fatal("switch to a different runtime session must open a delivery epoch")
	}
	if c.noteArmedTranscriptSession() {
		t.Fatal("repeat armed reload in the new session must not open another epoch")
	}

	setChatRuntimeSessionPointer(session, &runtimechat.Session{ID: "session-a"})
	if !c.noteArmedTranscriptSession() {
		t.Fatal("switch back to the first session must open a delivery epoch")
	}
}
