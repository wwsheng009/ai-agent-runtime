package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// sessionRuntimeEventWindowPage 是窗口模式响应的断言用结构。has_more / next_before_seq
// 用指针表示，以便区分「字段缺失（旧模式响应）」与「显式 false / 0」。
type sessionRuntimeEventWindowPage struct {
	Events        []map[string]interface{} `json:"events"`
	Count         int                      `json:"count"`
	LatestSeq     int64                    `json:"latest_seq"`
	AfterSeq      int64                    `json:"after_seq"`
	FirstSeq      int64                    `json:"first_seq"`
	LastSeq       int64                    `json:"last_seq"`
	HasMore       *bool                    `json:"has_more"`
	NextBeforeSeq *int64                   `json:"next_before_seq"`
}

// newSessionRuntimeEventWindowRouter 复用既有 handler 测试的装配方式（内存 runtime store
// + RegisterRoutes），并造好 count 条 assistant_message 事件；返回路由与写入的 seq 列表。
func newSessionRuntimeEventWindowRouter(t *testing.T, sessionID string, count int) (*mux.Router, []int64) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(0)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	seqs := make([]int64, 0, count)
	for index := 1; index <= count; index++ {
		seq, err := runtimeStore.AppendEvent(context.Background(), runtimeevents.Event{
			Type:      chat.EventAssistantMessage,
			SessionID: sessionID,
			Payload:   map[string]interface{}{"content": fmt.Sprintf("event-%d", index)},
		})
		require.NoErrorf(t, err, "造第 %d 条事件失败", index)
		seqs = append(seqs, seq)
	}

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router, seqs
}

// sessionRuntimeEventWindowURL 拼出 runtime/events 请求地址。
func sessionRuntimeEventWindowURL(sessionID, query string) string {
	target := "/api/runtime/sessions/" + sessionID + "/runtime/events"
	if query != "" {
		target += "?" + query
	}
	return target
}

// getSessionRuntimeEventWindowPage 发一次 GET 并解析响应；非 200 直接失败并带上响应体。
func getSessionRuntimeEventWindowPage(t *testing.T, router *mux.Router, target string) sessionRuntimeEventWindowPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equalf(t, http.StatusOK, rec.Code, "请求 %s 期望 200，实际 %d，响应体 %s", target, rec.Code, rec.Body.String())
	var page sessionRuntimeEventWindowPage
	require.NoErrorf(t, json.Unmarshal(rec.Body.Bytes(), &page), "解析 %s 响应失败：%s", target, rec.Body.String())
	return page
}

// sessionRuntimeEventWindowSeq 取 events[].payload.seq（窗口读取由 store 注入）并转成整数。
func sessionRuntimeEventWindowSeq(t *testing.T, event map[string]interface{}, index int) int64 {
	t.Helper()
	payload, ok := event["payload"].(map[string]interface{})
	require.Truef(t, ok, "第 %d 条事件缺少 payload：%#v", index, event)
	seq, ok := payload["seq"].(float64)
	require.Truef(t, ok, "第 %d 条事件 payload.seq 期望数字，实际 %#v", index, payload["seq"])
	return int64(seq)
}

// sessionRuntimeEventWindowSeqs 按响应顺序提取 seq，用于断言升序与分页无重叠。
func sessionRuntimeEventWindowSeqs(t *testing.T, page sessionRuntimeEventWindowPage) []int64 {
	t.Helper()
	seqs := make([]int64, 0, len(page.Events))
	for index, event := range page.Events {
		seqs = append(seqs, sessionRuntimeEventWindowSeq(t, event, index))
	}
	return seqs
}

// sessionRuntimeEventWindowContents 提取 events[].payload.content，用来证明取到的确实是
// 「最近的几条」而不是「最早的几条」。
func sessionRuntimeEventWindowContents(t *testing.T, page sessionRuntimeEventWindowPage) []string {
	t.Helper()
	contents := make([]string, 0, len(page.Events))
	for index, event := range page.Events {
		payload, ok := event["payload"].(map[string]interface{})
		require.Truef(t, ok, "第 %d 条事件缺少 payload：%#v", index, event)
		content, ok := payload["content"].(string)
		require.Truef(t, ok, "第 %d 条事件 payload.content 期望字符串，实际 %#v", index, payload["content"])
		contents = append(contents, content)
	}
	return contents
}

