package agentconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// GatewayPreferenceUpdate describes a partial update to the persisted
// gateway.* section (docs/gateway/12-connection-control.md §6.2 layered keys).
// Every field uses the three-state pointer discipline shared by the other
// persisted sections: nil = do not modify; non-nil = write the trimmed value
// (including the empty string to clear it).
type GatewayPreferenceUpdate struct {
	// URL nil=不修改；非 nil 写入 TrimSpace 后的值（空串=清除）。
	URL *string
	// Token 同 URL 的三态语义。令牌是机密：写盘时原样落 YAML（与 provider
	// api_key 同一密度），展示侧永远只经过 TokenHint。
	Token *string
	// Name 同 URL 的三态语义。
	Name *string
	// Enabled 同 URL 的三态语义；config 写路径用派生值（url+token 同时非空）
	// 或显式 false 关闭。
	Enabled *bool
	// AutoConnect 三态：外层 nil=不修改；非 nil 写入该布尔（区分"未设置（默认
	// true）"与"显式 false"）。
	AutoConnect *bool
}

// UpdateGatewayConfig updates the gateway section inside a config file without
// rewriting unrelated top-level sections.
//
// 整份「读-改-写」在 config_file_write.go 的写事务内完成：与 chat / theme /
// provider 等写者共用同一把配置文件写锁（§3.4 M11 / §12 R4）。分层加载开启时
// gateway.* 的写回按 key 路由到来源层（routeConfigWritePath），新键落到最高
// 可写层。
func UpdateGatewayConfig(configPath string, update GatewayPreferenceUpdate) (*GatewayConfig, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	// Layered configs: gateway.* edits go back to the layer that owns the
	// section (design §7 / D3). The keys are declared exhaustively so a partial
	// update still routes through the section origin.
	keys := gatewayUpdateWriteKeys(update)
	configPath = routeConfigWritePath(configPath, keys...)

	var current *GatewayConfig
	err := updateConfigFileDocument(configPath, configDocumentWriteOptions{createStarterWhenMissing: true}, func(_ *yaml.Node, root *yaml.Node) error {
		loaded, err := currentGatewayConfig(root)
		if err != nil {
			return err
		}
		applyGatewayPreferenceUpdate(loaded, update)

		sectionNode, err := marshalYAMLNode(loaded)
		if err != nil {
			return err
		}
		upsertYAMLMappingValue(root, "gateway", sectionNode)
		current = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

// gatewayUpdateWriteKeys lists the config key paths a gateway update touches.
func gatewayUpdateWriteKeys(update GatewayPreferenceUpdate) []string {
	keys := []string{"gateway"}
	if update.URL != nil {
		keys = append(keys, "gateway.url")
	}
	if update.Token != nil {
		keys = append(keys, "gateway.token")
	}
	if update.Name != nil {
		keys = append(keys, "gateway.name")
	}
	if update.Enabled != nil {
		keys = append(keys, "gateway.enabled")
	}
	if update.AutoConnect != nil {
		keys = append(keys, "gateway.auto_connect")
	}
	return keys
}

func applyGatewayPreferenceUpdate(current *GatewayConfig, update GatewayPreferenceUpdate) {
	if current == nil {
		return
	}
	if update.URL != nil {
		current.URL = strings.TrimSpace(*update.URL)
	}
	if update.Token != nil {
		current.Token = strings.TrimSpace(*update.Token)
	}
	if update.Name != nil {
		current.Name = strings.TrimSpace(*update.Name)
	}
	if update.Enabled != nil {
		current.Enabled = *update.Enabled
	}
	if update.AutoConnect != nil {
		// AutoConnect is a pointer in GatewayConfig so "unset (default true)"
		// stays distinct from an explicit false.
		value := *update.AutoConnect
		current.AutoConnect = &value
	}
}

// currentGatewayConfig decodes the gateway section from the YAML root; a
// missing section yields the zero config (not an error) so the first
// `/gateway config ...` write creates it.
func currentGatewayConfig(root *yaml.Node) (*GatewayConfig, error) {
	current := &GatewayConfig{}
	if root == nil {
		return current, nil
	}
	gatewayNode := mappingValue(root, "gateway")
	if gatewayNode == nil {
		return current, nil
	}
	if err := decodeYAMLNode(gatewayNode, current); err != nil {
		return nil, fmt.Errorf("decode gateway section: %w", err)
	}
	return current, nil
}
