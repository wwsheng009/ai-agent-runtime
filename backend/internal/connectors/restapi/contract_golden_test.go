package restapi

// 三宿主同一契约矩阵（docs/bots/06-target-architecture.md §4.2 N6）：
// gateway / aicli / runtime-server 共用同一套 restapi 路由与载荷，本文件锁定
// 与宿主无关的契约部分：
//
//  1. GET /connectors/schema 的逐字节 golden 快照
//     （testdata/connectors_schema.golden.json，覆盖 text/secret/url/bool/
//     enum/number 六种字段类型 + actions + modes + 各可选属性）；
//  2. GET /connectors 列表视图的 key 集合 = 文档集合（用全字段数据覆盖
//     omitempty 字段，确保无一遗漏、无一漂移）；
//  3. 错误载荷形状 {error} 与 {error,fields:[{key,message}]}。
//
// golden 更新入口（仅在 Schema 契约有意变更时执行；变更须 review 后提交）：
//
//	go test ./internal/connectors/restapi/ -run TestConnectorsSchemaGolden -update
//
// 也可手工替换 testdata/connectors_schema.golden.json；文件内容为 canonical
// JSON（key 排序、两空格缩进、末尾单个换行），读取时归一 CRLF→LF 后逐字节比较。

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// updateGolden 重写 testdata 下的 golden 快照（见文件头注释）。
var updateGolden = flag.Bool("update", false, "rewrite testdata golden snapshots (restapi contract matrix)")

const connectorsSchemaGoldenPath = "testdata/connectors_schema.golden.json"

// contractProvider 是契约矩阵的假 Provider：覆盖全部字段类型与可选属性，
// 让 golden 快照锁定 Schema JSON 的完整形状（含 json:"-" 内部字段不外泄）。
type contractProvider struct{}

func (p *contractProvider) Name() string { return "goldenweb" }

func (p *contractProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "goldenweb",
		Label:    "Golden Webhook",
		Modes:    []string{"polling", "webhook", "gateway"},
		Fields: []api.FieldSchema{
			{Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret, Required: true,
				Placeholder: "123456:ABC-DEF", Help: "从平台开发者后台获取"},
			{Key: "mode", Label: "接入模式", Type: api.FieldEnum, Required: true, Default: "polling",
				Options: []api.Option{
					{Value: "polling", Label: "长轮询"},
					{Value: "webhook", Label: "Webhook"},
					{Value: "gateway", Label: "长连接"},
				}},
			{Key: "display_name", Label: "显示名", Type: api.FieldText, Placeholder: "工作群"},
			{Key: "callback_url", Label: "回调地址", Type: api.FieldURL, Help: "公网可达的 HTTPS 地址"},
			{Key: "allow_media", Label: "允许媒体", Type: api.FieldBool, Default: "false"},
			{Key: "timeout_seconds", Label: "超时（秒）", Type: api.FieldNumber, Default: "30"},
		},
		Actions: []api.ActionSpec{
			{Key: "start_login", Label: "扫码登录", Kind: "qr"},
			{Key: "refresh", Label: "刷新状态", Kind: "button"},
		},
	}
}

func (p *contractProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *contractProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *contractProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

// newContractFixture 只注册 contractProvider：单 Provider 保证 schema providers
// 数组顺序确定（Service.Providers 的 map 迭代顺序不确定，多 Provider 无法逐字节比对）。
func newContractFixture(t *testing.T, configs ...connectors.Config) *fixture {
	t.Helper()
	store := connectors.NewMemoryStore(configs...)
	backend := &fakeBackend{}
	service := connectors.New(connectors.Deps{Store: store, Backend: backend, Logf: func(string, ...any) {}})
	if err := service.Register(&contractProvider{}); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Service:   service,
		Store:     store,
		Authorize: allowAll,
		Logf:      func(string, ...any) {},
		HostID:    "runtime-server",
	}
	return &fixture{
		store:   store,
		backend: backend,
		service: service,
		router:  NewRouter(deps),
		hooks:   NewHooksRouter(deps),
	}
}

// TestConnectorsSchemaGolden 断言 GET /connectors/schema 与 golden 快照逐字节相等。
func TestConnectorsSchemaGolden(t *testing.T) {
	fx := newContractFixture(t)
	recorder := do(t, fx.router, http.MethodGet, "/connectors/schema", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("schema 应 200，got %d %s", recorder.Code, recorder.Body.String())
	}
	got := canonicalJSON(t, recorder.Body.Bytes())

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(connectorsSchemaGoldenPath), 0o755); err != nil {
			t.Fatalf("创建 testdata 目录失败：%v", err)
		}
		if err := os.WriteFile(connectorsSchemaGoldenPath, got, 0o644); err != nil {
			t.Fatalf("写入 golden 失败：%v", err)
		}
	}
	want, err := os.ReadFile(connectorsSchemaGoldenPath)
	if err != nil {
		t.Fatalf("读取 golden 失败（可用 -update 生成）：%v", err)
	}
	want = []byte(strings.ReplaceAll(string(want), "\r\n", "\n"))
	if string(got) != string(want) {
		t.Fatalf("schema golden 漂移：%s\n--- want ---\n%s\n--- got ---\n%s",
			connectorsSchemaGoldenPath, want, got)
	}
}

