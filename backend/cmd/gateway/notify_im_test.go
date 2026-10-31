package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// readIMFile 读盘并解析 gateway-im.json（验证"真的落盘"而非只改内存）。
func readIMFile(t *testing.T, path string) notifyIMFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置文件应已落盘：%v", err)
	}
	var view notifyIMFile
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("落盘内容应为合法 JSON：%v", err)
	}
	return view
}

// TestNotifyIMGetOnMissingFile：文件不存在时 GET 仍 200，返回空目标列表，
// 且 meta 暴露 target_limit（控制台据此渲染，不硬编码）。
func TestNotifyIMGetOnMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	h := handleNotifyIM(src, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/notify/im", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应为 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Config notifyIMFile `json:"config"`
		Meta   notifyIMMeta `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Config.Targets) != 0 {
		t.Fatalf("未配置时不应有目标：%+v", resp.Config.Targets)
	}
	if resp.Meta.TargetLimit != notifyIMTargetLimit {
		t.Fatalf("meta.target_limit 应为 %d，实际 %d", notifyIMTargetLimit, resp.Meta.TargetLimit)
	}
}

// TestNotifyIMPutPersistsAndReloads：PUT 合法配置 → 200、真落盘、新实例能读回
// （证明原子写与热加载的数据源一致）。
func TestNotifyIMPutPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	var savedWith bool
	h := handleNotifyIM(src, nil, func(wasEnabled bool) { savedWith = wasEnabled })

	body := `{"version":1,"targets":[{"id":"ops","connector_id":"qq-ops","chat":"123","label":"运维群","scope":"all"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 应为 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if savedWith {
		t.Fatal("保存前没有启用目标，回调应收到 wasEnabled=false")
	}

	disk := readIMFile(t, path)
	if len(disk.Targets) != 1 || disk.Targets[0].ConnectorID != "qq-ops" {
		t.Fatalf("落盘内容不符：%+v", disk.Targets)
	}
	if disk.Version < 1 {
		t.Fatalf("落盘应带版本号：%d", disk.Version)
	}

	// 新实例（模拟进程重启）必须读回同一份配置。
	reloaded := newNotifyIMSource(path)
	got, warn := reloaded.Current()
	if warn != "" {
		t.Fatalf("重新装载不应有告警：%s", warn)
	}
	if len(got.Targets) != 1 || got.Targets[0].Chat != "123" {
		t.Fatalf("重启后配置不一致：%+v", got.Targets)
	}
	if reloaded.TargetCount() != 1 {
		t.Fatalf("启用目标数应为 1，实际 %d", reloaded.TargetCount())
	}
}

// TestNotifyIMPutRejectsInvalidWithoutSideEffect：校验失败返回 400，且**不落盘、
// 不改变生效配置**（配置规则的关键保证）。
func TestNotifyIMPutRejectsInvalidWithoutSideEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	h := handleNotifyIM(src, nil, nil)

	// 先写入一份合法配置。
	good := `{"version":1,"targets":[{"id":"ops","connector_id":"qq-ops","chat":"123"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewBufferString(good)))
	if rec.Code != http.StatusOK {
		t.Fatalf("前置合法写入失败：%d", rec.Code)
	}
	before := readIMFile(t, path)

	// 缺 connector_id。
	bad := `{"version":1,"targets":[{"id":"x","chat":"999"}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewBufferString(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法配置应 400，实际 %d %s", rec.Code, rec.Body.String())
	}

	after := readIMFile(t, path)
	if len(after.Targets) != len(before.Targets) || after.Targets[0].ID != before.Targets[0].ID {
		t.Fatalf("校验失败不得落盘：before=%+v after=%+v", before.Targets, after.Targets)
	}
	if cur, _ := src.Current(); len(cur.Targets) != 1 {
		t.Fatalf("校验失败不得改变生效配置：%+v", cur.Targets)
	}
}

// TestNotifyIMPutRejectsUnknownScope：未知 scope 在写入前被拒（避免配置漂移
// 导致投递被静默丢弃）。
func TestNotifyIMPutRejectsUnknownScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	h := handleNotifyIM(src, nil, nil)

	body := `{"version":1,"targets":[{"id":"x","connector_id":"c","chat":"1","scope":"bogus"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 scope 应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("被拒绝的配置不应产生文件")
	}
}

// TestNotifyIMPTPutRejectsTooManyTargets：超过上限 32 直接拒绝（并发预算的
// 前提条件，见 docs/gateway/18 §5.6/§6.3）。
func TestNotifyIMPutRejectsTooManyTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)

	view := notifyIMFile{Version: 1}
	for i := 0; i <= notifyIMTargetLimit; i++ {
		view.Targets = append(view.Targets, notifyIMTarget{
			ID:          "t" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			ConnectorID: "c",
			Chat:        "chat",
		})
	}
	raw, _ := json.Marshal(view)
	h := handleNotifyIM(src, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewReader(raw)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超限应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestNotifyIMTestEndpoint：测试接口的三种状态——未接线 501、目标不存在 404、
// 正常 200 且真的调用发送器。
func TestNotifyIMTestEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	h := handleNotifyIM(src, nil, nil)
	body := `{"version":1,"targets":[{"id":"ops","connector_id":"qq-ops","chat":"123"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("前置写入失败：%d", rec.Code)
	}

	th := handleNotifyIMTest(src)

	// 1) 发送器未接线（connectors 未装配）→ 501，不伪装成功。
	rec = httptest.NewRecorder()
	th.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test",
		bytes.NewBufferString(`{"target_id":"ops"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 2) 接线后正常发送。
	var gotConnector, gotChat, gotText string
	src.SetSendTest(func(_ context.Context, connectorID, chat, text string) error {
		gotConnector, gotChat, gotText = connectorID, chat, text
		return nil
	})
	rec = httptest.NewRecorder()
	th.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test",
		bytes.NewBufferString(`{"target_id":"ops"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if gotConnector != "qq-ops" || gotChat != "123" || gotText == "" {
		t.Fatalf("应把配置里的连接器/会话传给发送器：%q %q %q", gotConnector, gotChat, gotText)
	}

	// 3) 目标不存在 → 404。
	rec = httptest.NewRecorder()
	th.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test",
		bytes.NewBufferString(`{"target_id":"nope"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知目标应 404，实际 %d", rec.Code)
	}
}

// TestNotifyIMDisabledTargetNotCompiled：enabled=false 的目标不参与投递，
// 且不影响 TargetCount（唤醒门依据它判断"是否有 bot 收件人"）。
func TestNotifyIMDisabledTargetNotCompiled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	no := false
	view := notifyIMFile{Version: 1, Targets: []notifyIMTarget{
		{ID: "on", ConnectorID: "c1", Chat: "1"},
		{ID: "off", ConnectorID: "c1", Chat: "2", Enabled: &no},
	}}
	if err := writeNotifyIMFile(path, view); err != nil {
		t.Fatal(err)
	}
	reloaded := newNotifyIMSource(path)
	if got := reloaded.TargetCount(); got != 1 {
		t.Fatalf("停用目标不应计入启用数：%d", got)
	}
	if targets := reloaded.Targets(); len(targets) != 1 || targets[0].ID != "on" {
		t.Fatalf("编译结果应只含启用目标：%+v", targets)
	}
}

// TestNotifyIMScopeDefaultsPresence：全局 target 的 ignore_presence 缺省为 true
// （docs/gateway/18 §5.5：运维告警不应被"有人在看控制台"抑制）。
func TestNotifyIMScopeDefaultsPresence(t *testing.T) {
	view := notifyIMFile{Version: 1, Targets: []notifyIMTarget{
		{ID: "global", ConnectorID: "c", Chat: "1", Scope: "all"},
	}}
	targets := view.compiledTargets()
	if len(targets) != 1 || !targets[0].IgnorePresence {
		t.Fatalf("全局 target 应默认无视在场门：%+v", targets)
	}

	no := false
	view.Targets[0].IgnorePresence = &no
	if view.compiledTargets()[0].IgnorePresence {
		t.Fatal("显式 false 应被尊重")
	}
}

// TestNotifyIMNodeTargetValidationAndDefaults 锁定 source=node：
// node_id 必填、kinds 缺省 error/waiting、编译结果带 Source/NodeID。
func TestNotifyIMNodeTargetValidationAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	h := handleNotifyIM(src, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im",
		bytes.NewBufferString(`{"version":1,"targets":[{"id":"n1","source":"node"}]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 node_id 应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应落盘：%v", err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im",
		bytes.NewBufferString(`{"version":1,"targets":[{"id":"n1","source":"node","node_id":"node-a","label":"节点告警","scope":"sessions","sessions":["s1"]}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法节点目标应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	targets := src.Targets()
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	got := targets[0]
	if got.Source != "node" || got.NodeID != "node-a" {
		t.Fatalf("compiled source/node = %+v", got)
	}
	if len(got.Kinds) != 2 || got.Kinds[0] != "error" || got.Kinds[1] != "waiting" {
		t.Fatalf("节点目标缺省 kinds 应为 error/waiting：%+v", got.Kinds)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/notify/im",
		bytes.NewBufferString(`{"version":1,"targets":[{"id":"n1","source":"node","node_id":"node-a","kinds":["nope"]}]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 kind 应 400，实际 %d", rec.Code)
	}
}

// TestNotifyIMNodeTestRequiresSession：节点目标测试需要 scope=sessions 且至少
// 一个会话（节点侧按会话反查绑定），并走注入的节点委托发送器。
func TestNotifyIMNodeTestRequiresSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	if err := src.Apply(notifyIMFile{Version: 1, Targets: []notifyIMTarget{
		{ID: "n1", Source: "node", NodeID: "node-a"},
	}}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	h := handleNotifyIMTest(src)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test", bytes.NewBufferString(`{"target_id":"n1"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无会话应 400，实际 %d %s", rec.Code, rec.Body.String())
	}

	called := 0
	src.SetSendNodeAttention(func(_ context.Context, nodeID string, notice connectors.AttentionNotice) error {
		called++
		if nodeID != "node-a" || notice.Session.SessionID != "s1" {
			t.Errorf("delegate got node=%q session=%q", nodeID, notice.Session.SessionID)
		}
		return nil
	})
	if err := src.Apply(notifyIMFile{Version: 1, Targets: []notifyIMTarget{
		{ID: "n1", Source: "node", NodeID: "node-a", Scope: "sessions", Sessions: []string{"s1"}},
	}}, nil); err != nil {
		t.Fatalf("apply sessions: %v", err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test", bytes.NewBufferString(`{"target_id":"n1"}`)))
	if rec.Code != http.StatusOK || called != 1 {
		t.Fatalf("节点测试应成功一次：status=%d called=%d body=%s", rec.Code, called, rec.Body.String())
	}
}

// TestNotifyIMTestAcceptsDraftTarget：UI 在未保存的 target 上点“测试”时会带上
// 完整 target；服务端不得再返回 target not found，也不得顺手落盘。
func TestNotifyIMTestAcceptsDraftTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-im.json")
	src := newNotifyIMSource(path)
	gwCalls := 0
	src.SetSendTest(func(_ context.Context, connectorID, chat, text string) error {
		gwCalls++
		if connectorID != "gw-1" || chat != "chat-1" {
			t.Errorf("gateway send got %q %q", connectorID, chat)
		}
		return nil
	})
	nodeCalls := 0
	src.SetSendNodeAttention(func(_ context.Context, nodeID string, notice connectors.AttentionNotice) error {
		nodeCalls++
		if nodeID != "node-a" || notice.Session.SessionID != "s1" {
			t.Errorf("node send got %q %q", nodeID, notice.Session.SessionID)
		}
		return nil
	})
	h := handleNotifyIMTest(src)

	draftGateway := `{"target_id":"t-draft","target":{"id":"t-draft","connector_id":"gw-1","chat":"chat-1","scope":"all"}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test", bytes.NewBufferString(draftGateway)))
	if rec.Code != http.StatusOK || gwCalls != 1 {
		t.Fatalf("gateway draft test: status=%d calls=%d body=%s", rec.Code, gwCalls, rec.Body.String())
	}

	draftNode := `{"target_id":"t-node","target":{"id":"t-node","source":"node","node_id":"node-a","scope":"sessions","sessions":["s1"]}}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/notify/im/test", bytes.NewBufferString(draftNode)))
	if rec.Code != http.StatusOK || nodeCalls != 1 {
		t.Fatalf("node draft test: status=%d calls=%d body=%s", rec.Code, nodeCalls, rec.Body.String())
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("测试不应落盘草稿：%v", err)
	}
}
