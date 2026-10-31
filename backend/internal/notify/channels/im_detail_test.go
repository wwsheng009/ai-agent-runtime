package channels

// im_detail_test.go — 「已完成」通知附带最后一条助手回复在 IM 通道的渲染回归
// （docs/gateway/08-notifications.md §5.1；2026-10-31 真机复盘）。

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// TestIMCompletedDetailAppendedAfterProjection 锁定「已完成」附带的会话专属正文
// 不会因为投影文案替换 Notification.Body 而丢失，并且正文已含同一内容时不重复拼接。
func TestIMCompletedDetailAppendedAfterProjection(t *testing.T) {
	rec := &outboundRecorder{}
	targets := []IMTarget{{ID: "t1", ConnectorID: "c1", Chat: "chat-1", Source: "gateway", Scope: "all", Kinds: []string{"completed"}}}
	im := &IM{SendOutbound: rec.send, Targets: targets}

	detail := "结论：微信通知已经修好"
	if err := im.Deliver(context.Background(), notify.Event{
		Kind:   "completed",
		Target: "s1",
		// 宿主已把内容拼进了 Body（webpush/ntfy 口径），IM 侧必须仍能看到它。
		Notification: notify.Notification{Title: "会话标题", Body: "laptop: completed\n" + detail},
		Detail:       detail,
	}); err != nil {
		t.Fatal(err)
	}
	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("应投递一次，实际 %v", calls)
	}
	text := calls[0]
	if !strings.Contains(text, "会话标题") {
		t.Fatalf("标题缺失：%q", text)
	}
	if !strings.Contains(text, "已完成") {
		t.Fatalf("投影文案缺失：%q", text)
	}
	if !strings.Contains(text, detail) {
		t.Fatalf("会话专属正文必须出现在 IM 正文里：%q", text)
	}
	if strings.Count(text, detail) != 1 {
		t.Fatalf("会话专属正文重复拼接：%q", text)
	}

	// 对照：无 Detail 时保持既有形态（投影命中即不回退宿主正文）。
	rec2 := &outboundRecorder{}
	im2 := &IM{SendOutbound: rec2.send, Targets: targets}
	if err := im2.Deliver(context.Background(), notify.Event{
		Kind:         "completed",
		Target:       "s1",
		Notification: notify.Notification{Title: "会话标题", Body: "laptop: completed"},
	}); err != nil {
		t.Fatal(err)
	}
	plain := rec2.calls()[0]
	if strings.Contains(plain, "laptop") {
		t.Fatalf("投影命中时不应回退到宿主正文：%q", plain)
	}
}