package main

// push_completed_detail_test.go — 「已完成」通知附带最后一条助手回复的**通道级**
// 回归（docs/gateway/08-notifications.md §5.1）。
//
// 真机复盘（2026-10-31）：策略已开、push.go 也已算出内容并拼进 Notification.Body，
// 但 IM 通道的 messageText 会用投影文案（"✅ 会话回合已完成"）**替换** Body——于是
// QQ/微信上看到的消息只有那一行，助手回复整体消失（webpush/ntfy 被 muted，IM 是
// 唯一出口）。修复：内容另以 Event.Detail 透传，通道渲染时追加。
//
// 本用例从 pushNotifier.observe 一路断言到 IM 出站文本，锁死这条链路。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestPushNotifierCompletedDetailReachesIM(t *testing.T) {
	dir := t.TempDir()
	imPath := filepath.Join(dir, "gateway-im.json")
	raw := `{"version":9,"targets":[{"id":"t1","connector_id":"c1","chat":"chat-1",` +
		`"label":"ops","source":"gateway","scope":"all","kinds":["completed"],` +
		`"ignore_presence":true,"enabled":true}]}`
	if err := os.WriteFile(imPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	on, limit := true, 200
	policy := newNotifyPolicySource(filepath.Join(dir, "notify-policy.json"), "")
	if err := policy.Apply(notifyPolicyFile{Global: notifyPolicyScope{
		CompletedIncludeLastMessage:  &on,
		CompletedLastMessageMaxChars: &limit,
	}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var texts []string
	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.imSource = newNotifyIMSource(imPath)
	n.policy = policy
	n.sendOutbound = func(_ context.Context, _, _ string, msgs []connectors.OutboundMessage) error {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			texts = append(texts, m.Text)
		}
		return nil
	}

	reply := "结论：微信通知已经修好"
	n.observe([]*tunnel.Node{{NodeID: "n1", Name: "laptop", Sessions: []wire.SessionSummary{{
		SessionID:       "s1",
		Title:           "A",
		Attention:       "completed",
		LastMessageRole: "assistant",
		LastMessage:     reply,
	}}}})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(texts) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	got := append([]string(nil), texts...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("配置了 gateway target 的已完成事件未投递到 IM")
	}
	if !strings.Contains(got[0], reply) {
		t.Fatalf("IM 正文必须带最后一条助手回复，实际 %q", got[0])
	}
	if !strings.Contains(got[0], "已完成") {
		t.Fatalf("投影文案（✅ 会话回合已完成）不应丢，实际 %q", got[0])
	}
	if strings.Count(got[0], reply) != 1 {
		t.Fatalf("会话专属正文不应重复，实际 %q", got[0])
	}
}

// TestPushNotifierCompletedDetailOffKeepsNoDetail 对照：策略关闭时不带任何
// 助手内容（Detail 与 Body 都不追加），IM 文本保持既有形态。
func TestPushNotifierCompletedDetailOffKeepsNoDetail(t *testing.T) {
	dir := t.TempDir()
	imPath := filepath.Join(dir, "gateway-im.json")
	raw := `{"version":9,"targets":[{"id":"t1","connector_id":"c1","chat":"chat-1",` +
		`"source":"gateway","scope":"all","kinds":["completed"],"ignore_presence":true,"enabled":true}]}`
	if err := os.WriteFile(imPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var texts []string
	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.imSource = newNotifyIMSource(imPath)
	n.policy = newNotifyPolicySource(filepath.Join(dir, "notify-policy.json"), "")
	n.sendOutbound = func(_ context.Context, _, _ string, msgs []connectors.OutboundMessage) error {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			texts = append(texts, m.Text)
		}
		return nil
	}

	reply := "不应出现的内容"
	n.observe([]*tunnel.Node{{NodeID: "n1", Name: "laptop", Sessions: []wire.SessionSummary{{
		SessionID:       "s1",
		Title:           "A",
		Attention:       "completed",
		LastMessageRole: "assistant",
		LastMessage:     reply,
	}}}})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(texts) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	got := append([]string(nil), texts...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("未投递")
	}
	if strings.Contains(got[0], reply) {
		t.Fatalf("策略关闭时不得附带助手内容，实际 %q", got[0])
	}
}