// TestListSessionRuntimeEventsTailWindowReturnsLatestPage 覆盖 tail=1 首屏：取最新一页
// （limit 条）、升序、has_more=true，且 first_seq/last_seq/next_before_seq 自洽。
func TestListSessionRuntimeEventsTailWindowReturnsLatestPage(t *testing.T) {
	const sessionID = "session-runtime-window-tail"
	router, _ := newSessionRuntimeEventWindowRouter(t, sessionID, 7)

	page := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail=1&limit=3"))

	assert.Equal(t, 3, page.Count, "count 应等于本页条数")
	assert.Equal(t, []int64{5, 6, 7}, sessionRuntimeEventWindowSeqs(t, page),
		"tail=1&limit=3 应返回最新的 3 条并升序排列")
	assert.Equal(t, []string{"event-5", "event-6", "event-7"}, sessionRuntimeEventWindowContents(t, page),
		"取到的应是最近 3 条事件，而不是最早的 event-1..event-3")
	require.NotNil(t, page.HasMore, "窗口模式响应必须带 has_more 字段")
	assert.True(t, *page.HasMore, "还有更早事件时 has_more 应为 true")
	assert.Equal(t, int64(7), page.LatestSeq, "latest_seq 应等于会话最大 seq")
	assert.Equal(t, int64(5), page.FirstSeq, "first_seq 应等于本页最早一条的 seq")
	assert.Equal(t, int64(7), page.LastSeq, "last_seq 应等于本页最新一条的 seq")
	require.NotNil(t, page.NextBeforeSeq, "窗口模式响应必须带 next_before_seq 字段")
	assert.Equal(t, page.FirstSeq, *page.NextBeforeSeq, "next_before_seq 应等于 first_seq")
	assert.Equal(t, int64(0), page.AfterSeq, "窗口模式不带 after 语义，after_seq 保持默认 0")
}

// TestListSessionRuntimeEventsTailWindowPagesBackwardsWithoutOverlap 覆盖逐页向前：
// 用上一页的 next_before_seq 取更早一页，两页不重叠，翻到最老一页时 has_more=false。
func TestListSessionRuntimeEventsTailWindowPagesBackwardsWithoutOverlap(t *testing.T) {
	const sessionID = "session-runtime-window-pages"
	router, allSeqs := newSessionRuntimeEventWindowRouter(t, sessionID, 7)

	first := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail=1&limit=3"))
	require.NotNil(t, first.NextBeforeSeq, "第一页应给出向前翻页游标")
	assert.Equal(t, []int64{5, 6, 7}, sessionRuntimeEventWindowSeqs(t, first), "第一页应是最新的 3 条")
	firstSeqs := sessionRuntimeEventWindowSeqs(t, first)

	second := getSessionRuntimeEventWindowPage(t, router,
		sessionRuntimeEventWindowURL(sessionID, fmt.Sprintf("before_seq=%d&limit=3", *first.NextBeforeSeq)))
	secondSeqs := sessionRuntimeEventWindowSeqs(t, second)
	assert.Equal(t, []int64{2, 3, 4}, secondSeqs, "第二页应取 seq<5 的更早 3 条并升序")
	require.NotNil(t, second.HasMore, "第二页仍应带 has_more 字段")
	assert.True(t, *second.HasMore, "第二页之前还有 seq=1，has_more 应为 true")
	require.NotNil(t, second.NextBeforeSeq)
	assert.Equal(t, int64(2), *second.NextBeforeSeq, "第二页的 next_before_seq 应指向更早一页的上界")
	for _, seq := range secondSeqs {
		assert.NotContains(t, firstSeqs, seq, "第二页与第一页不应出现重叠事件")
	}

	third := getSessionRuntimeEventWindowPage(t, router,
		sessionRuntimeEventWindowURL(sessionID, fmt.Sprintf("before_seq=%d&limit=3", *second.NextBeforeSeq)))
	thirdSeqs := sessionRuntimeEventWindowSeqs(t, third)
	assert.Equal(t, []int64{1}, thirdSeqs, "最老一页只应剩 seq=1")
	require.NotNil(t, third.HasMore, "最老一页仍应带 has_more 字段")
	assert.False(t, *third.HasMore, "翻到最老一页时 has_more 应为 false")
	assert.Equal(t, int64(1), third.FirstSeq, "最老一页 first_seq 应为 1")
	assert.Equal(t, int64(1), third.LastSeq, "最老一页 last_seq 应为 1")
	assert.Equal(t, int64(7), third.LatestSeq, "翻到最老一页时 latest_seq 仍是会话最大 seq")

	visited := make([]int64, 0, len(allSeqs))
	visited = append(visited, firstSeqs...)
	visited = append(visited, secondSeqs...)
	visited = append(visited, thirdSeqs...)
	assert.ElementsMatch(t, allSeqs, visited, "三页合起来应恰好覆盖全部事件，不重不漏")

	empty := getSessionRuntimeEventWindowPage(t, router,
		sessionRuntimeEventWindowURL(sessionID, fmt.Sprintf("before_seq=%d&limit=3", *third.NextBeforeSeq)))
	assert.Equal(t, 0, empty.Count, "越过最老事件再往前取应返回空页")
	assert.Empty(t, empty.Events, "越过最老事件再往前取不应回绕出事件")
	require.NotNil(t, empty.HasMore, "空页仍应带 has_more 字段")
	assert.False(t, *empty.HasMore, "空页 has_more 应为 false")
	assert.Equal(t, int64(0), empty.FirstSeq, "空页 first_seq 应为 0")
	require.NotNil(t, empty.NextBeforeSeq, "空页仍应带 next_before_seq 字段")
	assert.Equal(t, int64(0), *empty.NextBeforeSeq, "空页 next_before_seq 应为 0")
	assert.Equal(t, int64(7), empty.LatestSeq, "空页的 latest_seq 仍是会话最大 seq")
}

