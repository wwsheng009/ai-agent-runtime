package connectors

import "testing"

// ChannelKey 必须跨宿主稳定：同一 bot 配置在网关/节点上得到同一个键。
func TestChannelKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"wechat identity", Config{ID: "c1", Provider: "wechat", Settings: map[string]string{"owner_user_id": "owner-1"}}, "wechat:owner-1"},
		{"telegram token prefix", Config{ID: "c2", Provider: "telegram", Secrets: map[string]string{"bot_token": "123456:abc-def"}}, "telegram:123456"},
		{"qq app id", Config{ID: "c3", Provider: "qqbot", Settings: map[string]string{"app_id": "app-1"}}, "qqbot:app-1"},
		{"feishu app id", Config{ID: "c4", Provider: "feishu", Settings: map[string]string{"app_id": "app-2"}}, "feishu:app-2"},
		{"fallback to connector id", Config{ID: "c5", Provider: "telegram"}, "telegram:conn:c5"},
	}
	for _, tc := range cases {
		if got := ChannelKey(tc.cfg); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