// connectorViewKeys 是 GET /connectors 列表视图的文档 key 集合
// （docs/bots/06-target-architecture.md §2.5 与 03-host-integration.md）。
var connectorViewKeys = []string{
	"id", "provider", "name", "enabled", "mode", "settings", "secret_keys",
	"policy", "created_at", "updated_at", "bot_identity", "bot_account",
	"active_owner", "owned_by_current_host", "runtime",
}

// TestConnectorsListKeySetContract 用一条全字段数据断言列表视图 key 集合：
// 缺省字段（settings/secret_keys/active_owner/runtime）必须真的出现，
// 不允许多出未文档化字段，也不允许少字段。
func TestConnectorsListKeySetContract(t *testing.T) {
	cfg := fullContractConfig()
	fx := newContractFixture(t, cfg)
	if claimed, owner, err := fx.store.ClaimConnectorOwner(cfg.ID, "runtime-server", time.Now().UTC(), time.Hour); err != nil || !claimed || owner != "runtime-server" {
		t.Fatalf("预置运行归属失败：claimed=%v owner=%q err=%v", claimed, owner, err)
	}

	recorder := do(t, fx.router, http.MethodGet, "/connectors", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d，want 1（%s）", len(payload.Items), recorder.Body.String())
	}
	assertKeySet(t, "GET /connectors items[0]", payload.Items[0], connectorViewKeys)

	// 值也要完整：确认覆盖到的不是空壳（omitempty 字段实际有内容）。
	var decoded struct {
		Items []ConnectorView `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	view := decoded.Items[0]
	if view.ActiveOwner != "runtime-server" || !view.OwnedByCurrentHost {
		t.Fatalf("归属字段 = %q/%v", view.ActiveOwner, view.OwnedByCurrentHost)
	}
	if view.Runtime == nil {
		t.Fatal("runtime 缺失（Stats 应返回零值快照）")
	}
	if len(view.Settings) == 0 || len(view.SecretKeys) != 1 || view.SecretKeys[0] != "bot_token" {
		t.Fatalf("settings/secret_keys = %v/%v", view.Settings, view.SecretKeys)
	}
	if len(view.Policy.AllowedUsers) != 2 || view.Policy.ReplyMode != "stream" {
		t.Fatalf("policy = %+v", view.Policy)
	}
}

// TestErrorPayloadShapeContract 断言两类错误载荷形状：
//   - {error}（单键，如 404 未知连接器）；
//   - {error,fields:[{key,message}]}（校验失败，fields 元素恰为 key/message）。
func TestErrorPayloadShapeContract(t *testing.T) {
	fx := newContractFixture(t)

	missing := do(t, fx.router, http.MethodGet, "/connectors/nope", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知连接器应 404，got %d", missing.Code)
	}
	single := decodeRawObject(t, missing.Body.Bytes())
	assertKeySet(t, "404 错误载荷", single, []string{"error"})
	var message string
	if err := json.Unmarshal(single["error"], &message); err != nil || strings.TrimSpace(message) == "" {
		t.Fatalf("error 应为非空字符串：%s", missing.Body.String())
	}

	invalid := do(t, fx.router, http.MethodPost, "/connectors", `{"id":"bad","provider":"goldenweb"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("缺少必填字段应 400，got %d %s", invalid.Code, invalid.Body.String())
	}
	payload := decodeRawObject(t, invalid.Body.Bytes())
	assertKeySet(t, "400 校验错误载荷", payload, []string{"error", "fields"})
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(payload["fields"], &fields); err != nil || len(fields) == 0 {
		t.Fatalf("fields 应为非空数组：%s", invalid.Body.String())
	}
	assertKeySet(t, "400 fields[0]", fields[0], []string{"key", "message"})
	var key string
	if err := json.Unmarshal(fields[0]["key"], &key); err != nil || key != "bot_token" {
		t.Fatalf("fields[0].key = %q，want bot_token（%s）", key, invalid.Body.String())
	}
}

// fullContractConfig 是覆盖列表视图全部字段的一条完整数据。
func fullContractConfig() connectors.Config {
	return connectors.Config{
		ID:        "golden-1",
		Provider:  "goldenweb",
		Name:      "Golden 工作群",
		Enabled:   true,
		Mode:      "webhook",
		CreatedAt: time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC),
		Settings: map[string]string{
			"display_name":    "工作群",
			"callback_url":    "https://example.com/hooks/connectors/golden-1",
			"allow_media":     "true",
			"account_id":      "ilink-bot-golden",
			"owner_user_id":   "ilink-user-golden",
			"timeout_seconds": "30",
		},
		Secrets: map[string]string{"bot_token": "tok-secret"},
		Policy: connectors.Policy{
			CommandPrefix: "/",
			AllowedUsers:  []string{"u1", "u2"},
			SessionMode:   "chat",
			ReplyMode:     "stream",
			RuntimeOwner:  "runtime-server",
		},
	}
}

// canonicalJSON 把响应体归一为稳定序列化：map key 排序（encoding/json 保证）、
// 两空格缩进、末尾单个换行——与 golden 文件逐字节比较的基准。
func canonicalJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, raw)
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func decodeRawObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("载荷不是 JSON object：%v（%s）", err, raw)
	}
	return object
}

func assertKeySet(t *testing.T, what string, object map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("%s key 集合漂移：got %v，want %v", what, got, expected)
	}
}
