package agentconfig

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderProxyUpdate describes a partial update to providers.items.<name>.proxy.
// Nil fields are not touched, so unrelated proxy values are preserved.
type ProviderProxyUpdate struct {
	Name    string
	HTTP    *string
	HTTPS   *string
	NoProxy *string
	Enabled *bool
}

// ProviderProxyResult reports the outcome of a provider proxy set/remove
// operation.
type ProviderProxyResult struct {
	ConfigPath string       `json:"config_path,omitempty"`
	Name       string       `json:"name"`
	Proxy      *ProxyConfig `json:"proxy,omitempty"`
	Removed    bool         `json:"removed"`
}

// ValidateProxyURL checks that raw is a parseable proxy URL with a supported
// scheme (http/https/socks5) and a host.
func ValidateProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("proxy url is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid proxy url %q: %w", raw, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("unsupported proxy url scheme %q in %q (supported: http, https, socks5)", parsed.Scheme, raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("proxy url %q must include host:port", raw)
	}
	return nil
}

// proxyUpdateFields is the shared partial-update surface for both provider
// level (providers.items.<name>.proxy) and global (providers.proxy) proxies.
// Nil fields are not touched, so unrelated proxy values are preserved.
type proxyUpdateFields struct {
	HTTP    *string
	HTTPS   *string
	NoProxy *string
	Enabled *bool
}

// apply merges the non-nil fields into merged and reports whether anything
// changed. Empty strings clear the corresponding field; non-empty http/https
// values are validated.
func (f proxyUpdateFields) apply(merged *ProxyConfig) (bool, error) {
	changed := false
	if f.HTTP != nil {
		value := strings.TrimSpace(*f.HTTP)
		if value != "" {
			if err := ValidateProxyURL(value); err != nil {
				return false, err
			}
		}
		if merged.HTTP != value {
			merged.HTTP = value
			changed = true
		}
	}
	if f.HTTPS != nil {
		value := strings.TrimSpace(*f.HTTPS)
		if value != "" {
			if err := ValidateProxyURL(value); err != nil {
				return false, err
			}
		}
		if merged.HTTPS != value {
			merged.HTTPS = value
			changed = true
		}
	}
	if f.NoProxy != nil {
		value := strings.TrimSpace(*f.NoProxy)
		if merged.NoProxy != value {
			merged.NoProxy = value
			changed = true
		}
	}
	if f.Enabled != nil {
		if merged.Enabled != *f.Enabled {
			merged.Enabled = *f.Enabled
			changed = true
		}
	}
	return changed, nil
}

// SetProviderProxyConfig adds or updates providers.items.<name>.proxy.
// Fields in update that are nil keep their current values; a non-nil pointer
// with an empty string clears that field. Enabled keeps its current value when
// update.Enabled is nil; a provider that never had a proxy defaults to enabled.
func SetProviderProxyConfig(configPath, name string, update ProviderProxyUpdate) (*ProviderProxyResult, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("provider name is required")
	}

	_, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	providerNode, canonical, ok := findProviderNodeInDocument(root, name)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", name)
	}

	current := &Provider{}
	if err := decodeYAMLNode(providerNode, current); err != nil {
		return nil, fmt.Errorf("decode provider %s: %w", canonical, err)
	}

	merged := &ProxyConfig{}
	if current.Proxy != nil {
		merged = current.Proxy.Clone()
	} else if update.Enabled == nil {
		// A brand-new proxy is enabled by default unless the caller
		// explicitly requests a disabled one.
		merged.Enabled = true
	}
	changed, err := proxyUpdateFields{
		HTTP:    update.HTTP,
		HTTPS:   update.HTTPS,
		NoProxy: update.NoProxy,
		Enabled: update.Enabled,
	}.apply(merged)
	if err != nil {
		return nil, err
	}
	if !changed {
		if merged.IsEmpty() {
			return nil, fmt.Errorf("provider %q has no proxy configuration to update", canonical)
		}
		return &ProviderProxyResult{ConfigPath: configPath, Name: canonical, Proxy: merged}, nil
	}

	if _, err := UpdateProviderConfig(configPath, ProviderConfigUpdate{Name: canonical, Proxy: merged}); err != nil {
		return nil, err
	}
	return &ProviderProxyResult{ConfigPath: configPath, Name: canonical, Proxy: merged}, nil
}

// GlobalProxyUpdate describes a partial update to providers.proxy (global).
// Nil fields are not touched; a non-nil pointer with an empty string clears
// that field.
type GlobalProxyUpdate struct {
	HTTP    *string
	HTTPS   *string
	NoProxy *string
	Enabled *bool
}