// TestListSessionRuntimeEventsTailWindowRejectsInvalidParams 覆盖参数校验：before_seq
// 非法（<=0 / 非数字）与 tail 非法值都应 400，而不是静默降级成其它模式。
func TestListSessionRuntimeEventsTailWindowRejectsInvalidParams(t *testing.T) {
	const sessionID = "session-runtime-window-invalid"
	router, _ := newSessionRuntimeEventWindowRouter(t, sessionID, 3)

	invalid := []struct {
		name  string
		query string
	}{
		{name: "before_seq 为 0", query: "before_seq=0"},
		{name: "before_seq 为负数", query: "before_seq=-1"},
		{name: "before_seq 非数字", query: "before_seq=abc"},
		{name: "tail 非法值", query: "tail=maybe"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, sessionRuntimeEventWindowURL(sessionID, tc.query), nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equalf(t, http.StatusBadRequest, rec.Code,
				"参数 %q 期望 400，实际 %d，响应体 %s", tc.query, rec.Code, rec.Body.String())
		})
	}

	// tail 的合法别名不应被当作非法值。
	for _, value := range []string{"1", "true", "yes"} {
		t.Run("tail="+value, func(t *testing.T) {
			page := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail="+value))
			assert.Equal(t, 3, page.Count, "tail=%s 应按窗口模式取最近一页的全部事件", value)
			require.NotNil(t, page.HasMore, "tail=%s 应为窗口模式响应", value)
			assert.False(t, *page.HasMore, "只有 3 条事件时 has_more 应为 false")
			assert.Equal(t, int64(1), page.FirstSeq, "只有 3 条事件时 first_seq 应为 1")
		})
	}

	// tail=0 显式关闭窗口模式：响应回落到旧字段集合（没有 has_more / first_seq）。
	legacy := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail=0"))
	assert.Nil(t, legacy.HasMore, "tail=0 应保持旧模式响应，不带 has_more 字段")
	assert.Nil(t, legacy.NextBeforeSeq, "tail=0 应保持旧模式响应，不带 next_before_seq 字段")
	assert.Equal(t, 3, legacy.Count, "tail=0 时旧模式仍按 after=0 返回全部事件")
}

