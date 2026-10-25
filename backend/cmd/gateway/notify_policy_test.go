package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

func policyKinds(kinds ...string) *[]string {
	out := append([]string(nil), kinds...)
	return &out
}

// TestBuildNotifyPolicyStoreKindOverride 锁定字段级覆盖：session 白名单
// 覆盖 global；未覆盖的会话仍受 global 限制；completion/completed 别名归一。
func TestBuildNotifyPolicyStoreKindOverride(t *testing.T) {
	file := notifyPolicyFile{
		Version: 1,
		Global:  notifyPolicyScope{AllowKinds: policyKinds("approval")},
		Sessions: map[string]notifyPolicyScope{
			"s-both": {AllowKinds: policyKinds("approval", "completed")},
		},
		Channels: map[string]notifyPolicyScope{
			"ntfy": {Muted: boolPtr(true)},
		},
	}
	store, quiet, quietSet, err := buildNotifyPolicyStore(file)
	if err != nil {
		t.Fatal(err)
	}
	if quietSet || len(quiet) != 0 {
		t.Fatalf("未设置 quiet_hours 不应产生窗口：%v/%v", quiet, quietSet)
	}
	if !store.Resolve("s-both", "webpush").AllowsKind("completed") {
		t.Fatal("session 白名单应覆盖 global")
	}
	if store.Resolve("s-other", "webpush").AllowsKind("completed") {
		t.Fatal("global 白名单应拦截其它会话的 completed")
	}
	if !store.Resolve("s-other", "webpush").AllowsKind("approval") {
		t.Fatal("global 白名单应放行 approval")
	}
	if !store.Resolve("s-both", "webpush").AllowsKind("completion") {
		t.Fatal("completion/completed 别名应归一")
	}
	if eff := store.Resolve("", "ntfy"); !eff.Muted {
		t.Fatal("channel:ntfy 应静音")
	}
}

// TestBuildNotifyPolicyStoreValidation 锁定非法配置被拒绝（400 语义）。
func TestBuildNotifyPolicyStoreValidation(t *testing.T) {
	cases := []notifyPolicyFile{
		{Global: notifyPolicyScope{MinPriority: "urgent"}},
		{Sessions: map[string]notifyPolicyScope{"s1": {QuietHours: policyKinds("22:00-08:00")}}},
		{Global: notifyPolicyScope{QuietHours: policyKinds("25:00-08:00")}},
		{Sessions: map[string]notifyPolicyScope{"": {Muted: boolPtr(true)}}},
	}
	for i, file := range cases {
		if _, _, _, err := buildNotifyPolicyStore(file); err == nil {
			t.Fatalf("case %d: 非法策略应报错", i)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

// TestNotifyPolicySourceLegacyFallback：notify-policy.json 缺失时只读回退
// 旧 notify-prefs.json，并在 GET 视图中转成可编辑的策略。
func TestNotifyPolicySourceLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "notify-prefs.json")
	content := `{"scopes":[
	  {"scope":"global","min_priority":"normal"},
	  {"scope":"channel:ntfy","muted":true},
	  {"scope":"session:s-1","min_priority":"high"}
	]}`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newNotifyPolicySource(filepath.Join(dir, "notify-policy.json"), legacy)
	if err := src.Load(); err != nil {
		t.Fatal(err)
	}
	if src.SourceLabel() != "legacy" {
		t.Fatalf("source = %s, want legacy", src.SourceLabel())
	}
	view, err := src.CurrentView()
	if err != nil {
		t.Fatal(err)
	}
	if view.Global.MinPriority != "normal" {
		t.Fatalf("legacy global 未转换: %+v", view.Global)
	}
	if item, ok := view.Channels["ntfy"]; !ok || item.Muted == nil || !*item.Muted {
		t.Fatalf("legacy channel 未转换: %+v", view.Channels)
	}
	prefs, _, _, err := src.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if eff := prefs.Resolve("s-1", "webpush"); eff.MinPriority != notify.PriorityHigh {
		t.Fatalf("legacy session 门槛 = %+v", eff)
	}
	if eff := prefs.Resolve("s-1", "ntfy"); !eff.Muted {
		t.Fatalf("legacy ntfy 静音 = %+v", eff)
	}
}

// TestNotifyPolicySourceHotReload：文件变更后 MaybeReload 生效；非法文件
// 保留最后一次成功策略（不把线上策略清空）。
func TestNotifyPolicySourceHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify-policy.json")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// 确保 mtime/size 变化能被 stamp 观察到。
		now := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":1,"global":{"allow_kinds":["approval"]}}`)
	src := newNotifyPolicySource(path, filepath.Join(dir, "notify-prefs.json"))
	if err := src.Load(); err != nil {
		t.Fatal(err)
	}
	prefs, _, _, err := src.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if prefs.Resolve("", "webpush").AllowsKind("completed") {
		t.Fatal("初始策略应拦截 completed")
	}

	write(`{"version":1,"global":{"allow_kinds":["completed"]}}`)
	if err := src.MaybeReload(); err != nil {
		t.Fatal(err)
	}
	prefs, _, _, err = src.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if !prefs.Resolve("", "webpush").AllowsKind("completed") || prefs.Resolve("", "webpush").AllowsKind("approval") {
		t.Fatal("热加载后的白名单未生效")
	}

	write(`{"version":1,"global":{"min_priority":"bogus"}}`)
	if err := src.MaybeReload(); err == nil {
		t.Fatal("非法文件应返回错误")
	}
	prefs, _, _, _ = src.Effective()
	if !prefs.Resolve("", "webpush").AllowsKind("completed") {
		t.Fatal("非法文件不应清空最后一次成功策略")
	}
}