// GlobalProxyResult reports the outcome of a global proxy set/remove operation.
type GlobalProxyResult struct {
	ConfigPath string       `json:"config_path,omitempty"`
	Proxy      *ProxyConfig `json:"proxy,omitempty"`
	Removed    bool         `json:"removed"`
}

// SetGlobalProxyConfig adds or updates providers.proxy, the global proxy that
// applies to every provider without a proxy of its own. A provider-level proxy
// (providers.items.<name>.proxy) still overrides it at runtime.
func SetGlobalProxyConfig(configPath string, update GlobalProxyUpdate) (*GlobalProxyResult, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}

	document, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}

	current := &ProxyConfig{}
	hasNode := false
	if providersNode := mappingValue(root, "providers"); providersNode != nil {
		if proxyNode := mappingValue(providersNode, "proxy"); proxyNode != nil {
			hasNode = true
			if err := decodeYAMLNode(proxyNode, current); err != nil {
				return nil, fmt.Errorf("decode providers.proxy: %w", err)
			}
		}
	}

	merged := &ProxyConfig{}
	if hasNode {
		merged = current.Clone()
	} else if update.Enabled == nil {
		// A brand-new global proxy is enabled by default unless the caller
		// explicitly requests a disabled one.
		merged.Enabled = true
	}
	changed, err := proxyUpdateFields{
		HTTP:    update.HTTP,
		HTTPS:   update.HTTPS,
		NoProxy: update.NoProxy,
		Enabled: update.Enabled,
	}.apply(merged)
	if err != nil {
		return nil, err
	}
	if !changed {
		if merged.IsEmpty() {
			return nil, fmt.Errorf("global proxy has no proxy configuration to update")
		}
		return &GlobalProxyResult{ConfigPath: configPath, Proxy: merged}, nil
	}

	providersNode := ensureChildMapping(root, "providers")
	upsertYAMLMappingValue(providersNode, "proxy", providerProxyYAMLNode(*merged))
	if err := writeProviderConfigDocument(configPath, document); err != nil {
		return nil, err
	}
	return &GlobalProxyResult{ConfigPath: configPath, Proxy: merged}, nil
}

// RemoveGlobalProxyConfig deletes providers.proxy entirely. Provider-level
// proxies (providers.items.<name>.proxy) keep working.
func RemoveGlobalProxyConfig(configPath string) (*GlobalProxyResult, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}

	document, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	providersNode := mappingValue(root, "providers")
	if providersNode == nil || mappingValue(providersNode, "proxy") == nil {
		return nil, fmt.Errorf("no global proxy configured")
	}
	removeYAMLMappingValue(providersNode, "proxy")
	if err := writeProviderConfigDocument(configPath, document); err != nil {
		return nil, err
	}
	return &GlobalProxyResult{ConfigPath: configPath, Removed: true}, nil
}

// RemoveProviderProxyConfig deletes providers.items.<name>.proxy entirely.
// The global providers.proxy (if any) keeps working for the provider.
func RemoveProviderProxyConfig(configPath, name string) (*ProviderProxyResult, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("provider name is required")
	}

	_, root, err := readProviderConfigDocument(configPath)
	if err != nil {
		return nil, err
	}
	providerNode, canonical, ok := findProviderNodeInDocument(root, name)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	if mappingValue(providerNode, "proxy") == nil {
		return nil, fmt.Errorf("provider %q has no proxy configured", canonical)
	}
	if _, err := UpdateProviderConfig(configPath, ProviderConfigUpdate{Name: canonical, ClearProxy: true}); err != nil {
		return nil, err
	}
	return &ProviderProxyResult{ConfigPath: configPath, Name: canonical, Removed: true}, nil
}

// findProviderNodeInDocument locates providers.items.<name> in a parsed config
// document, matching names case-insensitively. It returns the provider node and
// the canonical name found in the file.
func findProviderNodeInDocument(root *yaml.Node, name string) (*yaml.Node, string, bool) {
	if root == nil {
		return nil, "", false
	}
	providersNode := mappingValue(root, "providers")
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return nil, "", false
	}
	itemsNode := mappingValue(providersNode, "items")
	if itemsNode == nil || itemsNode.Kind != yaml.MappingNode {
		return nil, "", false
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for i := 0; i+1 < len(itemsNode.Content); i += 2 {
		key := strings.TrimSpace(itemsNode.Content[i].Value)
		if key == "" {
			continue
		}
		if key == name || strings.ToLower(key) == lower {
			node := itemsNode.Content[i+1]
			if node != nil && node.Kind == yaml.MappingNode {
				return node, key, true
			}
			return nil, key, false
		}
	}
	return nil, "", false
}