// TestListSessionRuntimeEventsLegacyAfterModeStaysOldestFirst 回归：不传 tail/before_seq 时
// after 升序续拉的旧语义（取最早一页）与响应字段集合都不变。
func TestListSessionRuntimeEventsLegacyAfterModeStaysOldestFirst(t *testing.T) {
	const sessionID = "session-runtime-window-legacy"
	router, _ := newSessionRuntimeEventWindowRouter(t, sessionID, 7)

	req := httptest.NewRequest(http.MethodGet, sessionRuntimeEventWindowURL(sessionID, "after=0&limit=2"), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equalf(t, http.StatusOK, rec.Code, "旧模式请求期望 200，实际 %d，响应体 %s", rec.Code, rec.Body.String())

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, float64(2), raw["count"], "旧模式 count 应为 2")
	assert.Equal(t, float64(7), raw["latest_seq"], "旧模式 latest_seq 应为会话最大 seq")
	assert.Equal(t, float64(0), raw["after_seq"], "旧模式 after_seq 应回显请求的 after")
	_, hasMoreField := raw["has_more"]
	assert.False(t, hasMoreField, "旧模式响应不应出现窗口字段 has_more")
	_, firstSeqField := raw["first_seq"]
	assert.False(t, firstSeqField, "旧模式响应不应出现窗口字段 first_seq")

	var page sessionRuntimeEventWindowPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Equal(t, []int64{1, 2}, sessionRuntimeEventWindowSeqs(t, page),
		"after=0&limit=2 仍应返回最早的 2 条（窗口模式未改变旧行为）")
	assert.Equal(t, []string{"event-1", "event-2"}, sessionRuntimeEventWindowContents(t, page),
		"旧模式返回的是最早的 event-1、event-2")

	next := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "after=2&limit=2"))
	assert.Equal(t, []int64{3, 4}, sessionRuntimeEventWindowSeqs(t, next), "after=2&limit=2 应继续按升序续拉")
	assert.Equal(t, int64(2), next.AfterSeq, "续拉响应应回显 after=2")
}

// TestListSessionRuntimeEventsTailWindowAppliesDefaultAndMaxLimit 覆盖窗口页大小：
// limit<=0 时默认 200，超过上限 1000 时截到 1000，has_more 仍表示「还有更早事件」。
func TestListSessionRuntimeEventsTailWindowAppliesDefaultAndMaxLimit(t *testing.T) {
	const sessionID = "session-runtime-window-limit"
	const total = 1200
	router, _ := newSessionRuntimeEventWindowRouter(t, sessionID, total)

	defaultPage := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail=1"))
	assert.Equal(t, 200, defaultPage.Count, "limit<=0 时窗口页大小应默认 200")
	require.NotNil(t, defaultPage.HasMore, "tail=1 应为窗口模式响应")
	assert.True(t, *defaultPage.HasMore, "1200 条事件只取 200 条，has_more 应为 true")
	assert.Equal(t, int64(1001), defaultPage.FirstSeq, "默认页应是最近 200 条，first_seq 为 1001")
	assert.Equal(t, int64(1200), defaultPage.LastSeq, "默认页 last_seq 应为 1200")
	assert.Equal(t, int64(1200), defaultPage.LatestSeq, "latest_seq 应为会话最大 seq")

	cappedPage := getSessionRuntimeEventWindowPage(t, router, sessionRuntimeEventWindowURL(sessionID, "tail=1&limit=5000"))
	assert.Equal(t, 1000, cappedPage.Count, "limit 超过上限时应截到 1000 条")
	require.NotNil(t, cappedPage.HasMore, "tail=1&limit=5000 应为窗口模式响应")
	assert.True(t, *cappedPage.HasMore, "1200 条事件只取 1000 条，has_more 应为 true")
	assert.Equal(t, int64(201), cappedPage.FirstSeq, "上限页应是最近 1000 条，first_seq 为 201")
	assert.Equal(t, int64(1200), cappedPage.LastSeq, "上限页 last_seq 应为 1200")

	oldestPage := getSessionRuntimeEventWindowPage(t, router,
		sessionRuntimeEventWindowURL(sessionID, fmt.Sprintf("before_seq=%d&limit=1000", cappedPage.FirstSeq)))
	assert.Equal(t, 200, oldestPage.Count, "continue 向前取应得到剩余 200 条")
	require.NotNil(t, oldestPage.HasMore, "向前翻页应为窗口模式响应")
	assert.False(t, *oldestPage.HasMore, "取完最老事件后 has_more 应为 false")
	assert.Equal(t, int64(1), oldestPage.FirstSeq, "最老一页 first_seq 应为 1")
	assert.Equal(t, int64(200), oldestPage.LastSeq, "最老一页 last_seq 应为 200")
}
