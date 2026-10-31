package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotifyTargetHealthRecordAndClear 锁定目标级出站健康的语义：
// 失败记原因、成功即清除、按连接器区分、错误文本有长度上限。
func TestNotifyTargetHealthRecordAndClear(t *testing.T) {
	h := newNotifyTargetHealth()
	if got := h.Snapshot(); len(got) != 0 {
		t.Fatalf("fresh snapshot = %v, want empty", got)
	}

	h.Record("gw-a", errors.New(`wechat: chat "x" 缺少 context_token`))
	h.Record("gw-b", nil) // 无失败：不应产生条目
	snap := h.Snapshot()
	if len(snap) != 1 || !strings.Contains(snap["gw-a"], "context_token") {
		t.Fatalf("snapshot = %v", snap)
	}
	if _, ok := snap["gw-b"]; ok {
		t.Fatalf("成功不应留下条目：%v", snap)
	}

	// 下一次成功清除旧错误（避免过期错误长期挂在页面上）。
	h.Record("gw-a", nil)
	if snap := h.Snapshot(); len(snap) != 0 {
		t.Fatalf("成功后应清除：%v", snap)
	}

	// 超长错误文本按上限截断（保留前缀，补 …）。
	h.Record("gw-c", errors.New(strings.Repeat("错", notifyTargetErrorMaxRunes+50)))
	got := h.Snapshot()["gw-c"]
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长错误应截断：%q", got)
	}
	if runes := []rune(got); len(runes) != notifyTargetErrorMaxRunes+1 {
		t.Fatalf("截断长度 = %d，want %d(+1 省略号)", len(runes), notifyTargetErrorMaxRunes)
	}

	// RecordMany：整组一起记 / 一起清（会话绑定路径拿不到逐个明细）。
	h.RecordMany([]string{"gw-x", "gw-y"}, errors.New("boom"))
	snap = h.Snapshot()
	if snap["gw-x"] != "boom" || snap["gw-y"] != "boom" {
		t.Fatalf("RecordMany 失败应挂到每个连接器：%v", snap)
	}
	h.RecordMany([]string{"gw-x", "gw-y"}, nil)
	snap = h.Snapshot()
	if _, ok := snap["gw-x"]; ok {
		t.Fatalf("RecordMany 成功应清除：%v", snap)
	}
}

// TestNotifyIMHealthHandler 锁定 GET /api/notify/im/health 的契约（控制台依赖它
// 在目标行显示"最近发送失败"）。
func TestNotifyIMHealthHandler(t *testing.T) {
	h := newNotifyTargetHealth()
	h.Record("gw-wechat", errors.New("wechat: 缺少 context_token"))

	rr := httptest.NewRecorder()
	handleNotifyIMHealth(h)(rr, httptest.NewRequest(http.MethodGet, "/api/notify/im/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	if !strings.Contains(body.Errors["gw-wechat"], "context_token") {
		t.Fatalf("errors = %v", body.Errors)
	}

	// 非 GET：405（与其它管理端点一致）。
	rr2 := httptest.NewRecorder()
	handleNotifyIMHealth(h)(rr2, httptest.NewRequest(http.MethodPut, "/api/notify/im/health", nil))
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", rr2.Code)
	}

	// 无失败：errors 是空对象（不是 null），控制台可无条件遍历。
	empty := httptest.NewRecorder()
	handleNotifyIMHealth(newNotifyTargetHealth())(empty, httptest.NewRequest(http.MethodGet, "/api/notify/im/health", nil))
	if !strings.Contains(empty.Body.String(), `"errors":{}`) {
		t.Fatalf("空健康响应 = %s", empty.Body.String())
	}
}

// TestPushNotifierDispatchErrorLogThrottle 锁定日志限流：同一错误文本只打一次，
// 不同错误文本各自放行（避免一个持续错误刷满 journalctl）。
func TestPushNotifierDispatchErrorLogThrottle(t *testing.T) {
	n := newPushNotifier(nil, "", nil)
	msg := `push: dispatch failed (attention=completed): wechat: 缺少 context_token`
	if !n.shouldLogDispatchError(msg) {
		t.Fatal("首次出现应打印")
	}
	if n.shouldLogDispatchError(msg) {
		t.Fatal("同一错误在窗口内不该重复打印")
	}
	if !n.shouldLogDispatchError(msg + " (other)") {
		t.Fatal("不同错误文本应各自放行")
	}
}
