package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// /web/api/screen 结构化消息过滤（roles / q）
//
// 背景：对话页签需要「只看工具消息 / 只看用户消息 / 按正文搜索」这类过滤。
// 过滤在服务端执行 —— 前端只持有最新一页窗口（msg_limit），客户端过滤会漏掉
// 尚未加载的更早消息，也拿不到准确的「匹配 N / 共 M 条」计数。服务端先过滤、
// 再在过滤后的序列上做 msg_limit/msg_before 分页（= 搜索结果分页语义），
// message_window.total 是匹配条数、unfiltered_total 是过滤前总数。
// ---------------------------------------------------------------------------

// filterTestSession 构造 6 条消息（角色与正文都可辨识）：
//   - roles=user → q1/q2
//   - roles=user,assistant → q1/Answer one/q2/Answer two
//   - q=answer → Answer one/Answer two（大小写不敏感）
func filterTestSession() *ChatSession {
	return &ChatSession{Messages: []types.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "Answer one"},
		{Role: "system", Content: "sys1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "Answer two"},
		{Role: "system", Content: "sys2"},
	}}
}

func filterContents(msgs []chatWebScreenMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func filterRolesOf(msgs []chatWebScreenMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

// filterCaseLabel 生成等价性断言的用例标签（map 遍历顺序不稳定，显式排序）。
func filterCaseLabel(window chatWebMessageWindow, filter chatWebMessageFilter) string {
	roles := make([]string, 0, len(filter.Roles))
	for role := range filter.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return fmt.Sprintf("window={before:%d limit:%d} roles=%v q=%q", window.Before, window.Limit, roles, filter.Query)
}

func TestChatWebMessageFilterRoleKnown(t *testing.T) {
	// 与 chat.js 的 MSG_LABELS / msg-filter.js 的 FILTER_ROLES 同集合。
	for _, role := range []string{"user", "assistant", "reasoning", "tool", "system", "command", "diagnostic", "runtime"} {
		if !chatWebMessageFilterRoleKnown(role) {
			t.Fatalf("已知角色 %q 应被接受", role)
		}
	}
	for _, role := range []string{"", "User", "bogus", "error", "tool_result"} {
		if chatWebMessageFilterRoleKnown(role) {
			t.Fatalf("未知角色 %q 不应被接受（避免拼写错误把结果集变成空）", role)
		}
	}
}

func TestChatWebMessageFilterMatchAndApply(t *testing.T) {
	msgs := []chatWebScreenMessage{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "Answer one"},
		{Role: "tool", Content: "shell output"},
		{Role: "system", Content: "sys1"},
	}

	// 未激活：active=false，apply 不改动。
	var inactive chatWebMessageFilter
	if inactive.active() {
		t.Fatal("零值过滤器不应处于激活态")
	}
	if got := inactive.apply(msgs); len(got) != len(msgs) {
		t.Fatalf("未激活时 apply 不应过滤，实际 %d 条", len(got))
	}

	// 角色多选：只保留选中角色。
	roles := chatWebMessageFilter{Roles: map[string]bool{"user": true, "tool": true}}
	if !roles.active() {
		t.Fatal("选中角色后应处于激活态")
	}
	if got := filterRolesOf(roles.apply(append([]chatWebScreenMessage(nil), msgs...))); strings.Join(got, ",") != "user,tool" {
		t.Fatalf("角色过滤结果 = %v, want [user tool]", got)
	}

	// 正文搜索：小写子串匹配（调用方已把查询小写化）。
	query := chatWebMessageFilter{Query: "answer"}
	if got := filterContents(query.apply(append([]chatWebScreenMessage(nil), msgs...))); len(got) != 1 || got[0] != "Answer one" {
		t.Fatalf("正文搜索结果 = %v, want [Answer one]", got)
	}

	// 角色 + 正文：同时满足才保留。
	both := chatWebMessageFilter{Roles: map[string]bool{"system": true}, Query: "answer"}
	if got := both.apply(append([]chatWebScreenMessage(nil), msgs...)); len(got) != 0 {
		t.Fatalf("角色与正文是 AND 关系，实际 %v", filterContents(got))
	}

	// 空输入：nil / 空切片都不 panic。
	if got := both.apply(nil); len(got) != 0 {
		t.Fatalf("nil 输入应返回空，实际 %d 条", len(got))
	}
}

func TestChatWebMessageFilterParam(t *testing.T) {
	long := strings.Repeat("a", chatWebMessageFilterMaxQueryRunes+50)
	cases := []struct {
		label     string
		url       string
		wantRoles []string
		wantQuery string
	}{
		{"无参数", ChatWebAPIScreenPath, nil, ""},
		{"单角色", ChatWebAPIScreenPath + "?roles=tool", []string{"tool"}, ""},
		{"多角色去空白并小写", ChatWebAPIScreenPath + "?roles=%20Tool%20,%20user", []string{"tool", "user"}, ""},
		{"未知角色被忽略", ChatWebAPIScreenPath + "?roles=bogus", nil, ""},
		{"部分未知角色只保留已知项", ChatWebAPIScreenPath + "?roles=bogus,tool", []string{"tool"}, ""},
		{"搜索词去空白并小写", ChatWebAPIScreenPath + "?q=%20Answer%20", nil, "answer"},
		{"角色 + 搜索词", ChatWebAPIScreenPath + "?roles=assistant&q=one", []string{"assistant"}, "one"},
		{"空值等价于不过滤", ChatWebAPIScreenPath + "?roles=&q=%20%20", nil, ""},
		{"超长搜索词按 rune 截断", ChatWebAPIScreenPath + "?q=" + long, nil, strings.Repeat("a", chatWebMessageFilterMaxQueryRunes)},
		{"多字节搜索词截断后仍是合法 UTF-8", ChatWebAPIScreenPath + "?q=" + strings.Repeat("中", chatWebMessageFilterMaxQueryRunes+10), nil, strings.Repeat("中", chatWebMessageFilterMaxQueryRunes)},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		got := chatWebMessageFilterParam(req)
		if tc.wantRoles == nil {
			if len(got.Roles) != 0 {
				t.Fatalf("%s: roles = %v, want 空", tc.label, got.Roles)
			}
		} else {
			if len(got.Roles) != len(tc.wantRoles) {
				t.Fatalf("%s: roles = %v, want %v", tc.label, got.Roles, tc.wantRoles)
			}
			for _, role := range tc.wantRoles {
				if !got.Roles[role] {
					t.Fatalf("%s: roles = %v, want 含 %q", tc.label, got.Roles, role)
				}
			}
		}
		if got.Query != tc.wantQuery {
			t.Fatalf("%s: query = %q, want %q", tc.label, got.Query, tc.wantQuery)
		}
		if (tc.wantRoles == nil && tc.wantQuery == "") && got.active() {
			t.Fatalf("%s: 空参数不应处于激活态", tc.label)
		}
	}
	if got := chatWebMessageFilterParam(nil); got.active() {
		t.Fatal("nil 请求应返回零值过滤器")
	}
}

func TestBuildChatWebScreenSnapshotForFilterRolesAndPaging(t *testing.T) {
	withWebTestSession(t, filterTestSession())

	// 1) 只看用户消息：不过滤时的 6 条 → 匹配 2 条。
	snap := buildChatWebScreenSnapshotForFilter(chatWebMessageWindow{}, chatWebMessageFilter{Roles: map[string]bool{"user": true}})
	if !snap.Available {
		t.Fatalf("快照应可用，实际 reason=%q", snap.Reason)
	}
	if got := filterContents(snap.Messages); strings.Join(got, ",") != "q1,q2" {
		t.Fatalf("roles=user → %v, want [q1 q2]", got)
	}
	if snap.MessageWindow == nil || snap.MessageWindow.Total != 2 || snap.MessageWindow.UnfilteredTotal != 6 {
		t.Fatalf("message_window = %+v, want {Total:2 UnfilteredTotal:6}", snap.MessageWindow)
	}
	if snap.Text != "user> q1\nuser> q2" {
		t.Fatalf("text 应按过滤后的消息重建，实际 %q", snap.Text)
	}

	// 2) 角色多选 + 正文搜索（AND）。
	snap = buildChatWebScreenSnapshotForFilter(chatWebMessageWindow{},
		chatWebMessageFilter{Roles: map[string]bool{"assistant": true}, Query: "two"})
	if got := filterContents(snap.Messages); strings.Join(got, ",") != "Answer two" {
		t.Fatalf("roles=assistant&q=two → %v, want [Answer two]", got)
	}
	if snap.MessageWindow.Total != 1 || snap.MessageWindow.UnfilteredTotal != 6 {
		t.Fatalf("message_window = %+v, want {Total:1 UnfilteredTotal:6}", snap.MessageWindow)
	}

	// 3) 分页作用在过滤后的序列上（搜索结果分页）：匹配 2 条时取最新 1 条。
	snap = buildChatWebScreenSnapshotForFilter(chatWebMessageWindow{Limit: 1},
		chatWebMessageFilter{Roles: map[string]bool{"user": true}})
	if got := filterContents(snap.Messages); strings.Join(got, ",") != "q2" {
		t.Fatalf("msg_limit=1 → %v, want [q2]", got)
	}
	w := snap.MessageWindow
	if w == nil || w.Total != 2 || w.UnfilteredTotal != 6 || w.Start != 1 || w.End != 2 || !w.HasMore {
		t.Fatalf("message_window = %+v, want {Total:2 UnfilteredTotal:6 Start:1 End:2 HasMore:true}", w)
	}
	if snap.Text != "user> q2" {
		t.Fatalf("text 应与过滤窗口同源，实际 %q", snap.Text)
	}

	// 4) 上滚一页：msg_before 游标同样在过滤后的索引空间里。
	snap = buildChatWebScreenSnapshotForFilter(chatWebMessageWindow{Before: 1, Limit: 1},
		chatWebMessageFilter{Roles: map[string]bool{"user": true}})
	if got := filterContents(snap.Messages); strings.Join(got, ",") != "q1" {
		t.Fatalf("msg_before=1 → %v, want [q1]", got)
	}
	if snap.MessageWindow.Start != 0 || snap.MessageWindow.End != 1 || snap.MessageWindow.HasMore {
		t.Fatalf("message_window = %+v, want {Start:0 End:1 HasMore:false}", snap.MessageWindow)
	}

	// 5) 0 命中：仍返回分页元信息（前端据此显示「匹配 0 / 共 6 条」）。
	snap = buildChatWebScreenSnapshotForFilter(chatWebMessageWindow{Limit: 5},
		chatWebMessageFilter{Query: "no-such-text"})
	if len(snap.Messages) != 0 {
		t.Fatalf("0 命中时 messages 应为空，实际 %v", filterContents(snap.Messages))
	}
	if snap.MessageWindow == nil || snap.MessageWindow.Total != 0 || snap.MessageWindow.UnfilteredTotal != 6 {
		t.Fatalf("0 命中 message_window = %+v, want {Total:0 UnfilteredTotal:6}", snap.MessageWindow)
	}
	if snap.Text != "" || len(snap.Lines) != 0 {
		t.Fatalf("0 命中时 text/lines 应为空，实际 text=%q lines=%v", snap.Text, snap.Lines)
	}
}

// TestBuildChatWebScreenSnapshotForFilterMatchesFullThenFilter 锁定过滤路径与
// 「先取全量 → 过滤 → 切片 → 重建 lines/text」的参考实现逐字段一致（含
// unfiltered_total 与未过滤时的历史行为不变）。
func TestBuildChatWebScreenSnapshotForFilterMatchesFullThenFilter(t *testing.T) {
	withWebTestSession(t, filterTestSession())

	windows := []chatWebMessageWindow{
		{},
		{Limit: 1},
		{Limit: 3},
		{Before: 2, Limit: 2},
		{Before: 999, Limit: 999},
	}
	filters := []chatWebMessageFilter{
		{}, // 未激活：必须与既有窗口化路径逐字段一致
		{Roles: map[string]bool{"user": true}},
		{Roles: map[string]bool{"assistant": true, "system": true}},
		{Query: "answer"},
		{Roles: map[string]bool{"system": true}, Query: "sys"},
		{Query: "no-such-text"},
	}
	for _, window := range windows {
		for _, filter := range filters {
			full := buildChatWebScreenSnapshotFull()
			want := *full
			want.Messages = append([]chatWebScreenMessage(nil), full.Messages...)
			unfilteredTotal := len(want.Messages)
			want.Messages = filter.apply(want.Messages)
			matchedTotal := len(want.Messages)
			if filter.active() {
				windowChatWebMessages(&want, window)
				if want.MessageWindow == nil {
					want.MessageWindow = &chatWebMessageWindowInfo{}
				}
				want.MessageWindow.Total = matchedTotal
				want.MessageWindow.UnfilteredTotal = unfilteredTotal
				lines := chatWebLinesForMessages(want.Messages)
				want.Lines = lines
				want.Text = strings.Join(lines, "\n")
			} else if window.active() {
				// 过滤未激活：窗口化提取路径（O(窗口)），元信息由窗口裁剪写入。
				windowChatWebMessages(&want, window)
			}
			// 过滤未激活时保持既有路径（窗口激活 → 窗口化提取；否则全量），
			// 不做任何后处理：未分页的 message_window 元信息由 JSON 序列化层
			// （marshalChatWebScreenJSONWindowFiltered）补写，不属于快照构建语义。

			got := buildChatWebScreenSnapshotForFilter(window, filter)
			compareChatWebSnapshots(t, filterCaseLabel(window, filter), got, &want)
		}
	}
}

// TestHandleChatWebAPIScreenJSONMessageFilter 覆盖 HTTP 层的参数解析、过滤 +
// 分页组合、JSON 字段（total / unfiltered_total）与文本视图。
func TestHandleChatWebAPIScreenJSONMessageFilter(t *testing.T) {
	withWebTestSession(t, filterTestSession())

	type screenFilterResponse struct {
		Messages []chatWebScreenMessage `json:"messages"`
		Window   struct {
			Total           int  `json:"total"`
			Start           int  `json:"start"`
			End             int  `json:"end"`
			HasMore         bool `json:"has_more"`
			UnfilteredTotal *int `json:"unfiltered_total"`
		} `json:"message_window"`
		Text string `json:"text"`
	}
	fetch := func(t *testing.T, url string) screenFilterResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		HandleChatWebAPIScreen(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", url, rec.Code)
		}
		var parsed screenFilterResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("%s: screen JSON invalid: %v", url, err)
		}
		return parsed
	}

	// 1) 角色过滤 + 分页：匹配 2 条（q1/q2），取最新 1 条。
	parsed := fetch(t, ChatWebAPIScreenPath+"?format=json&roles=user&msg_limit=1")
	if got := filterContents(parsed.Messages); strings.Join(got, ",") != "q2" {
		t.Fatalf("roles=user&msg_limit=1 → %v, want [q2]", got)
	}
	if parsed.Window.Total != 2 || parsed.Window.Start != 1 || parsed.Window.End != 2 || !parsed.Window.HasMore {
		t.Fatalf("message_window = %+v, want {Total:2 Start:1 End:2 HasMore:true}", parsed.Window)
	}
	if parsed.Window.UnfilteredTotal == nil || *parsed.Window.UnfilteredTotal != 6 {
		t.Fatalf("unfiltered_total = %v, want 6", parsed.Window.UnfilteredTotal)
	}
	if !strings.Contains(parsed.Text, "q2") || strings.Contains(parsed.Text, "q1") {
		t.Fatalf("text 应与过滤窗口同源，实际 %q", parsed.Text)
	}

	// 2) 正文搜索大小写不敏感（前端提交小写，这里验证大写输入同样命中）。
	parsed = fetch(t, ChatWebAPIScreenPath+"?format=json&q=ANSWER")
	if got := filterContents(parsed.Messages); strings.Join(got, ",") != "Answer one,Answer two" {
		t.Fatalf("q=ANSWER → %v, want [Answer one Answer two]", got)
	}
	if parsed.Window.Total != 2 || parsed.Window.UnfilteredTotal == nil || *parsed.Window.UnfilteredTotal != 6 {
		t.Fatalf("message_window = %+v, want {Total:2 UnfilteredTotal:6}", parsed.Window)
	}

	// 3) 0 命中：messages 为空但计数可读。
	parsed = fetch(t, ChatWebAPIScreenPath+"?format=json&q=no-such-text")
	if len(parsed.Messages) != 0 || parsed.Window.Total != 0 {
		t.Fatalf("0 命中 → messages=%v window=%+v", filterContents(parsed.Messages), parsed.Window)
	}
	if parsed.Window.UnfilteredTotal == nil || *parsed.Window.UnfilteredTotal != 6 {
		t.Fatalf("0 命中时 unfiltered_total = %v, want 6", parsed.Window.UnfilteredTotal)
	}

	// 4) 未知角色被忽略 = 不过滤（保持完整 transcript 的历史行为）。
	parsed = fetch(t, ChatWebAPIScreenPath+"?format=json&roles=bogus")
	if len(parsed.Messages) != 6 {
		t.Fatalf("未知角色不应过滤，实际 %d 条", len(parsed.Messages))
	}
	if parsed.Window.UnfilteredTotal != nil {
		t.Fatalf("未过滤时不应写 unfiltered_total，实际 %d", *parsed.Window.UnfilteredTotal)
	}

	// 5) 不传过滤参数：与历史行为一致（6 条、无 unfiltered_total）。
	parsed = fetch(t, ChatWebAPIScreenPath+"?format=json")
	if len(parsed.Messages) != 6 || parsed.Window.Total != 6 || parsed.Window.UnfilteredTotal != nil {
		t.Fatalf("未过滤响应 = messages:%d window:%+v, want 6 条且无 unfiltered_total", len(parsed.Messages), parsed.Window)
	}

	// 6) 文本视图同样支持过滤（?tail 仍作用于过滤后的文本）。
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?roles=assistant&q=two", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Answer two") || strings.Contains(body, "q1") {
		t.Fatalf("文本视图过滤应只含匹配消息，实际 %q", body)
	}
}
