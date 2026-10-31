package restapi

import (
	"net/http"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestUpdateRuntimeModeSwitchesForm 锁定管理面可切换连接器运行形态
// （只出站副本 ↔ 网关自持）：这是「网关自持，不依赖节点」的开关面，
// 且 Origin* 必须保持不变（切换收/发形态不改变复制来源）。
func TestUpdateRuntimeModeSwitchesForm(t *testing.T) {
	cfg := baseConfig("gw-1-wechat", "fakeweb")
	cfg.RuntimeMode = connectors.RuntimeModeOutbound
	cfg.OriginNodeID = "node-a"
	cfg.OriginConnectorID = "wechat-1"
	fx := newFixture(t, allowAll, cfg)

	toSelfHosted := do(t, fx.router, http.MethodPatch, "/connectors/gw-1-wechat",
		`{"runtime_mode":"gateway","settings":{"inbound_mode":"notify-only"}}`)
	if toSelfHosted.Code != http.StatusOK {
		t.Fatalf("切换 runtime_mode 应 200，got %d %s", toSelfHosted.Code, toSelfHosted.Body.String())
	}
	updated, _ := fx.store.GetConfig("gw-1-wechat")
	if updated.RuntimeMode != connectors.RuntimeModeGateway {
		t.Fatalf("runtime_mode = %q, want gateway", updated.RuntimeMode)
	}
	if !updated.InboundNotifyOnly() {
		t.Fatalf("gateway + notify-only 应生效：%+v", updated.Settings)
	}
	if updated.OriginNodeID != "node-a" || updated.OriginConnectorID != "wechat-1" {
		t.Fatalf("切换运行形态不得改动 Origin*：%+v", updated)
	}

	// 只提交 inbound_mode（不带 runtime_mode）：运行形态保持不变。
	toggleInbound := do(t, fx.router, http.MethodPatch, "/connectors/gw-1-wechat",
		`{"settings":{"inbound_mode":""}}`)
	if toggleInbound.Code != http.StatusOK {
		t.Fatalf("切换入站策略应 200，got %d %s", toggleInbound.Code, toggleInbound.Body.String())
	}
	if got, _ := fx.store.GetConfig("gw-1-wechat"); got.InboundNotifyOnly() {
		t.Fatalf("inbound_mode 清空后应回到 agent 策略：%+v", got.Settings)
	}

	// 切回只出站副本。
	back := do(t, fx.router, http.MethodPatch, "/connectors/gw-1-wechat", `{"runtime_mode":"outbound"}`)
	if back.Code != http.StatusOK {
		t.Fatalf("切回只出站副本应 200，got %d %s", back.Code, back.Body.String())
	}
	if got, _ := fx.store.GetConfig("gw-1-wechat"); got.RuntimeMode != connectors.RuntimeModeOutbound {
		t.Fatalf("runtime_mode = %q, want outbound", got.RuntimeMode)
	}

	// 未知取值必须 400 且不落盘（不做静默降级）。
	bad := do(t, fx.router, http.MethodPatch, "/connectors/gw-1-wechat", `{"runtime_mode":"self"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("未知 runtime_mode 应 400，got %d %s", bad.Code, bad.Body.String())
	}
	if got, _ := fx.store.GetConfig("gw-1-wechat"); got.RuntimeMode != connectors.RuntimeModeOutbound {
		t.Fatalf("400 后不得改动落盘值：%q", got.RuntimeMode)
	}
}

// TestCreateAcceptsRuntimeMode 锁定新建连接器可直接声明运行形态（网关原生自建
// bot 用 "" = 正常轮询；自持形态用 "gateway"）。
func TestCreateAcceptsRuntimeMode(t *testing.T) {
	fx := newFixture(t, allowAll)

	created := do(t, fx.router, http.MethodPost, "/connectors",
		`{"id":"gw-2-wechat","provider":"fakeweb","name":"网关自建","runtime_mode":"gateway","settings":{"bot_token":"tok"}}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("新建应 201，got %d %s", created.Code, created.Body.String())
	}
	if cfg, ok := fx.store.GetConfig("gw-2-wechat"); !ok || cfg.RuntimeMode != connectors.RuntimeModeGateway {
		t.Fatalf("新建未落到 gateway 形态：%+v ok=%v", cfg, ok)
	}

	bad := do(t, fx.router, http.MethodPost, "/connectors",
		`{"id":"gw-3-wechat","provider":"fakeweb","runtime_mode":"self","settings":{"bot_token":"tok"}}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("未知 runtime_mode 应 400，got %d %s", bad.Code, bad.Body.String())
	}
}