// TestNotifyPolicyAPI 锁定 GET/PUT 契约：默认视图、保存后立即可读、
// 非法内容 400 且不落盘。
func TestNotifyPolicyAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify-policy.json")
	src := newNotifyPolicySource(path, filepath.Join(dir, "notify-prefs.json"))
	if err := src.Load(); err != nil {
		t.Fatal(err)
	}
	saved := false
	handler := handleNotifyPolicy(src, func() notifyPolicyMeta {
		return notifyPolicyMeta{Channels: []notifyChannelMeta{{Name: "webpush", Label: "Web Push", Available: true}}}
	}, func() { saved = true })

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/notify/policy", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", get.Code, get.Body.String())
	}
	var got struct {
		Policy notifyPolicyFile `json:"policy"`
		Meta   notifyPolicyMeta `json:"meta"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Meta.Source != "default" || len(got.Meta.Kinds) != 5 {
		t.Fatalf("默认元数据 = %+v", got.Meta)
	}

	body := `{"version":1,"global":{"allow_kinds":["approval"],"quiet_hours":["22:00-08:00"]},"sessions":{"s1":{"muted":true}}}`
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/notify/policy", bytes.NewBufferString(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", put.Code, put.Body.String())
	}
	if !saved {
		t.Fatal("PUT 成功应触发 onSaved")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("策略文件未落盘: %v", err)
	}
	prefs, quiet, quietSet, err := src.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if !quietSet || len(quiet) != 1 {
		t.Fatalf("quiet_hours 未生效: %v/%v", quiet, quietSet)
	}
	if prefs.Resolve("s1", "webpush").AllowsKind("completed") {
		t.Fatal("s1 未应用 allow_kinds")
	}
	if prefs.Resolve("", "webpush").AllowsKind("completed") {
		t.Fatal("global 未应用 allow_kinds")
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodPut, "/api/notify/policy",
		bytes.NewBufferString(`{"version":1,"global":{"min_priority":"urgent"}}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法 PUT = %d %s", bad.Code, bad.Body.String())
	}
}

// TestPushNotifierPolicyKindFilter：策略 allow_kinds 在宿主 observe 阶段
// 就拦截 kind，不进入合并/投递；被拦截事件写入 seen，不会反复重试。
func TestPushNotifierPolicyKindFilter(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "notify-policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"global":{"allow_kinds":["approval"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newNotifyPolicySource(path, filepath.Join(dir, "notify-prefs.json"))
	if err := src.Load(); err != nil {
		t.Fatal(err)
	}

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = 0
	n.policy = src
	n.applyPolicy()

	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "completed"}},
	}})
	if waitHit(hits, 300*time.Millisecond) {
		t.Fatal("completed 被 allow_kinds 拦截，不应投递")
	}
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s2", Title: "B", Attention: "approval"}},
	}})
	if !waitHit(hits, 2*time.Second) {
		t.Fatal("approval 应正常投递")
	}
}
