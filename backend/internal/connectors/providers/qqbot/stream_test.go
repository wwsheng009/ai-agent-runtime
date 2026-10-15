package qqbot

// 流式消息（stream_messages，仅 C2C）单测：
//   - replace 模式分片：index 递增 / input_state 1→10 / 首片回填 stream_msg_id /
//     msg_seq 全片段一致 / 重复与缩短文本不发片（前缀约束 40007 防御）；
//   - markdown_support 切换 content_type；
//   - 群聊与无被动上下文的单聊走委托会话（不发流式，Final 走普通发送）。

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestStreamSessionC2CReplaceFragments(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:user-openid-1", "evt-1")

	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}
	if err := session.Update(context.Background(), "第一段"); err != nil {
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "第一段"); err != nil { // 重复：不发
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "第一段第二段"); err != nil {
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "第一"); err != nil { // 缩短：不发（前缀约束）
		t.Fatal(err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{Text: "第一段第二段最终"}}); err != nil {
		t.Fatal(err)
	}

	fragments := fake.streamFragmentPayloads()
	if len(fragments) != 3 {
		t.Fatalf("fragments = %d, want 3: %+v", len(fragments), fragments)
	}
	first := fragments[0]
	if first["index"] != float64(0) || first["input_state"] != float64(1) ||
		first["input_mode"] != "replace" || first["content_type"] != "text" ||
		first["content_raw"] != "第一段" || first["msg_id"] != "evt-1" {
		t.Fatalf("first fragment = %+v", first)
	}
	if _, ok := first["stream_msg_id"]; ok {
		t.Fatalf("first fragment must not carry stream_msg_id: %+v", first)
	}
	second := fragments[1]
	if second["index"] != float64(1) || second["stream_msg_id"] != "stream-1" ||
		second["content_raw"] != "第一段第二段" || second["input_state"] != float64(1) {
		t.Fatalf("second fragment = %+v", second)
	}
	last := fragments[2]
	if last["index"] != float64(2) || last["input_state"] != float64(10) ||
		last["content_raw"] != "第一段第二段最终" || last["stream_msg_id"] != "stream-1" {
		t.Fatalf("final fragment = %+v", last)
	}
	for i, fragment := range fragments {
		if fragment["msg_seq"] != float64(1) {
			t.Fatalf("fragment %d msg_seq = %v, want 1（同一流式消息同序号）", i, fragment["msg_seq"])
		}
	}
}

func TestStreamSessionMarkdownAndGroupFallback(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["markdown_support"] = "true"
	provider.rememberInboundMsgID("user:user-openid-1", "evt-1")

	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "**加粗**"); err != nil {
		t.Fatal(err)
	}
	fragments := fake.streamFragmentPayloads()
	if len(fragments) != 1 || fragments[0]["content_type"] != "markdown" {
		t.Fatalf("fragments = %+v", fragments)
	}

	// 群聊：委托会话（不发 stream_messages，Final 走普通发送）。
	// 用无 markdown 的配置，断言走普通文本发送路径。
	plainCfg := testConfig("qq-1", "app-1", "secret-1")
	groupSession, err := provider.StartStreaming(context.Background(), plainCfg, "group:group-openid-1")
	if err != nil {
		t.Fatalf("group start streaming: %v", err)
	}
	if err := groupSession.Update(context.Background(), "群消息流式"); err != nil {
		t.Fatal(err)
	}
	if err := groupSession.Final(context.Background(), []connectors.OutboundMessage{{Text: "群终态"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.streamFragmentPayloads()); got != 1 {
		t.Fatalf("group must not stream, fragments = %d", got)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].path != "/v2/groups/group-openid-1/messages" ||
		sent[0].payload["content"] != "群终态" {
		t.Fatalf("group final send = %+v", sent)
	}
}

func TestStreamSessionNoPassiveContextDelegates(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	// 无最近入站 msg_id：退化委托会话，不发流式。
	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "文本"); err != nil {
		t.Fatal(err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{Text: "终态"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.streamFragmentPayloads()); got != 0 {
		t.Fatalf("fragments = %d, want 0（无被动上下文不发流式）", got)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].payload["content"] != "终态" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestStreamSessionFailKeepsPrefix(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:user-openid-1", "evt-1")

	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Update(context.Background(), "已完成一半"); err != nil {
		t.Fatal(err)
	}
	if err := session.Fail(context.Background(), "（生成中断）"); err != nil {
		t.Fatal(err)
	}
	fragments := fake.streamFragmentPayloads()
	if len(fragments) != 2 {
		t.Fatalf("fragments = %+v", fragments)
	}
	last := fragments[1]
	if last["input_state"] != float64(10) || last["content_raw"] != "已完成一半\n\n（生成中断）" {
		t.Fatalf("fail fragment = %+v", last)
	}
}

// streamFragmentPayloads 返回流式分片请求载荷快照（拷贝）。
func (f *fakeQQ) streamFragmentPayloads() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, 0, len(f.streamFragments))
	for _, payload := range f.streamFragments {
		cloned := make(map[string]any, len(payload))
		for key, value := range payload {
			cloned[key] = value
		}
		out = append(out, cloned)
	}
	return out
}
